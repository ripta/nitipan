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
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
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

	// Warn receives warnings, such as plain HTTP. Nil drops them.
	Warn func(string)

	// Now is wall-clock time, for request ages. Default: time.Now.
	Now func() time.Time
}

// Forwarder sends a handed-off request to the broker and returns the reply body.
type Forwarder func(ctx context.Context, req handoff.Request) (json.RawMessage, error)

// Node is a running node.
type Node struct {
	opts   Options
	cl     *client
	https  bool
	term   time.Duration
	keep   time.Duration
	label  atomic.Value
	seen   map[string]struct{}
	seenQ  []string
	forwrd map[string]Forwarder
}

// New validates the options. It touches no file and opens no connection.
func New(opts Options) (*Node, error) {
	if opts.Machine.ID == "" {
		return nil, ErrNoMachineID
	}

	base, https, err := brokerURL(opts.Config)
	if err != nil {
		return nil, err
	}

	tr, err := newTransport(opts.Config, !https)
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

	n := &Node{
		opts:  opts,
		https: https,
		seen:  map[string]struct{}{},
		cl: &client{
			base:      base,
			http:      &http.Client{Transport: tr},
			machineID: opts.Machine.ID,
			uid:       opts.UID,
		},
	}

	n.forwrd = map[string]Forwarder{KindPing: n.forwardPing}

	for k, f := range opts.Forward {
		n.forwrd[k] = f
	}

	if !https {
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

	key, err := loadKey(dir.Path())
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
	reg, err := n.register(ctx)
	if err != nil {
		return err
	}

	n.label.Store(reg.Label)
	n.term = time.Duration(reg.TermMillis) * time.Millisecond
	n.keep = time.Duration(reg.KeepaliveMillis) * time.Millisecond

	sctx, cancel := context.WithCancel(ctx)
	exited := make(chan struct{})

	// The stream goroutine shares the node's dedupe state and keepalive. The next session must not
	// start until it is gone.
	defer func() {
		cancel()
		<-exited
	}()

	streamErr := make(chan error, 1)

	go func() {
		defer close(exited)

		streamErr <- n.stream(sctx)
	}()

	changes, err := n.opts.Watch(sctx, n.leasesPath())
	if err != nil {
		return fmt.Errorf("watching the leases directory: %w", err)
	}

	if err := n.sendSnapshot(sctx); err != nil {
		return err
	}

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
		case <-pending:
			pending = nil

			if err := n.sendSnapshot(sctx); err != nil {
				return err
			}
		case <-heart.C:
			if err := n.heartbeat(sctx); err != nil {
				return err
			}
		case <-reqTick.C:
			n.serveRequests(sctx)
		case <-execTick.C:
			if n.executableChanged(exe) {
				return ErrExecutableChanged
			}
		}
	}
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
	snap, err := n.snapshot()
	if err != nil {
		return fmt.Errorf("reading the leases to mirror: %w", err)
	}

	return n.cl.call(ctx, wire.PathSnapshot, snap, nil)
}

func (n *Node) heartbeat(ctx context.Context) error {
	if err := n.cl.call(ctx, wire.PathHeartbeat, wire.Heartbeat{Version: wire.Current}, nil); err != nil {
		return err
	}

	return n.sendSnapshot(ctx)
}
