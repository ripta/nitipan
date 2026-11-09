package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Ledger is the ring ledger of one registration: the keys some wake path already rang for. A waking
// path rings once per key, so a session that ignores its ring cannot be woken forever. The keys here
// are message IDs, written as "mail:<id>". Notices and requests join as keys of their own.
//
// The file sits beside the registration, so a waiter with no node running reads it too. It carries
// ADR-04's major.minor. A ledger a path cannot use reads as empty, so the path may ring again, which a
// repeated ring allows.
type Ledger struct {
	Version  record.Version       `json:"version"`
	Identity string               `json:"identity"`
	Rung     map[string]time.Time `json:"rung"`
}

// MailKey is the ledger key of a mail message.
func MailKey(id string) string { return "mail:" + id }

// RingRetention is how long a rung key stays in the ledger.
const RingRetention = 8 * 24 * time.Hour

// ledgerState says whether a ledger file can be used and rewritten.
type ledgerState int

const (
	ledgerOK ledgerState = iota

	// ledgerNewer is readable but of a higher minor. ledgerUnusable cannot be decoded or has another
	// major. Neither is ever rewritten.
	ledgerNewer
	ledgerUnusable
)

func loadLedger(root, identity string) (Ledger, ledgerState) {
	l := Ledger{Identity: identity, Rung: map[string]time.Time{}}

	data, err := state.ReadName(root, RingDir, identity+fileSuffix)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return l, ledgerOK
		}

		return l, ledgerUnusable
	}

	var got Ledger
	if json.Unmarshal(data, &got) != nil || checkVersion(got.Version) != nil || got.Identity != identity || got.Rung == nil {
		return l, ledgerUnusable
	}

	if got.Version.Minor > Version.Minor {
		return got, ledgerNewer
	}

	return got, ledgerOK
}

// Rung reads the keys already rung for an identity. A ledger this binary cannot use reads as empty.
func Rung(root, identity string) map[string]time.Time {
	l, st := loadLedger(root, identity)
	if st == ledgerUnusable {
		return map[string]time.Time{}
	}

	return l.Rung
}

// lockLedger takes the identity's ledger lock, so a waiter and a node never ring the same key twice.
func lockLedger(root, identity string) (unlock func(), err error) {
	dir := filepath.Join(root, RingDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", RingDir, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, identity+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the ring ledger lock: %w", err)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("locking the ring ledger: %w", err)
	}

	return func() { _ = f.Close() }, nil
}

func writeLedger(root string, l Ledger) error {
	l.Version = Version

	data, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encoding the ring ledger: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, RingDir), l.Identity+fileSuffix, data)
}

// Claim marks the keys it returns as rung and returns the ones no path rang for yet. A caller rings
// only for what Claim returned. It prunes keys older than the retention as it writes.
//
// A ledger this binary cannot use, or of a higher minor, is never rewritten. Claim then returns the keys
// the ledger does not show as rung, with an error that matches ErrNotRewritten. The caller may ring for
// them, since a repeated ring loses nothing, and should not ring again soon.
func Claim(root, identity string, keys []string, now time.Time) ([]string, error) {
	unlock, err := lockLedger(root, identity)
	if err != nil {
		return nil, err
	}

	defer unlock()

	l, st := loadLedger(root, identity)

	var fresh []string

	for _, k := range keys {
		if _, done := l.Rung[k]; !done {
			fresh = append(fresh, k)
			l.Rung[k] = now.UTC()
		}
	}

	if st != ledgerOK {
		return fresh, fmt.Errorf("ring ledger %s: %w", identity, ErrNotRewritten)
	}

	if len(fresh) == 0 {
		return nil, nil
	}

	for k, at := range l.Rung {
		if now.Sub(at) > RingRetention {
			delete(l.Rung, k)
		}
	}

	if err := writeLedger(root, l); err != nil {
		return nil, err
	}

	return fresh, nil
}

// Unclaim removes keys a path claimed and then failed to ring, so a later path rings for them. A ledger
// that was not rewritten holds nothing to remove.
func Unclaim(root, identity string, keys []string) error {
	unlock, err := lockLedger(root, identity)
	if err != nil {
		return err
	}

	defer unlock()

	l, st := loadLedger(root, identity)
	if st != ledgerOK {
		return nil
	}

	for _, k := range keys {
		delete(l.Rung, k)
	}

	return writeLedger(root, l)
}

// WaiterPresence is the file an armed waiter publishes. A newer waiter for the same session replaces
// an older one, which sees it is no longer named and exits.
type WaiterPresence struct {
	Version  record.Version      `json:"version"`
	Identity string              `json:"identity"`
	PID      int                 `json:"pid"`
	Start    record.ProcessStart `json:"start"`
	Machine  record.Machine      `json:"machine"`
	ArmedAt  time.Time           `json:"armed_at"`
}

// Arm publishes the waiter's presence, replacing any earlier waiter's.
func Arm(root string, w WaiterPresence) error {
	if problem := WaiterProblem(root, w.Identity); problem != "" {
		return fmt.Errorf("waiter presence %s (%s): %w", w.Identity, problem, ErrNotRewritten)
	}

	w.Version = Version

	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("encoding the waiter presence: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, WaitDir), w.Identity+fileSuffix, data)
}

// WaiterProblem says why a presence file exists but cannot be used or replaced, or returns the empty
// string when there is none or it is fine.
func WaiterProblem(root, identity string) string {
	data, err := state.ReadName(root, WaitDir, identity+fileSuffix)
	if err != nil {
		return ""
	}

	var w WaiterPresence
	if err := json.Unmarshal(data, &w); err != nil {
		return "it cannot be decoded"
	}

	if err := checkVersion(w.Version); err != nil {
		return err.Error()
	}

	if w.Version.Minor > Version.Minor {
		return fmt.Sprintf("it is version %s, and this nitipan writes %s", w.Version, Version)
	}

	return ""
}

// Waiter reads the presence file of an identity. A missing, unreadable, or unknown-version file
// returns false.
func Waiter(root, identity string) (WaiterPresence, bool) {
	data, err := state.ReadName(root, WaitDir, identity+fileSuffix)
	if err != nil {
		return WaiterPresence{}, false
	}

	var w WaiterPresence
	if json.Unmarshal(data, &w) != nil || checkVersion(w.Version) != nil || w.Identity != identity || w.PID <= 0 {
		return WaiterPresence{}, false
	}

	return w, true
}

// Disarm removes the presence file when it still names the process.
func Disarm(root string, w WaiterPresence) {
	cur, ok := Waiter(root, w.Identity)
	if !ok || cur.PID != w.PID || !cur.Start.Equal(w.Start) {
		return
	}

	_ = os.Remove(filepath.Join(root, WaitDir, w.Identity+fileSuffix))
}
