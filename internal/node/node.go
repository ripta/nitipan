// Package node is the per-machine, per-user daemon. It mirrors the machine's leases to the broker,
// holds the machine's one broker connection, and forwards the CLI's requests.
//
// The node opens every connection. The broker never connects in.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// Errors a node returns.
var (
	// ErrNoMachineID means the operating system gave no machine ID.
	ErrNoMachineID = errors.New("the hardware UUID could not be read. Retry, or report it")

	// ErrExecutableChanged means the node's executable was replaced. launchd restarts the node on the
	// new binary.
	ErrExecutableChanged = errors.New("the node executable changed")
)

const (
	defaultBackoff   = time.Second
	maxBackoff       = 30 * time.Second
	defaultExecEvery = 5 * time.Second
	debounce         = 100 * time.Millisecond
	dedupeWindow     = 1024
)

// Options configure a node.
type Options struct {
	// StateDir is the absolute path of the state directory.
	StateDir string

	// UID and User identify the OS user the node runs as.
	UID  int
	User string

	// Machine names this machine. Machine.ID is the hardware UUID, and the node refuses to start
	// without one.
	Machine record.Machine

	// Self describes this process for the presence record.
	SelfPID   int
	SelfStart record.ProcessStart

	// Version is the node's build version.
	Version string

	Config config.Node

	// ExecPath is the executable the node watches. When it changes, the node exits.
	ExecPath string

	// CodexPath and CodexHome come from the node's LaunchAgent arguments, never a registration or
	// another process. An empty path disables Codex pushes.
	CodexPath string
	CodexHome string
	// Home is the node user's home from the operating-system account database.
	Home string

	// ExecEvery is how often the executable is checked. Default: 5 seconds.
	ExecEvery time.Duration

	// Heartbeat overrides the heartbeat interval. Default: a third of the broker's term.
	Heartbeat time.Duration

	// Backoff is the first pause between connection attempts. It doubles up to 30 seconds. Default:
	// one second.
	Backoff time.Duration

	// Watch watches the leases directory. Default: SystemWatch.
	Watch WatchFunc

	// Forward handles each request kind the node knows. Default: the ping request.
	Forward map[string]Forwarder

	// OnEvent sees each event the broker streams. An error leaves the event unacked, so the broker
	// sends it again after a reconnect.
	OnEvent func(wire.Event) error

	// Checker judges the liveness of the holders of broker leases. Nil reads every holder as alive.
	Checker *liveness.Checker

	// StateOptions are the state directory options the node takes its lock with.
	StateOptions state.Options

	// Roots derives the repo entries of a mirrored lease that has no usable derived file. Nil leaves such
	// a lease unknown.
	Roots *rootset.Deriver

	// DeriveBudget bounds the time one snapshot spends deriving entries. Default: DefaultDeriveBudget.
	DeriveBudget time.Duration

	// Finder finds checkout boundaries, for the check that an anchor still covers a lease's paths. Nil
	// means no boundary exists.
	Finder keys.BoundaryFinder

	// Clock paces renewals. Default: CLOCK_MONOTONIC.
	Clock monoclock.Clock

	// WakeGap is how long the loop may go between ticks before the node treats the gap as a sleep.
	// ReplyGrace is how long a reply may wait unread before it becomes a notice. Defaults: DefaultWakeGap
	// and DefaultReplyGrace.
	WakeGap    time.Duration
	ReplyGrace time.Duration

	// MailEvery is how often the node expires and prunes mail, and reaps dead registrations. Default:
	// DefaultMailEvery.
	MailEvery time.Duration

	// Warn receives warnings, such as plain HTTP. Nil drops them.
	Warn func(string)

	// Now is wall-clock time, for request ages. Default: time.Now.
	Now func() time.Time
}

// Forwarder sends a handed-off request to the broker and returns the reply body.
type Forwarder func(ctx context.Context, req handoff.Request) (json.RawMessage, error)

// Node is a running node.
type Node struct {
	opts  Options
	cl    *client
	https bool
	term  time.Duration
	keep  time.Duration
	label atomic.Value

	// actingFor is true once the broker said at registration that it reads explicit acting_for. A
	// broker that predates it ignores the field and replaces the value, so a claim for another user
	// would count in the wrong scope.
	actingFor atomic.Bool

	// mailReplies is true once the broker said at registration that it reads a mail send's reply_to. A
	// broker that predates it would carry a reply as a first message.
	mailReplies atomic.Bool

	// reportDue is true from registration until the complete mail report reaches the broker. The broker
	// holds mail from another user until then, so the node keeps trying.
	reportDue atomic.Bool

	seen   map[string]struct{}
	seenQ  []string
	forwrd map[string]Forwarder

	// recallNext is the earliest deadline, in Unix nanoseconds, among the recalls in force the last
	// snapshot found. Zero means none. The event loop resends the mirror when the clock reaches it.
	recallNext atomic.Int64

	// recallWarnMu guards recallWarned.
	recallWarnMu sync.Mutex

	// recallWarned holds the recall problems already warned about, so a heartbeat does not repeat them.
	recallWarned map[string]string

	book       *leaseBook
	deadlineMu sync.Mutex
	written    map[string]bool

	// ringAfter holds, for each registration, the time before which a failed socket ring is not tried again.
	ringMu    sync.Mutex
	ringAfter map[string]time.Time

	// mailMu serializes the mail code that reads registrations and judges their liveness. The stream
	// goroutine and the main loop both do.
	mailMu sync.Mutex
}

// New validates the options. It touches no file and opens no connection.
func New(opts Options) (*Node, error) {
	if opts.Machine.ID == "" {
		return nil, ErrNoMachineID
	}

	base, https, httpClient, err := brokerHTTP(opts.Config)
	if err != nil {
		return nil, err
	}

	if opts.Backoff == 0 {
		opts.Backoff = defaultBackoff
	}

	if opts.ExecEvery == 0 {
		opts.ExecEvery = defaultExecEvery
	}

	if opts.Watch == nil {
		opts.Watch = SystemWatch
	}

	if opts.Now == nil {
		opts.Now = time.Now
	}

	if opts.Clock == nil {
		opts.Clock = monoclock.System{}
	}

	if opts.WakeGap == 0 {
		opts.WakeGap = DefaultWakeGap
	}

	if opts.MailEvery == 0 {
		opts.MailEvery = DefaultMailEvery
	}

	n := &Node{
		opts:  opts,
		https: https,
		seen:  map[string]struct{}{},
		book:  newBook(),

		written:   map[string]bool{},
		ringAfter: map[string]time.Time{},
		cl: &client{
			base:      base,
			http:      httpClient,
			machineID: opts.Machine.ID,
			uid:       opts.UID,
		},
	}

	n.forwrd = map[string]Forwarder{KindPing: n.forwardPing}
	n.registerLeaseKinds()
	n.registerMailKinds()

	for k, f := range opts.Forward {
		n.forwrd[k] = f
	}

	if !https {
		if err := refusePlain(opts.StateDir); err != nil {
			return nil, err
		}

		n.warn("the broker at " + opts.Config.Broker + " is reached over plain HTTP. Anyone on the path can read and change leases. " +
			"Give the broker a certificate.")
	}

	return n, nil
}

// Label returns the label the broker gave this node, once registered.
func (n *Node) Label() string {
	label, _ := n.label.Load().(string)

	return label
}

func (n *Node) warn(msg string) {
	if n.opts.Warn != nil {
		n.opts.Warn(msg)
	}
}

// Run serves until ctx ends or the executable changes. It returns nil on a cancelled context.
func (n *Node) Run(ctx context.Context) error {
	dir, err := state.Open(n.opts.StateDir, state.Options{UID: n.opts.UID})
	if err != nil {
		return err
	}

	lock, err := lockNode(dir.Path())
	if err != nil {
		return err
	}

	defer lock.Close()

	key, err := LoadKey(dir.Path())
	if err != nil {
		return err
	}

	n.cl.key = key

	exe, err := n.execFingerprint()
	if err != nil {
		return err
	}

	err = handoff.WritePresence(dir.Path(), handoff.Presence{
		PID:            n.opts.SelfPID,
		Start:          n.opts.SelfStart,
		Machine:        n.opts.Machine,
		NodeVersion:    n.opts.Version,
		RequestVersion: handoff.RequestVersion,
		CodexPath:      n.opts.CodexPath,
		CodexHome:      n.opts.CodexHome,
		Kinds:          n.kinds(),
	})
	if err != nil {
		return err
	}

	defer handoff.RemovePresence(dir.Path())

	return n.reconnectLoop(ctx, exe)
}

func (n *Node) execFingerprint() (fingerprint, error) {
	if n.opts.ExecPath == "" {
		return fingerprint{}, nil
	}

	return statExec(n.opts.ExecPath)
}

func (n *Node) reconnectLoop(ctx context.Context, exe fingerprint) error {
	backoff := n.opts.Backoff

	for {
		started := time.Now()

		err := n.session(ctx, exe)

		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrExecutableChanged):
			return err
		}

		n.warn(fmt.Sprintf("lost the broker: %v", err))

		if time.Since(started) > 2*maxBackoff {
			backoff = n.opts.Backoff
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

// session registers, opens the stream, and then sends snapshots until the stream or the executable
// goes away.
func (n *Node) session(ctx context.Context, exe fingerprint) error {
	sctx, streamErr, cleanup, err := n.setupSession(ctx)
	if err != nil {
		return err
	}

	defer cleanup()

	changes, regChanges, recallChanges, err := n.watchState(sctx)
	if err != nil {
		return err
	}

	if err := n.sendSnapshot(sctx); err != nil {
		return err
	}

	n.maintain(sctx)

	return n.runEventLoop(ctx, sctx, exe, streamErr, changes, regChanges, recallChanges)
}

func (n *Node) setupSession(ctx context.Context) (sctx context.Context, streamErr <-chan error, cleanup func(), err error) {
	reg, err := n.register(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	n.actingFor.Store(reg.ActingForExplicit)
	n.mailReplies.Store(reg.MailReplies)
	n.label.Store(reg.Label)
	n.term = time.Duration(reg.TermMillis) * time.Millisecond
	n.keep = time.Duration(reg.KeepaliveMillis) * time.Millisecond

	if reg.CrossUser {
		if err := RecordCrossUserEnabled(n.opts.StateDir); err != nil {
			return nil, nil, nil, fmt.Errorf("recording cross-user flag: %w", err)
		}
	}

	n.book.setConnected(true)
	n.reportDue.Store(true)
	n.retryReport(ctx)

	var cancel context.CancelFunc
	sctx, cancel = context.WithCancel(ctx)
	exited := make(chan struct{})
	errChan := make(chan error, 1)
	streamErr = errChan

	go func() {
		defer close(exited)
		errChan <- n.stream(sctx)
	}()

	cleanup = func() {
		cancel()
		<-exited
		n.book.setConnected(false)
		n.publishDeadlinesNow()
	}

	return
}

func (n *Node) runEventLoop(ctx, sctx context.Context, exe fingerprint, streamErr <-chan error, changes, regChanges, recallChanges <-chan struct{}) error {
	lastTick := n.clockNow()

	beat := n.opts.Heartbeat
	if beat == 0 {
		beat = max(n.term/3, 50*time.Millisecond)
	}

	heart := time.NewTicker(beat)
	defer heart.Stop()

	execTick := time.NewTicker(n.opts.ExecEvery)
	defer execTick.Stop()

	reqTick := time.NewTicker(250 * time.Millisecond)
	defer reqTick.Stop()

	mailTick := time.NewTicker(n.opts.MailEvery)
	defer mailTick.Stop()

	var pending <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-streamErr:
			return fmt.Errorf("the stream ended: %w", err)
		case <-changes:
			if pending == nil {
				pending = time.After(debounce)
			}
		case <-regChanges:
			if pending == nil {
				pending = time.After(debounce)
			}
		case <-recallChanges:
			if pending == nil {
				pending = time.After(debounce)
			}
		case <-pending:
			pending = nil

			if err := n.sendSnapshot(sctx); err != nil {
				return err
			}
		case <-heart.C:
			if err := n.heartbeat(sctx); err != nil {
				return err
			}

			n.maintain(sctx)
		case <-reqTick.C:
			now := n.clockNow()

			if now-lastTick > n.opts.WakeGap {
				if err := n.sendSnapshot(sctx); err != nil {
					return err
				}

				n.maintain(sctx)
			}

			lastTick = now

			n.serveRequests(sctx)
			n.deliverLate()
			n.ringMail()
			n.reportReads(sctx)

			if n.recallDeadlineReached() {
				if err := n.sendSnapshot(sctx); err != nil {
					return err
				}
			}
		case <-mailTick.C:
			n.retryReport(sctx)
			n.serviceMail(sctx)
		case <-execTick.C:
			if n.executableChanged(exe) {
				return ErrExecutableChanged
			}
		}
	}
}

// recallDeadlineReached reports whether the clock reached the earliest deadline of a recall in force.
//
// A deadline passing changes no file, so nothing else would resend the mirror. The caller only resends.
// It writes no lease state and ends no sub-lease.
//
// Every snapshot sets the deadline again, so a resend re-arms the check.
func (n *Node) recallDeadlineReached() bool {
	next := n.recallNext.Load()

	return next != 0 && n.opts.Now().UnixNano() >= next
}

// watchState watches the leases directory, the recalls directory, and the mail registrations. A new
// or changed registration reports its session IDs to the broker without waiting for the next heartbeat,
// so mail sent to a fresh session does not fail as "no such session". A new, changed, or cancelled
// recall changes what the mirror says, so it resends too.
func (n *Node) watchState(ctx context.Context) (leases, registrations, recalls <-chan struct{}, err error) {
	leases, err = n.opts.Watch(ctx, n.leasesPath())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("watching the leases directory: %w", err)
	}

	registrations, err = n.opts.Watch(ctx, n.opts.StateDir+"/"+mailbox.RegDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("watching the mail registrations: %w", err)
	}

	recalls, err = n.opts.Watch(ctx, n.opts.StateDir+"/"+state.RecallsDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("watching the recalls directory: %w", err)
	}

	return leases, registrations, recalls, nil
}

func (n *Node) leasesPath() string {
	return n.opts.StateDir + "/" + state.LeasesDir
}

func (n *Node) executableChanged(was fingerprint) bool {
	if n.opts.ExecPath == "" {
		return false
	}

	now, err := statExec(n.opts.ExecPath)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}

	return err == nil && now != was
}

func (n *Node) register(ctx context.Context) (wire.Registered, error) {
	var out wire.Registered

	err := n.cl.call(ctx, wire.PathRegister, wire.Register{
		Version:     wire.Current,
		MachineID:   n.opts.Machine.ID,
		UID:         n.opts.UID,
		User:        n.opts.User,
		Host:        n.opts.Machine.Host,
		NodeVersion: n.opts.Version,
		HTTPS:       n.https,

		MailCapability: wire.MailCapability,
	}, &out)
	if err != nil {
		return out, fmt.Errorf("registering: %w", err)
	}

	if err := out.Version.Check(); err != nil {
		return out, err
	}

	return out, nil
}

func (n *Node) sendSnapshot(ctx context.Context) error {
	return n.sendSnapshotAck(ctx)
}

func (n *Node) heartbeat(ctx context.Context) error {
	if err := n.cl.call(ctx, wire.PathHeartbeat, wire.Heartbeat{Version: wire.Current}, nil); err != nil {
		return err
	}

	return n.sendSnapshot(ctx)
}
