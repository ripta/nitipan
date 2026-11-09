// Package mailbox is the mail files in the state directory: stored messages, the ledger of IDs the
// node accepted, session registrations, the ring ledger, waiter presence, and the sender's stage
// notes.
//
// The tree lives under mail/, which V1 binaries never scan. Every file carries a major.minor version
// under ADR-04's reader rules. One this binary cannot use reads as unknown: it is never rung, read, or
// rewritten, and `mail status` says why.
package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

// Directories under the state directory.
const (
	InboxDir    = "mail/inbox"
	AcceptedDir = "mail/accepted"
	ReadDir     = "mail/read"
	RegDir      = "mail/reg"
	RingDir     = "mail/ring"
	WaitDir     = "mail/wait"
	SentDir     = "mail/sent"

	fileSuffix = ".json"
)

// Version is the format version every file here is written at.
var Version = record.Version{Major: 1, Minor: 0}

// Errors for the files here.
var (
	// ErrUnknownVersion means a file was written at a major this binary does not read.
	ErrUnknownVersion = errors.New("file version is not supported")

	// ErrNewer means a file was written at a higher minor, and this binary will not rewrite it.
	ErrNewer = errors.New("file was written by a newer nitipan and is not rewritten")

	// ErrNotRewritten means a file is unusable or of a higher minor than this binary writes, and the
	// write that would replace it was skipped. Rewriting it would drop what a newer nitipan put there.
	ErrNotRewritten = errors.New("file is unusable or of a newer minor and is not rewritten")

	// ErrBadName means a session ID or message ID is not safe as a file name.
	ErrBadName = errors.New("name is not safe as a file name")
)

// Message kinds.
const (
	KindMail   = "mail"
	KindBounce = "bounce"
)

// Message is one stored message. It never carries a credential.
type Message struct {
	Version record.Version `json:"version"`
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`

	ToSession string `json:"to_session"`
	ToAddress string `json:"to_address"`

	// Sender is the <mail name>@<label> the broker stamped. SenderVerified is absent for an unverified
	// sender, and a reader treats absent as unverified.
	Sender         string `json:"sender,omitempty"`
	SenderVerified bool   `json:"sender_verified,omitempty"`

	Body string `json:"body"`

	// Reason and Orig are set on a bounce. BodyDropped says the broker held no body to return.
	Reason      string `json:"reason,omitempty"`
	Orig        string `json:"orig,omitempty"`
	BodyDropped bool   `json:"body_dropped,omitempty"`

	StoredAt time.Time `json:"stored_at"`
}

// Unknown is a file this binary cannot use, and why.
type Unknown struct {
	Name   string
	Reason string
}

// checkVersion judges a decoded version. A higher minor reads fine.
func checkVersion(v record.Version) error {
	if v.Major != Version.Major {
		return fmt.Errorf("version %s, this nitipan reads %d.x: %w", v, Version.Major, ErrUnknownVersion)
	}

	return nil
}

func messageName(session, id string) string { return session + "." + id + fileSuffix }

// ValidID reports whether a message ID is safe as a file name part. A message ID is a request ID.
func ValidID(id string) bool { return state.ValidID(id) }

// Store publishes a message into the inbox and syncs the directory, so a power loss after the call
// cannot lose it. The node acks the broker only after Store returns.
func Store(root string, m Message) error {
	if !session.ValidSession(m.ToSession) || !ValidID(m.ID) {
		return fmt.Errorf("message %q for session %q: %w", m.ID, m.ToSession, ErrBadName)
	}

	m.Version = Version

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding message %s: %w", m.ID, err)
	}

	dir := filepath.Join(root, InboxDir)

	if err := atomicfile.Write(dir, messageName(m.ToSession, m.ID), data); err != nil {
		return err
	}

	return SyncDir(dir)
}

// SyncDir fsyncs a directory.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s to sync it: %w", dir, err)
	}

	defer d.Close()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}

	return nil
}

// Names lists the inbox file names that belong to any of the sessions, in name order. It is one
// directory read.
func Names(root string, sessions []string) ([]string, error) {
	all, err := state.ListNames(root, InboxDir)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, n := range all {
		for _, s := range sessions {
			if strings.HasPrefix(n, s+".") && strings.HasSuffix(n, fileSuffix) {
				out = append(out, n)

				break
			}
		}
	}

	return out, nil
}

// Pending reads the messages waiting for any of the sessions. A file of an unknown version or one that
// cannot be decoded is returned apart, and is never read as mail.
func Pending(root string, sessions []string) (msgs []Message, unknown []Unknown, err error) {
	names, err := Names(root, sessions)
	if err != nil {
		return nil, nil, err
	}

	for _, n := range names {
		data, err := state.ReadName(root, InboxDir, n)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			unknown = append(unknown, Unknown{Name: n, Reason: err.Error()})

			continue
		}

		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			unknown = append(unknown, Unknown{Name: n, Reason: "cannot be decoded: " + err.Error()})

			continue
		}

		if err := checkVersion(m.Version); err != nil {
			unknown = append(unknown, Unknown{Name: n, Reason: err.Error()})

			continue
		}

		if messageName(m.ToSession, m.ID) != n {
			unknown = append(unknown, Unknown{Name: n, Reason: "its name does not match its contents"})

			continue
		}

		msgs = append(msgs, m)
	}

	return msgs, unknown, nil
}

// Remove deletes a stored message.
func Remove(root string, m Message) error {
	err := os.Remove(filepath.Join(root, InboxDir, messageName(m.ToSession, m.ID)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing message %s: %w", m.ID, err)
	}

	return nil
}

// All lists every stored message of every session, for the node. It skips what it cannot use.
func All(root string) ([]Message, error) {
	names, err := state.ListNames(root, InboxDir)
	if err != nil {
		return nil, err
	}

	sessions := map[string]bool{}

	for _, n := range names {
		if s, _, ok := strings.Cut(n, "."); ok {
			sessions[s] = true
		}
	}

	var list []string
	for s := range sessions {
		list = append(list, s)
	}

	msgs, _, err := Pending(root, list)

	return msgs, err
}

// Accepted states of a message ID the node took in.
const (
	StateStored = "stored"
	StateRead   = "read"
)

// Accepted is one entry of the ledger of message IDs the node accepted. It is kept as long as the
// broker could still resend the message, so a resend after a crash between the write and the ack is
// dropped, even when the first copy was already consumed.
type Accepted struct {
	Version record.Version `json:"version"`
	ID      string         `json:"id"`
	State   string         `json:"state"`
	At      time.Time      `json:"at"`
}

// AcceptedRetention is how long a ledger entry stays. The broker never resends mail older than its
// 7-day retention, and the extra day covers clock skew between the machines.
const AcceptedRetention = 8 * 24 * time.Hour

// Accept records that a message ID is on disk.
func Accept(root, id string, at time.Time) error {
	return writeAccepted(root, Accepted{ID: id, State: StateStored, At: at})
}

func writeAccepted(root string, a Accepted) error {
	if !ValidID(a.ID) {
		return fmt.Errorf("message %q: %w", a.ID, ErrBadName)
	}

	a.Version = Version

	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding the ledger entry of %s: %w", a.ID, err)
	}

	return atomicfile.Write(filepath.Join(root, AcceptedDir), a.ID+fileSuffix, data)
}

// AcceptedExists reports whether the ledger holds a file for the ID, whatever its version. A file this
// binary cannot read still says the node took the message in, so a resend must not become a second copy.
func AcceptedExists(root, id string) bool {
	_, err := os.Stat(filepath.Join(root, AcceptedDir, id+fileSuffix))

	return err == nil
}

// AcceptedState returns the ledger state of an ID, and false when the node never accepted it.
func AcceptedState(root, id string) (string, bool) {
	data, err := state.ReadName(root, AcceptedDir, id+fileSuffix)
	if err != nil {
		return "", false
	}

	var a Accepted
	if json.Unmarshal(data, &a) != nil || checkVersion(a.Version) != nil {
		return "", false
	}

	return a.State, true
}

// AcceptedAll returns every ledger entry, keyed by message ID.
func AcceptedAll(root string) (map[string]Accepted, error) {
	names, err := state.ListNames(root, AcceptedDir)
	if err != nil {
		return nil, err
	}

	out := map[string]Accepted{}

	for _, n := range names {
		data, err := state.ReadName(root, AcceptedDir, n)
		if err != nil {
			continue
		}

		var a Accepted
		if json.Unmarshal(data, &a) != nil || checkVersion(a.Version) != nil || a.ID+fileSuffix != n {
			continue
		}

		out[a.ID] = a
	}

	return out, nil
}

// SetAcceptedRead marks a ledger entry read, after the node reported the read to the broker.
func SetAcceptedRead(root, id string, at time.Time) error {
	if AcceptedExists(root, id) {
		data, err := state.ReadName(root, AcceptedDir, id+fileSuffix)
		if err != nil {
			return fmt.Errorf("reading the ledger entry of %s: %w", id, err)
		}

		var cur Accepted
		if json.Unmarshal(data, &cur) != nil || checkVersion(cur.Version) != nil || cur.Version.Minor > Version.Minor {
			return fmt.Errorf("ledger entry %s: %w", id, ErrNotRewritten)
		}
	}

	return writeAccepted(root, Accepted{ID: id, State: StateRead, At: at})
}

// PruneAccepted removes ledger entries older than the retention.
func PruneAccepted(root string, now time.Time) error {
	all, err := AcceptedAll(root)
	if err != nil {
		return err
	}

	for id, a := range all {
		if now.Sub(a.At) > AcceptedRetention {
			if err := os.Remove(filepath.Join(root, AcceptedDir, id+fileSuffix)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("pruning the ledger entry of %s: %w", id, err)
			}
		}
	}

	return nil
}

// MarkRead records that a reader consumed a message, for the node to report. The reader owns this
// file and the node owns the ledger, so neither rewrites the other's.
func MarkRead(root, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("message %q: %w", id, ErrBadName)
	}

	data, err := json.Marshal(struct {
		Version record.Version `json:"version"`
		ID      string         `json:"id"`
	}{Version, id})
	if err != nil {
		return fmt.Errorf("encoding the read marker of %s: %w", id, err)
	}

	return atomicfile.Write(filepath.Join(root, ReadDir), id+fileSuffix, data)
}

// PendingReads lists the IDs readers consumed that the node has not reported.
func PendingReads(root string) ([]string, error) {
	names, err := state.ListNames(root, ReadDir)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, n := range names {
		if id, ok := strings.CutSuffix(n, fileSuffix); ok && ValidID(id) {
			out = append(out, id)
		}
	}

	return out, nil
}

// ClearRead removes a read marker once the node reported it.
func ClearRead(root, id string) {
	_ = os.Remove(filepath.Join(root, ReadDir, id+fileSuffix))
}

func removeFile(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}

	return nil
}
