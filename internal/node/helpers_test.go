package node

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/tlstest"
)

type localVolume struct{}

func (localVolume) Local(string) error { return nil }

// rig is a broker over httptest and the state directory a node runs in.
type rig struct {
	t     *testing.T
	b     *broker.Broker
	store *broker.Store
	clock *monoclock.Fake
	srv   *httptest.Server
	state string

	// intercept, when set, sees each request first. It returns true when it answered.
	intercept atomic.Pointer[func(http.ResponseWriter, *http.Request) bool]
}

func newRig(t *testing.T) *rig {
	t.Helper()

	store, err := broker.OpenStore(filepath.Join(t.TempDir(), "broker.json"), broker.StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	clock := monoclock.NewFake(time.Hour)
	b := broker.New(broker.Config{Store: store, Term: 5 * time.Minute, Keepalive: 30 * time.Millisecond, Clock: clock, Version: "test"})
	r := &rig{t: t, b: b, store: store, clock: clock}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if f := r.intercept.Load(); f != nil && (*f)(w, req) {
			return
		}

		b.Handler().ServeHTTP(w, req)
	}))

	t.Cleanup(srv.Close)

	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	r.srv = srv
	r.state = filepath.Join(stateDir, "state")

	return r
}

var testMachine = record.Machine{Host: "laptop", ID: "machine-1"}

func (r *rig) options() Options {
	return Options{
		StateDir:  r.state,
		UID:       os.Getuid(),
		User:      "tester",
		Machine:   testMachine,
		SelfPID:   os.Getpid(),
		SelfStart: record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1},
		Version:   "test",
		Config:    config.Node{Broker: r.srv.URL},
		Heartbeat: 40 * time.Millisecond,
		Backoff:   10 * time.Millisecond,
		ExecEvery: 20 * time.Millisecond,
	}
}

// running is a node whose Run is in progress.
type running struct {
	n      *Node
	cancel context.CancelFunc
	done   chan error
}

func (r *rig) start(opts Options) *running {
	r.t.Helper()

	n, err := New(opts)
	if err != nil {
		r.t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &running{n: n, cancel: cancel, done: make(chan error, 1)}

	go func() { run.done <- n.Run(ctx) }()

	r.t.Cleanup(func() { _ = run.stop(r.t) })

	return run
}

func (run *running) stop(t *testing.T) error {
	t.Helper()

	run.cancel()

	return run.wait(t, 5*time.Second)
}

// wait returns the error Run ended with. It can be called again, since it puts the result back.
func (run *running) wait(t *testing.T, limit time.Duration) error {
	t.Helper()

	select {
	case err := <-run.done:
		run.done <- err

		return err
	case <-time.After(limit):
		t.Fatalf("the node did not stop within %v", limit)
	}

	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func (r *rig) view() (broker.NodeView, bool) {
	for _, v := range r.b.Nodes() {
		if v.Key.MachineID == testMachine.ID && v.Key.UID == os.Getuid() {
			return v, true
		}
	}

	return broker.NodeView{}, false
}

func (r *rig) waitLeases(n int) broker.NodeView {
	r.t.Helper()

	var got broker.NodeView

	eventually(r.t, "the broker to mirror leases", func() bool {
		v, ok := r.view()
		got = v

		return ok && v.HasSnapshot && len(v.Leases) == n
	})

	return got
}

// writeRecord publishes a valid V1 path record into leases/ as a V1 binary would, and returns its
// bytes.
func writeRecord(t *testing.T, stateDir, path string) (id string, data []byte) {
	t.Helper()

	id = state.NewID()

	rec := record.Record{
		Envelope: record.Envelope{
			Version:       record.PathVersion,
			ID:            id,
			Footprint:     record.PathFootprint([]keys.Key{{Path: path}}),
			ClearWhenDead: true,
			Liveness:      record.LivenessProcess,
			Machine:       testMachine,
			Holder:        record.Holder{Agent: "claude", PID: os.Getpid(), Start: record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1}},
		},
		Reach:      record.ReachMachine,
		Domain:     record.Domain{Kind: record.KindPath, Checkout: path, Keys: []keys.Key{{Path: path}}},
		ActingFor:  "tester",
		AcquiredAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}

	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(stateDir, state.LeasesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	return id, data
}

func newRequestFor(url string) (*http.Request, error) {
	return http.NewRequest(http.MethodGet, url, nil)
}

// newTLSBroker serves the rig's broker over TLS with the given certificate and returns its URL.
func newTLSBroker(t *testing.T, r *rig, files tlstest.Files) string {
	t.Helper()

	srv := httptest.NewUnstartedServer(r.b.Handler())

	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		t.Fatal(err)
	}

	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()

	t.Cleanup(srv.Close)

	return srv.URL
}
