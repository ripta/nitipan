package cli

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/node"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/tlstest"
	"github.com/space-armada/nitipan/internal/wire"
)

type okVolume struct{}

func (okVolume) Local(string) error { return nil }

const nodePID = 9000

// wideFixture is a broker over httptest and the machines whose nodes connect to it.
type wideFixture struct {
	t     *testing.T
	b     *broker.Broker
	store *broker.Store
	srv   *httptest.Server
	clock *monoclock.Fake

	// gate, when set, holds every request for the path until the channel closes.
	gate atomic.Pointer[gateRule]

	// missing, when set, answers 404 for the path, as a broker that predates the call would.
	missing atomic.Pointer[string]

	// caFile is the certificate nodes trust when the fixture serves TLS. Empty for plain HTTP.
	caFile string
}

type gateRule struct {
	path string
	open chan struct{}
	once sync.Once
}

func (g *gateRule) release() { g.once.Do(func() { close(g.open) }) }

func newWideFixture(t *testing.T) *wideFixture {
	t.Helper()

	return newWideFixtureWith(t, false)
}

// newWideFixtureTLS serves the broker over TLS, which mail requires of a node.
func newWideFixtureTLS(t *testing.T) *wideFixture {
	t.Helper()

	return newWideFixtureWith(t, true)
}

func newWideFixtureWith(t *testing.T, useTLS bool) *wideFixture {
	t.Helper()

	store, err := broker.OpenStore(filepath.Join(t.TempDir(), "broker.json"), broker.StoreOptions{Volume: okVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	f := &wideFixture{t: t, store: store, clock: monoclock.NewFake(time.Hour)}
	f.b = broker.New(broker.Config{Store: store, Term: 5 * time.Minute, Keepalive: 30 * time.Millisecond, Clock: f.clock, Version: "test"})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := f.missing.Load(); p != nil && *p == r.URL.Path {
			http.NotFound(w, r)

			return
		}

		if g := f.gate.Load(); g != nil && g.path == r.URL.Path {
			select {
			case <-g.open:
			case <-r.Context().Done():
				return
			}
		}

		f.b.Handler().ServeHTTP(w, r)
	})

	f.srv = httptest.NewUnstartedServer(handler)

	if useTLS {
		files := tlstest.SelfSigned(t, t.TempDir())

		cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
		if err != nil {
			t.Fatal(err)
		}

		f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		f.caFile = files.Cert

		f.srv.StartTLS()
	} else {
		f.srv.Start()
	}

	t.Cleanup(f.srv.Close)
	t.Cleanup(func() {
		if g := f.gate.Swap(nil); g != nil {
			g.release()
		}
	})

	return f
}

// machine is one world with a node on the fixture's broker. start runs the node once the test has
// added every process it will use, since the fake process table is not safe to change while the node
// reads it.
type wideMachine struct {
	f      *wideFixture
	w      *world
	now    atomic.Pointer[time.Time]
	cancel context.CancelFunc
	done   chan error

	replyGrace time.Duration
}

func (f *wideFixture) machine(id string) *wideMachine {
	f.t.Helper()

	w := newWorld(f.t)
	w.machine = record.Machine{Host: "host-" + id, ID: id}
	w.handoffWait = 5 * time.Second
	w.table.Add(nodePID, proctest.Process{Start: startOf(nodePID)})

	m := &wideMachine{f: f, w: w, done: make(chan error, 1)}
	m.now.Store(&w.now)

	return m
}

func (m *wideMachine) start() {
	m.w.t.Helper()

	// The node reads its own copy of the process table, since commands rewrite the world's.
	table := proctest.New(record.PlatformDarwin, nodePID)
	for pid, p := range m.w.table.Procs {
		table.Add(pid, *p)
	}

	m.configureNode()

	n, err := node.New(node.Options{
		StateDir: m.w.state, UID: os.Getuid(), User: tester, Machine: m.w.machine, SelfPID: nodePID, SelfStart: startOf(nodePID),
		Version: "test", Config: config.Node{Broker: m.f.srv.URL, CAFile: m.f.caFile}, Heartbeat: 40 * time.Millisecond, Backoff: 10 * time.Millisecond,
		Clock: m.f.clock, WakeGap: time.Hour, Checker: &liveness.Checker{Procs: table, Local: m.w.machine}, Finder: m.w.finder,
		Now: func() time.Time { return *m.now.Load() }, ReplyGrace: m.replyGrace,
		Warn:  func(msg string) { m.w.t.Log("node warning: " + msg) },
		Roots: &rootset.Deriver{CachePath: filepath.Join(m.w.state, handoff.RootsCache)},
	})
	if err != nil {
		m.w.t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	go func() { m.done <- n.Run(ctx) }()

	m.w.t.Cleanup(m.stop)

	waitFor(m.w.t, "the node's presence record", func() bool {
		_, err := handoff.ReadPresence(m.w.state)

		return err == nil
	})
}

// configureNode writes the node config file the CLI looks for, so machine claims record their repo entries.
func (m *wideMachine) configureNode() {
	m.w.t.Helper()

	dir := config.Dir(filepath.Join(m.w.root, "userhome"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.w.t.Fatal(err)
	}

	body := `{"version":"1.0","broker":"` + m.f.srv.URL + `"`
	if m.f.caFile != "" {
		body += `,"ca_file":"` + m.f.caFile + `"`
	}

	body += `}`
	if err := os.WriteFile(filepath.Join(dir, config.NodeFile), []byte(body), 0o600); err != nil {
		m.w.t.Fatal(err)
	}
}

func (m *wideMachine) stop() {
	if m.cancel == nil {
		return
	}

	m.cancel()

	select {
	case err := <-m.done:
		_ = err
	case <-time.After(5 * time.Second):
	}

	m.cancel = nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "protocol.file.allow=always", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = keys.CleanGitEnv(os.Environ())

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

// repoIn makes a repository with one commit that has files under svc/.
func (w *world) repoIn(name string) string {
	w.t.Helper()

	dir := w.dir(name)
	gitIn(w.t, dir, "init")

	for _, f := range []string{"svc/a.txt", "svc/b.txt", "other/c.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			w.t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}

	gitIn(w.t, dir, "add", "-A")
	gitIn(w.t, dir, "commit", "-m", "first")

	return dir
}

// cloneInto clones a repository into this world and installs the hooks.
func (w *world) cloneInto(origin, name string) string {
	w.t.Helper()

	dir := filepath.Join(w.root, name)
	gitIn(w.t, w.root, "clone", origin, dir)

	return dir
}

func (w *world) installHooks(c caller, repo string) {
	w.t.Helper()

	wantStatus(w.t, w.run(c, "setup", "git", "--install", repo), 0)
}

var wideLeaseID = regexp.MustCompile(`lease ([0-9A-HJKMNP-TV-Z]{26})`)

func leaseIDIn(t *testing.T, out string) string {
	t.Helper()

	m := wideLeaseID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no lease ID in %q", out)
	}

	return m[1]
}

func (f *wideFixture) leases() []wire.Lease { return f.b.Leases() }
