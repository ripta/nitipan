// Package handoff is the file exchange between the nitipan CLI and the node.
//
// A session may not reach the network from its sandbox, but the state directory is writable from
// every agent. The CLI publishes a request under broker/pending, the node moves it to claimed and
// forwards it to the broker, and the node publishes the reply under answered. V1 binaries never scan
// broker/.
package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Directories under the state directory.
const (
	PendingDir  = "broker/pending"
	ClaimedDir  = "broker/claimed"
	AnsweredDir = "broker/answered"

	// BrokerDir holds the presence record beside the three request directories.
	BrokerDir    = "broker"
	PresenceName = "presence.json"

	fileSuffix = ".json"
)

// Versions this binary writes. A reader refuses a version it does not know, and a node keeps
// answering every request version it ever accepted.
const (
	RequestVersion  = 5
	PresenceVersion = 1
)

// ActingForSince is the first claim request version whose acting_for is explicit: a user label the
// CLI asks to claim for, never an OS user name for the broker to replace. An older node answers such a
// claim "node too old", so it is never forwarded under a version whose acting_for the broker replaces.
const ActingForSince = 5

// MaxAge is how old a request may be when the node forwards it. The node judges by its own clock,
// which is the clock the CLI used, since both run on one machine.
const MaxAge = 24 * time.Hour

// Errors the CLI reports.
var (
	// ErrNodeNotRunning means no live node published a presence record.
	ErrNodeNotRunning = errors.New("node not running")

	// ErrNodeTooOld means the node does not know the request kind or version.
	ErrNodeTooOld = errors.New("node too old")

	// ErrRequestExpired means a request is older than MaxAge.
	ErrRequestExpired = errors.New("request expired")
)

// ErrIDMismatch means a request body names an ID other than its file name. A session could plant
// one to forge the reply to another live request.
var ErrIDMismatch = errors.New("request ID does not match its file name")

// Requester names the process that published a request, so a lock holder can reap what a dead
// requester left.
type Requester struct {
	PID     int                 `json:"pid"`
	Start   record.ProcessStart `json:"start"`
	Machine record.Machine      `json:"machine"`
}

func (r Requester) envelope() record.Envelope {
	return record.Envelope{
		Liveness: record.LivenessProcess,
		Machine:  r.Machine,
		Holder:   record.Holder{PID: r.PID, Start: r.Start},
	}
}

// Request is one broker request a session asks the node to forward. The ID is the idempotency key
// the broker sees.
type Request struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Requester Requester       `json:"requester"`
	CreatedAt time.Time       `json:"created_at"`
	Body      json.RawMessage `json:"body,omitempty"`
}

// Reply is the node's answer. Error is empty when the request succeeded.
type Reply struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Requester Requester       `json:"requester"`
	Error     *ReplyError     `json:"error,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
}

// Reply error codes.
const (
	CodeExpired = "request_expired"
	CodeTooOld  = "node_too_old"
	CodeBroker  = "broker_error"

	// CodeBrokerTooOld means the broker does not serve the call the request needs.
	CodeBrokerTooOld = "broker_too_old"
)

// ReplyError says why a request failed.
type ReplyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Presence is the record a live node publishes. The CLI checks it with the liveness rule before it
// writes a request.
type Presence struct {
	CodexPath string              `json:"codex_path,omitempty"`
	CodexHome string              `json:"codex_home,omitempty"`
	Version   int                 `json:"version"`
	PID       int                 `json:"pid"`
	Start     record.ProcessStart `json:"start"`
	Machine   record.Machine      `json:"machine"`

	NodeVersion string `json:"node_version"`

	// RequestVersion is the highest request version the node reads. Kinds are the request kinds it
	// knows.
	RequestVersion int      `json:"request_version"`
	Kinds          []string `json:"kinds"`
}

// Supports reports whether the node knows a request of this kind and version.
func (p Presence) Supports(kind string, version int) error {
	if version > p.RequestVersion {
		return fmt.Errorf("the node reads request version %d, this nitipan writes %d: %w", p.RequestVersion, version, ErrNodeTooOld)
	}

	if !slices.Contains(p.Kinds, kind) {
		return fmt.Errorf("the node does not know the %q request: %w", kind, ErrNodeTooOld)
	}

	return nil
}

// Expired reports whether a request is older than MaxAge at now.
func Expired(req Request, now time.Time) bool {
	return now.Sub(req.CreatedAt) > MaxAge
}

// ReadPresence reads the node's presence record. A missing record returns an error matching
// fs.ErrNotExist.
func ReadPresence(root string) (Presence, error) {
	data, err := os.ReadFile(filepath.Join(root, BrokerDir, PresenceName))
	if err != nil {
		return Presence{}, fmt.Errorf("reading the node presence record: %w", err)
	}

	var p Presence
	if err := json.Unmarshal(data, &p); err != nil {
		return Presence{}, fmt.Errorf("decoding the node presence record: %w", err)
	}

	if p.Version != PresenceVersion {
		return Presence{}, fmt.Errorf("the node presence record is version %d, this nitipan reads %d", p.Version, PresenceVersion)
	}

	return p, nil
}

// WritePresence publishes the presence record atomically.
func WritePresence(root string, p Presence) error {
	p.Version = PresenceVersion

	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding the presence record: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, BrokerDir), PresenceName, data)
}

// RemovePresence deletes the presence record when the node stops.
func RemovePresence(root string) {
	_ = os.Remove(filepath.Join(root, BrokerDir, PresenceName))
}

// NotRunningError carries the install text the user needs.
type NotRunningError struct {
	Install string
	Why     string
}

// Error implements error.
func (e *NotRunningError) Error() string {
	msg := "node not running"
	if e.Why != "" {
		msg += " (" + e.Why + ")"
	}

	return msg + ".\n" + strings.TrimRight(e.Install, "\n")
}

// Is matches ErrNodeNotRunning.
func (e *NotRunningError) Is(target error) bool {
	return target == ErrNodeNotRunning
}

// Require returns the presence of a live node. With none, it fails at once with the install text. A
// holder whose liveness reads unknown counts as running, as it does everywhere else.
func Require(root string, checker *liveness.Checker, install string) (Presence, error) {
	p, err := ReadPresence(root)
	if errors.Is(err, fs.ErrNotExist) {
		return Presence{}, &NotRunningError{Install: install, Why: "no presence record"}
	}

	if err != nil {
		return Presence{}, &NotRunningError{Install: install, Why: err.Error()}
	}

	env := record.Envelope{Liveness: record.LivenessProcess, Machine: p.Machine, Holder: record.Holder{PID: p.PID, Start: p.Start}}

	if res := checker.Check(env); res.Status == liveness.Dead {
		return Presence{}, &NotRunningError{Install: install, Why: "the process in the presence record has exited"}
	}

	return p, nil
}

// Submit publishes a request into pending. It takes no lock, and the request appears all at once.
func Submit(root string, req Request) error {
	version := KindSince(req.Kind)
	if version == 0 {
		version = RequestVersion
	}

	return SubmitVersion(root, req, version)
}

// SubmitVersion is Submit with an explicit request version. A test stands in for an older CLI with it.
func SubmitVersion(root string, req Request, version int) error {
	req.Version = version

	if !state.ValidID(req.ID) {
		return fmt.Errorf("%q: %w", req.ID, state.ErrBadID)
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding the request: %w", err)
	}

	return atomicfile.Write(filepath.Join(root, PendingDir), req.ID+fileSuffix, data)
}

// Pending lists the IDs waiting in pending, in ID order.
func Pending(root string) ([]string, error) {
	return listIDs(root, PendingDir)
}

// Withdraw removes a request that is still pending. It reports false when the node already claimed
// it, and the CLI then waits for the answer.
func Withdraw(root, id string) bool {
	return os.Remove(filepath.Join(root, PendingDir, id+fileSuffix)) == nil
}

// Claim moves a pending request to claimed with rename(2) and reads it. A request another claim
// took returns fs.ErrNotExist.
func Claim(root, id string) (Request, error) {
	from := filepath.Join(root, PendingDir, id+fileSuffix)
	to := filepath.Join(root, ClaimedDir, id+fileSuffix)

	if err := os.MkdirAll(filepath.Join(root, ClaimedDir), 0o700); err != nil {
		return Request{}, fmt.Errorf("creating %s: %w", ClaimedDir, err)
	}

	if err := os.Rename(from, to); err != nil {
		return Request{}, fmt.Errorf("claiming request %s: %w", id, err)
	}

	data, err := os.ReadFile(to)
	if err != nil {
		return Request{}, fmt.Errorf("reading claimed request %s: %w", id, err)
	}

	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{ID: id}, fmt.Errorf("decoding claimed request %s: %w", id, err)
	}

	if req.ID != id || !state.ValidID(req.ID) {
		return Request{ID: id}, fmt.Errorf("claimed request %s names the ID %q: %w", id, req.ID, ErrIDMismatch)
	}

	return req, nil
}

// Answer publishes the reply and then removes the claimed request.
func Answer(root string, rep Reply) error {
	rep.Version = RequestVersion

	data, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("encoding the reply: %w", err)
	}

	if err := atomicfile.Write(filepath.Join(root, AnsweredDir), rep.ID+fileSuffix, data); err != nil {
		return err
	}

	err = os.Remove(filepath.Join(root, ClaimedDir, rep.ID+fileSuffix))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing claimed request %s: %w", rep.ID, err)
	}

	return nil
}

// ReadReply reads the answer to a request. A request not yet answered returns an error matching
// fs.ErrNotExist.
func ReadReply(root, id string) (Reply, error) {
	data, err := os.ReadFile(filepath.Join(root, AnsweredDir, id+fileSuffix))
	if err != nil {
		return Reply{}, fmt.Errorf("reading the reply to %s: %w", id, err)
	}

	var rep Reply
	if err := json.Unmarshal(data, &rep); err != nil {
		return Reply{}, fmt.Errorf("decoding the reply to %s: %w", id, err)
	}

	return rep, nil
}

// Reap removes the requests and replies whose requester is dead, since nobody will read them. A
// lock holder runs it with the other repairs. A file that cannot be decoded, or whose body names
// another ID than its file name, is removed once it is older than the request window. It returns
// the first removal failure but tries every file.
func Reap(l *state.Locked, checker *liveness.Checker) error {
	root := l.Dir().Path()

	var first error

	for _, sub := range []string{PendingDir, ClaimedDir, AnsweredDir} {
		names, err := state.ListNames(root, sub)
		if err != nil {
			return err
		}

		for _, name := range names {
			if !strings.HasSuffix(name, fileSuffix) {
				continue
			}

			data, err := state.ReadName(root, sub, name)
			if err != nil {
				continue
			}

			var who struct {
				ID        string    `json:"id"`
				Requester Requester `json:"requester"`
			}

			id, _ := strings.CutSuffix(name, fileSuffix)

			if json.Unmarshal(data, &who) != nil || who.Requester.PID <= 0 || who.ID != id {
				if undecodableExpired(root, sub, name) {
					err = l.RemoveNamed(sub, name)
					if err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
						first = fmt.Errorf("reaping %s/%s: %w", sub, name, err)
					}
				}

				continue
			}

			if checker.Check(who.Requester.envelope()).Status != liveness.Dead {
				continue
			}

			err = l.RemoveNamed(sub, name)
			if err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
				first = fmt.Errorf("reaping %s/%s: %w", sub, name, err)
			}
		}
	}

	return first
}

// undecodableExpired reports whether a file that names no requester is older than the request
// window. Nobody can say whose it is, so age is the only test. A file this recent may still be
// mid-write by a newer binary.
func undecodableExpired(root, sub, name string) bool {
	info, err := os.Stat(filepath.Join(root, sub, name))

	return err == nil && time.Since(info.ModTime()) > MaxAge
}

func listIDs(root, sub string) ([]string, error) {
	names, err := state.ListNames(root, sub)
	if err != nil {
		return nil, err
	}

	var ids []string

	for _, n := range names {
		id, ok := strings.CutSuffix(n, fileSuffix)
		if ok && state.ValidID(id) {
			ids = append(ids, id)
		}
	}

	return ids, nil
}
