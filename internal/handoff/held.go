package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// Request kinds the node forwards to the broker.
const (
	KindClaim   = "claim"
	KindRelease = "release"
	KindBreak   = "break"
	KindRenew   = "renew"
	KindList    = "list"
	KindFence   = "fence"

	// KindWhoHolds and KindWhoIs are the discovery queries.
	KindWhoHolds = "who-holds"
	KindWhoIs    = "who-is"
	KindWhoAmI   = "who-am-i"
)

// Directories beside the request directories. A V1 binary never scans broker/.
const (
	HeldDir      = "broker/held"
	DeadlinesDir = "broker/deadlines"
	InflightDir  = "broker/inflight"
	RootsCache   = "broker/rootsets.json"
)

// FileVersion is the version of the sidecar, deadline, and in-flight files this binary writes. A reader
// follows the record format's rule: a higher minor reads, a higher major is unusable.
var FileVersion = record.Version{Major: 1, Minor: 0}

// SidecarVersion is the version of the sidecar this binary writes. Minor 1 adds Token. A reader of minor 0
// ignores the field, and the CLI writes it from the reply it got, so a node that predates it cannot leave
// a sidecar without a token the broker returned.
var SidecarVersion = record.Version{Major: 1, Minor: 1}

// ErrUnusableFile means a sidecar or deadline file is of a major this binary does not read, or cannot
// be decoded. A caller treats the thing it describes as unknown, which means unchecked.
var ErrUnusableFile = errors.New("the file cannot be used by this nitipan")

// HolderKey is the digest that tells the sessions of one node apart. It hashes the fields the local
// holder match rule compares.
func HolderKey(h record.Holder, liveness string) string {
	parts := []string{h.Agent}

	if liveness == record.LivenessSession {
		parts = append(parts, h.SessionID)
	} else {
		parts = append(parts, fmt.Sprint(h.PID), h.Start.String(), h.SessionID)
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	return hex.EncodeToString(sum[:16])
}

// Anchor is the machine lease that a wider-reach lease hangs on.
type Anchor struct {
	ID string `json:"id"`

	// Keys are the anchor's keys when the claim was made. Covered are the keys the claim itself covers.
	Keys    []keys.Key `json:"keys"`
	Covered []keys.Key `json:"covered"`

	// Created says the claim took the anchor new. A later report that the anchor already covers a claim
	// clears it.
	Created bool `json:"created"`
}

// ClaimBody is the body of a claim request. The request ID is the lease ID.
type ClaimBody struct {
	Reach       string          `json:"reach"`
	Entries     []repoid.Name   `json:"entries"`
	Derivation  int             `json:"derivation"`
	Anchor      Anchor          `json:"anchor"`
	Holder      record.Envelope `json:"holder"`
	ActingFor   string          `json:"acting_for"`
	Description string          `json:"description,omitempty"`
}

// ReleaseBody is the body of a release request.
type ReleaseBody struct {
	ID     string          `json:"id,omitempty"`
	Name   *repoid.Name    `json:"name,omitempty"`
	Holder record.Envelope `json:"holder"`
}

// BreakBody is the body of a break request.
type BreakBody struct {
	ID    string        `json:"id"`
	Force bool          `json:"force"`
	By    record.Holder `json:"by"`
}

// FenceBody is the body of a fence report request. See wire.FenceReport.
type FenceBody struct {
	Token uint64 `json:"token"`
}

// RenewBody is the body of a synchronous renew request.
type RenewBody struct {
	IDs    []string        `json:"ids"`
	Holder record.Envelope `json:"holder"`
}

// Sidecar is the node's record of a wider-reach lease, keyed by lease ID. The link to the anchor
// lives here and never in the anchor's record, so the anchor keeps its V1 version.
type Sidecar struct {
	Version record.Version `json:"version"`
	ID      string         `json:"id"`
	Reach   string         `json:"reach"`

	Anchor     Anchor        `json:"anchor"`
	Entries    []repoid.Name `json:"entries"`
	Derivation int           `json:"derivation"`

	// Holder is the envelope a notice is addressed to, and the one liveness is judged by.
	Holder      record.Envelope `json:"holder"`
	Description string          `json:"description,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`

	// Token is the fence token of the broker's grant. Zero means none is known yet, as for a lease granted
	// before tokens existed.
	Token uint64 `json:"token,omitempty"`
}

// WriteSidecar writes a sidecar under the state lock.
func WriteSidecar(l *state.Locked, sc Sidecar) error {
	sc.Version = SidecarVersion

	data, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("encoding the sidecar for %s: %w", sc.ID, err)
	}

	return l.ReplaceNamed(HeldDir, sc.ID+fileSuffix, append(data, '\n'))
}

// RemoveSidecar deletes a sidecar under the state lock.
func RemoveSidecar(l *state.Locked, id string) error {
	err := l.RemoveNamed(HeldDir, id+fileSuffix)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the sidecar for %s: %w", id, err)
	}

	return nil
}

func decodeVersioned(data []byte, into any) error {
	var head struct {
		Version record.Version `json:"version"`
	}

	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("decoding: %w", ErrUnusableFile)
	}

	if head.Version.Major != FileVersion.Major {
		return fmt.Errorf("version %s: %w", head.Version, ErrUnusableFile)
	}

	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("decoding: %w", ErrUnusableFile)
	}

	return nil
}

// ReadSidecar reads one sidecar. A missing one matches fs.ErrNotExist. One this binary cannot use
// matches ErrUnusableFile.
func ReadSidecar(root, id string) (Sidecar, error) {
	data, err := state.ReadName(root, HeldDir, id+fileSuffix)
	if err != nil {
		return Sidecar{}, err
	}

	var sc Sidecar
	if err := decodeVersioned(data, &sc); err != nil {
		return Sidecar{}, fmt.Errorf("sidecar %s: %w", id, err)
	}

	return sc, nil
}

// Sidecars lists every usable sidecar, and the lease IDs whose sidecar cannot be used.
func Sidecars(root string) (usable []Sidecar, unknown []string, err error) {
	names, err := state.ListNames(root, HeldDir)
	if err != nil {
		return nil, nil, err
	}

	for _, name := range names {
		id, ok := strings.CutSuffix(name, fileSuffix)
		if !ok {
			continue
		}

		sc, err := ReadSidecar(root, id)

		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			unknown = append(unknown, id)
		default:
			usable = append(usable, sc)
		}
	}

	return usable, unknown, nil
}

// DeadlineLease is one broker lease in a holder's deadline file.
type DeadlineLease struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Liveness string `json:"liveness,omitempty"`

	// LapsesAt is the wall-clock time the node expects the broker to lapse the lease, counted from
	// when the acked renewal was sent.
	LapsesAt time.Time `json:"lapses_at"`
}

// Deadlines is the file a node publishes for each holder. The CLI reads it to say that a holder's broker
// leases are at risk, and when they lapse.
type Deadlines struct {
	Version   record.Version  `json:"version"`
	Holder    string          `json:"holder"`
	Connected bool            `json:"connected"`
	WrittenAt time.Time       `json:"written_at"`
	Leases    []DeadlineLease `json:"leases"`
}

// WriteDeadlines publishes the file atomically. The node writes it without the state lock, since it is the
// only writer.
func WriteDeadlines(root string, d Deadlines) error {
	d.Version = FileVersion

	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding the deadline file: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, DeadlinesDir), d.Holder+fileSuffix, data)
}

// RemoveDeadlines deletes a holder's deadline file.
func RemoveDeadlines(root, holder string) {
	_ = os.Remove(filepath.Join(root, DeadlinesDir, holder+fileSuffix))
}

// ReadDeadlines reads a holder's deadline file. A missing file matches fs.ErrNotExist, and an unusable one
// matches ErrUnusableFile.
func ReadDeadlines(root, holder string) (Deadlines, error) {
	data, err := state.ReadName(root, DeadlinesDir, holder+fileSuffix)
	if err != nil {
		return Deadlines{}, err
	}

	var d Deadlines
	if err := decodeVersioned(data, &d); err != nil {
		return Deadlines{}, fmt.Errorf("deadline file %s: %w", holder, err)
	}

	return d, nil
}

// Risk describes what a deadline file says at now: the lines a command prints for a holder whose node is
// disconnected or whose leases ran out.
func (d Deadlines) Risk(now time.Time) []string {
	var out []string

	for _, l := range d.Leases {
		switch {
		case l.State == "live" && !l.LapsesAt.After(now):
			out = append(out, fmt.Sprintf("broker lease %s lapsed at %s", l.ID, l.LapsesAt.Local().Format(time.RFC3339)))
		case l.State == "lapsed":
			out = append(out, fmt.Sprintf("broker lease %s lapsed at %s", l.ID, l.LapsesAt.Local().Format(time.RFC3339)))
		case l.State == "live" && !d.Connected:
			out = append(out, fmt.Sprintf("broker lease %s is at risk: the node is disconnected from the broker, and the lease lapses at %s unless it reconnects",
				l.ID, l.LapsesAt.Local().Format(time.RFC3339)))
		}
	}

	return out
}

// Inflight is the record of a request whose outcome the CLI did not see. A release keeps the sidecars
// it asked to remove, so a retry after a lost reply can still decide about their anchors.
type Inflight struct {
	Version   record.Version `json:"version"`
	ID        string         `json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	Sidecars  []Sidecar      `json:"sidecars,omitempty"`
}

// InflightKey names the in-flight record of one command: the same holder, kind, and body.
func InflightKey(holder, kind string, body any) string {
	data, _ := json.Marshal(body)
	sum := sha256.Sum256(append([]byte(holder+"\x00"+kind+"\x00"), data...))

	return hex.EncodeToString(sum[:16])
}

// LookupInflight returns the request ID a timed-out command left, when it is still inside the request
// window.
func LookupInflight(root, key string, now time.Time) (string, bool) {
	data, err := state.ReadName(root, InflightDir, key+fileSuffix)
	if err != nil {
		return "", false
	}

	var in Inflight
	if decodeVersioned(data, &in) != nil || !state.ValidID(in.ID) || now.Sub(in.CreatedAt) > MaxAge {
		return "", false
	}

	return in.ID, true
}

// NoteInflight records the ID of a request the CLI stopped waiting for, so a retry of the same command
// reuses it and the broker returns the same answer.
func NoteInflight(root, key, id string, now time.Time) error {
	return NoteInflightWith(root, key, id, now, InflightSidecars(root, key, now))
}

// NoteInflightWith is NoteInflight for a request that also names the sidecars it asks to remove.
func NoteInflightWith(root, key, id string, now time.Time, sidecars []Sidecar) error {
	data, err := json.Marshal(Inflight{Version: FileVersion, ID: id, CreatedAt: now, Sidecars: sidecars})
	if err != nil {
		return fmt.Errorf("encoding the in-flight record: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, InflightDir), key+fileSuffix, data)
}

// InflightSidecars returns the sidecars a release left in its in-flight record.
func InflightSidecars(root, key string, now time.Time) []Sidecar {
	data, err := state.ReadName(root, InflightDir, key+fileSuffix)
	if err != nil {
		return nil
	}

	var in Inflight
	if decodeVersioned(data, &in) != nil || now.Sub(in.CreatedAt) > MaxAge {
		return nil
	}

	return in.Sidecars
}

// ClearInflight forgets a command's in-flight record once its answer arrived.
func ClearInflight(root, key string) {
	_ = os.Remove(filepath.Join(root, InflightDir, key+fileSuffix))
}

// Consume removes a reply the CLI has read.
func Consume(root, id string) {
	_ = os.Remove(filepath.Join(root, AnsweredDir, id+fileSuffix))
}

// Answered lists the replies still waiting to be read, with their ages.
func Answered(root string, now time.Time) (map[string]time.Duration, error) {
	ids, err := listIDs(root, AnsweredDir)
	if err != nil {
		return nil, err
	}

	out := make(map[string]time.Duration, len(ids))

	for _, id := range ids {
		info, err := os.Stat(filepath.Join(root, AnsweredDir, id+fileSuffix))
		if err == nil {
			out[id] = now.Sub(info.ModTime())
		}
	}

	return out, nil
}

// ReleaseReply is the body of a release reply. It carries the sidecars the node removed, since the CLI
// needs the anchor facts to decide whether the anchor goes too.
type ReleaseReply struct {
	Result   wire.Result `json:"result"`
	Sidecars []Sidecar   `json:"sidecars,omitempty"`
}

// KindSince is the request version that introduced a kind. A request older than that is refused, since
// no CLI of that version could have written it. Returns 0 for unknown kinds.
func KindSince(kind string) int {
	switch kind {
	case "ping":
		return 1
	case KindClaim, KindRelease, KindBreak, KindRenew, KindList, KindFence:
		return 2
	case KindWhoHolds, KindWhoIs:
		return 3
	case KindMailSend, KindMailStatus, KindWhoAmI:
		return 4
	}

	return 0
}

// OrphanDir holds the anchors of claims whose refusal reached the CLI too late.
const OrphanDir = "broker/orphans"

// Orphan records a machine lease that a refused claim took new. The call that delivers the claim's
// late-refusal notice releases it, by the same rules a release uses.
type Orphan struct {
	Version record.Version  `json:"version"`
	ID      string          `json:"id"`
	Anchor  Anchor          `json:"anchor"`
	Holder  record.Envelope `json:"holder"`
}

// WriteOrphan writes an orphan under the state lock.
func WriteOrphan(l *state.Locked, o Orphan) error {
	o.Version = FileVersion

	data, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("encoding the orphan anchor for %s: %w", o.ID, err)
	}

	return l.ReplaceNamed(OrphanDir, o.ID+fileSuffix, append(data, '\n'))
}

// ReadOrphan reads the orphan a claim left. A missing one matches fs.ErrNotExist.
func ReadOrphan(root, id string) (Orphan, error) {
	data, err := state.ReadName(root, OrphanDir, id+fileSuffix)
	if err != nil {
		return Orphan{}, err
	}

	var o Orphan
	if err := decodeVersioned(data, &o); err != nil {
		return Orphan{}, fmt.Errorf("orphan anchor %s: %w", id, err)
	}

	return o, nil
}

// RemoveOrphan deletes an orphan under the state lock.
func RemoveOrphan(l *state.Locked, id string) error {
	err := l.RemoveNamed(OrphanDir, id+fileSuffix)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the orphan anchor for %s: %w", id, err)
	}

	return nil
}

// Request kinds for mail. A node of request version 3 or lower does not know them.
const (
	KindMailSend   = "mail-send"
	KindMailStatus = "mail-status"
)

// MailSendBody is the body of a mail-send request. The request ID is the message ID. FromSession is
// the sender's session, which the node reports to the broker from the sender's registration.
type MailSendBody struct {
	To          string `json:"to"`
	Body        string `json:"body"`
	FromAgent   string `json:"from_agent"`
	FromSession string `json:"from_session"`
}

// MailStatusBody is the body of a mail-status request.
type MailStatusBody struct {
	ID string `json:"id"`
}
