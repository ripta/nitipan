package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/tlstest"
	"github.com/space-armada/nitipan/internal/wire"
)

func TestNodeRegistersWithTheBrokerAndGetsItsLabel(t *testing.T) {
	r := newRig(t)
	run := r.start(r.options())

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	if want := broker.DeriveLabel(testMachine.ID, os.Getuid()); run.n.Label() != want {
		t.Errorf("label = %q, want %q", run.n.Label(), want)
	}

	eventually(t, "a live stream", func() bool {
		v, ok := r.view()

		return ok && v.StreamLive && v.User == "tester" && v.Host == "laptop"
	})
}

func TestNodeSendsItsInstanceKeyOnEveryRequest(t *testing.T) {
	r := newRig(t)
	r.start(r.options())

	eventually(t, "a snapshot", func() bool { v, ok := r.view(); return ok && v.HasSnapshot })

	data, err := os.ReadFile(filepath.Join(r.state, keyName))
	if err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(filepath.Join(r.state, keyName))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}

	row := r.store.Data().Nodes[broker.NodeKey{MachineID: testMachine.ID, UID: os.Getuid()}.String()]
	if row.KeyHash == "" || row.KeyHash == strings.TrimSpace(string(data)) {
		t.Errorf("broker row = %+v: the store must hold a hash, never the key", row)
	}
}

func TestNodeKeepsItsKeyAcrossRestarts(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}

	second, err := LoadKey(dir)
	if err != nil || first != second || len(first) < 32 {
		t.Errorf("keys = %q, %q, %v", first, second, err)
	}

	if err := os.Chmod(filepath.Join(dir, keyName), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadKey(dir); err == nil {
		t.Error("a world-readable key was accepted")
	}
}

func TestNodeMirrorsAV1RecordByteForByteOnConnect(t *testing.T) {
	r := newRig(t)

	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}

	id, data := writeRecord(t, r.state, "/work/repo")

	r.start(r.options())

	v := r.waitLeases(1)
	var want, got any

	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(v.Leases[0].Record, &got); err != nil {
		t.Fatal(err)
	}

	if v.Leases[0].ID != id || !reflect.DeepEqual(got, want) || v.Leases[0].Unknown != nil {
		t.Errorf("mirror = %+v, want the record unchanged", v.Leases[0])
	}

	after, err := os.ReadFile(filepath.Join(r.state, state.LeasesDir, id+".json"))
	if err != nil || string(after) != string(data) {
		t.Error("mirroring changed the record on disk")
	}
}

type unusableCase struct {
	Name    string
	File    string
	Content string
	Reason  string
}

var unusableCases = []unusableCase{
	{"higher major", "01HZZZZZZZZZZZZZZZZZZZZZZZ.json", `{"version":"2.0","id":"01HZZZZZZZZZZZZZZZZZZZZZZZ"}`, "higher major version"},
	{"not json", "01HYYYYYYYYYYYYYYYYYYYYYYY.json", `not json`, "envelope does not decode"},
	{"file name is no lease ID", "weird.json", `{"version":"1.0","id":"weird"}`, "file name"},
	{"ID differs from the file name", "01HXXXXXXXXXXXXXXXXXXXXXXX.json", `{"version":"1.0","id":"01HWWWWWWWWWWWWWWWWWWWWWWW","footprint":{}}`, ""},
}

func TestUnusableRecordsMirrorAsAnUnknownHolderWithAnAllFootprint(t *testing.T) {
	for _, tc := range unusableCases {
		t.Run(tc.Name, func(t *testing.T) {
			got, _ := mirrorOf(state.File{ID: strings.TrimSuffix(tc.File, ".json"), BadName: !state.ValidID(strings.TrimSuffix(tc.File, ".json")), Data: []byte(tc.Content)})

			if got.Unknown == nil || got.Unknown.Footprint != wire.FootprintAll || got.Record != nil {
				t.Fatalf("mirror = %+v, want an unknown holder with an all footprint", got)
			}

			if !strings.Contains(got.Unknown.Reason, tc.Reason) {
				t.Errorf("reason = %q, want %q", got.Unknown.Reason, tc.Reason)
			}
		})
	}
}

func TestNodeMirrorsAnUnusableRecordNextToAUsableOne(t *testing.T) {
	r := newRig(t)
	leases := filepath.Join(r.state, state.LeasesDir)

	if err := os.MkdirAll(leases, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(leases, "01HZZZZZZZZZZZZZZZZZZZZZZZ.json"), []byte(`{"version":"2.0","id":"01HZZZZZZZZZZZZZZZZZZZZZZZ"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	writeRecord(t, r.state, "/work/repo")
	r.start(r.options())

	v := r.waitLeases(2)

	unknown := 0

	for _, l := range v.Leases {
		if l.Unknown != nil {
			unknown++
		}
	}

	if unknown != 1 {
		t.Errorf("unknown holders = %d, want 1: %+v", unknown, v.Leases)
	}
}

func TestNodeSendsANewSnapshotWhenLeasesChange(t *testing.T) {
	r := newRig(t)
	r.start(r.options())

	r.waitLeases(0)

	writeRecord(t, r.state, "/work/one")
	r.waitLeases(1)

	id, _ := writeRecord(t, r.state, "/work/two")
	r.waitLeases(2)

	if err := os.Remove(filepath.Join(r.state, state.LeasesDir, id+".json")); err != nil {
		t.Fatal(err)
	}

	r.waitLeases(1)
}

func TestKqueueWatchReportsChangesInAnExistingAndALaterDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := filepath.Join(t.TempDir(), "leases")

	ch, err := SystemWatch(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	expect := func(what string) {
		t.Helper()

		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("no change reported for %s", what)
		}
	}

	expect("the directory appearing")

	// Drain the burst that the appearance may leave before testing a write.
	time.Sleep(2 * pollEvery)

	for len(ch) > 0 {
		<-ch
	}

	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	expect("a file published")

	for len(ch) > 0 {
		<-ch
	}

	if err := os.Rename(filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")); err != nil {
		t.Fatal(err)
	}

	expect("a rename")
}

func TestNodeSnapshotsOnEveryHeartbeatEvenWhenTheWatcherIsSilent(t *testing.T) {
	r := newRig(t)

	silent := func(ctx context.Context, _ string) (<-chan struct{}, error) {
		ch := make(chan struct{})

		go func() { <-ctx.Done() }()

		return ch, nil
	}

	opts := r.options()
	opts.Watch = silent

	r.start(opts)
	r.waitLeases(0)

	writeRecord(t, r.state, "/work/quiet")

	// Only the heartbeat's snapshot can carry this lease to the broker.
	r.waitLeases(1)
}

func TestNodeResyncsASnapshotAfterTheBrokerClosesItsStream(t *testing.T) {
	r := newRig(t)

	var registrations atomic.Int32

	opts := r.options()
	opts.Warn = func(string) { registrations.Add(1) }

	run := r.start(opts)

	eventually(t, "a live stream", func() bool { v, ok := r.view(); return ok && v.StreamLive })

	key := broker.NodeKey{MachineID: testMachine.ID, UID: os.Getuid()}
	if !r.b.CloseStream(key) {
		t.Fatal("no stream to close")
	}

	writeRecord(t, r.state, "/work/after-close")

	eventually(t, "the node to reconnect and resync", func() bool {
		v, ok := r.view()

		return ok && v.StreamLive && len(v.Leases) == 1
	})

	if run.n.Label() == "" {
		t.Error("label lost on reconnect")
	}
}

func TestEventsReachTheNodeAndAreAckedOnlyWhenHandled(t *testing.T) {
	r := newRig(t)

	var (
		mu    sync.Mutex
		calls []string
		fail  atomic.Bool
	)

	fail.Store(true)

	opts := r.options()
	opts.OnEvent = func(ev wire.Event) error {
		mu.Lock()
		calls = append(calls, ev.ID)
		mu.Unlock()

		if fail.Load() {
			return errors.New("not yet")
		}

		return nil
	}
	opts.Warn = func(string) {}

	r.start(opts)

	key := broker.NodeKey{MachineID: testMachine.ID, UID: os.Getuid()}

	eventually(t, "a live stream", func() bool { v, ok := r.view(); return ok && v.StreamLive })

	id, err := r.b.Enqueue(key, "note", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "the first delivery", func() bool { mu.Lock(); defer mu.Unlock(); return len(calls) == 1 })

	time.Sleep(100 * time.Millisecond)

	if got := r.b.Unacked(key); len(got) != 1 || got[0].ID != id {
		t.Fatalf("a failed handler must leave the event queued, queue = %+v", got)
	}

	fail.Store(false)
	r.b.CloseStream(key)

	eventually(t, "the replay to be handled and acked", func() bool { return len(r.b.Unacked(key)) == 0 })
}

func TestAnEventResentAfterALostAckRunsOnce(t *testing.T) {
	n := &Node{seen: map[string]struct{}{}}
	runs := 0

	n.opts.OnEvent = func(wire.Event) error { runs++; return nil }

	// handle acks over the network, which this node does not have. A failed ack still counts the
	// event as handled, so the resend must not run it again.
	base, _ := url.Parse("http://127.0.0.1:1")
	n.cl = &client{base: base, http: &http.Client{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = n.handle(ctx, wire.Event{ID: "e1"})
	_ = n.handle(ctx, wire.Event{ID: "e1"})

	if runs != 1 {
		t.Errorf("handler ran %d times, want 1", runs)
	}
}

func TestSecondNodeForTheSameMachineAndUserFailsOnItsLock(t *testing.T) {
	r := newRig(t)
	first := r.start(r.options())

	eventually(t, "registration", func() bool { return first.n.Label() != "" })

	second, err := New(r.options())
	if err != nil {
		t.Fatal(err)
	}

	err = second.Run(context.Background())
	if !errors.Is(err, ErrNodeRunning) {
		t.Fatalf("second node = %v, want ErrNodeRunning", err)
	}

	if !strings.Contains(err.Error(), filepath.Join(r.state, lockName)) {
		t.Errorf("error %q does not name the lock file", err)
	}
}

func TestNodePublishesPresenceWhileRunningAndRemovesItOnExit(t *testing.T) {
	r := newRig(t)
	run := r.start(r.options())

	eventually(t, "the presence record", func() bool { _, err := handoff.ReadPresence(r.state); return err == nil })

	p, err := handoff.ReadPresence(r.state)
	if err != nil || p.PID != os.Getpid() || p.NodeVersion != "test" || p.RequestVersion != handoff.RequestVersion {
		t.Fatalf("presence = %+v, %v", p, err)
	}

	if err := p.Supports(KindPing, 1); err != nil {
		t.Errorf("Supports(ping) = %v", err)
	}

	if err := run.stop(t); err != nil {
		t.Fatal(err)
	}

	if _, err := handoff.ReadPresence(r.state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("presence after exit: %v", err)
	}
}

func TestNodeRefusesToStartWithoutAMachineID(t *testing.T) {
	r := newRig(t)

	opts := r.options()
	opts.Machine.ID = ""

	_, err := New(opts)
	if !errors.Is(err, ErrNoMachineID) || !strings.Contains(err.Error(), "hardware UUID could not be read") {
		t.Errorf("New = %v", err)
	}
}

func TestNodeExitsWhenItsExecutableChanges(t *testing.T) {
	r := newRig(t)
	exe := filepath.Join(t.TempDir(), "nitipan")

	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := r.options()
	opts.ExecPath = exe

	run := r.start(opts)

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	// An upgrade installs a new file under the same name.
	next := exe + ".new"
	if err := os.WriteFile(next, []byte("version two"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(next, exe); err != nil {
		t.Fatal(err)
	}

	if err := run.wait(t, 5*time.Second); !errors.Is(err, ErrExecutableChanged) {
		t.Errorf("Run = %v, want ErrExecutableChanged", err)
	}
}

func TestNodeExitsWhenItsExecutableIsRemoved(t *testing.T) {
	r := newRig(t)
	exe := filepath.Join(t.TempDir(), "nitipan")

	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := r.options()
	opts.ExecPath = exe

	run := r.start(opts)

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	if err := run.wait(t, 5*time.Second); !errors.Is(err, ErrExecutableChanged) {
		t.Errorf("Run = %v, want ErrExecutableChanged", err)
	}
}

type addrCase struct {
	Name     string
	Broker   string
	Err      error
	WantWarn bool
}

var addrCases = []addrCase{
	{"plain HTTP to loopback warns", "http://127.0.0.1:8443", nil, true},
	{"plain HTTP to a private address warns", "http://10.1.2.3:8443", nil, true},
	{"plain HTTP to a public address is refused", "http://203.0.113.5:8443", ErrPlainPublic, false},
	{"plain HTTP to a name warns and is judged at dial time", "http://broker.lan:8443", nil, true},
	{"HTTPS to a public name is fine", "https://broker.example", nil, false},
	{"no scheme", "broker:8443", ErrBadBroker, false},
	{"wrong scheme", "ftp://broker", ErrBadBroker, false},
}

func TestNodeAppliesTheAddressRules(t *testing.T) {
	for _, tc := range addrCases {
		t.Run(tc.Name, func(t *testing.T) {
			var warned []string

			_, err := New(Options{
				Machine: testMachine,
				Config:  config.Node{Broker: tc.Broker},
				Warn:    func(m string) { warned = append(warned, m) },
			})
			if !errors.Is(err, tc.Err) {
				t.Fatalf("New = %v, want %v", err, tc.Err)
			}

			if (len(warned) > 0) != tc.WantWarn {
				t.Errorf("warnings = %v, want a warning: %v", warned, tc.WantWarn)
			}
		})
	}
}

func TestProxyComesFromTheConfigAndNeverTheEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy:1")
	t.Setenv("HTTPS_PROXY", "http://env-proxy:1")

	req, _ := newRequestFor("http://203.0.113.5/x")

	none, err := proxyFunc(config.Node{})
	if err != nil {
		t.Fatal(err)
	}

	if u, err := none(req); u != nil || err != nil {
		t.Errorf("no proxy configured, got %v, %v", u, err)
	}

	set, err := proxyFunc(config.Node{Proxy: "http://corp-proxy:3128"})
	if err != nil {
		t.Fatal(err)
	}

	if u, _ := set(req); u == nil || u.Host != "corp-proxy:3128" {
		t.Errorf("proxy = %v, want corp-proxy:3128", u)
	}

	if _, err := proxyFunc(config.Node{Proxy: "not a url"}); err == nil {
		t.Error("a bad proxy URL was accepted")
	}
}

func TestNodeVerifiesTheBrokersCertificateAgainstItsCAFile(t *testing.T) {
	r := newRig(t)
	files := tlstest.SelfSigned(t, t.TempDir())

	tlsSrv := newTLSBroker(t, r, files)

	trusting := r.options()
	trusting.Config = config.Node{Broker: tlsSrv, CAFile: files.Cert}

	var warned []string

	trusting.Warn = func(m string) { warned = append(warned, m) }

	run := r.start(trusting)

	eventually(t, "registration over TLS", func() bool { return run.n.Label() != "" })

	if len(warned) != 0 {
		t.Errorf("an HTTPS node warned: %v", warned)
	}

	// The same broker with only the system roots is untrusted, so a node cannot register.
	other := newRig(t)
	system := other.options()
	system.Config = config.Node{Broker: newTLSBroker(t, other, files)}
	system.Warn = func(string) {}

	n, err := New(system)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := n.register(context.Background()); err == nil {
		t.Error("a self-signed certificate was accepted against the system roots")
	}
}

func TestNodeRegistersAsHTTPSOnlyWhenItUsedTLS(t *testing.T) {
	plain, err := New(Options{Machine: testMachine, Config: config.Node{Broker: "http://127.0.0.1:1"}})
	if err != nil || plain.https {
		t.Errorf("plain node https = %v, %v", plain != nil && plain.https, err)
	}

	secure, err := New(Options{Machine: testMachine, Config: config.Node{Broker: "https://127.0.0.1:1"}})
	if err != nil || !secure.https {
		t.Errorf("tls node https = %v, %v", secure != nil && secure.https, err)
	}
}

func TestCABundleThatHoldsNoCertificateIsRefused(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bad, []byte("nothing"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(Options{Machine: testMachine, Config: config.Node{Broker: "https://127.0.0.1:1", CAFile: bad}})
	if err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Errorf("New = %v", err)
	}
}

func waitReply(t *testing.T, r *rig, id string) handoff.Reply {
	t.Helper()

	var rep handoff.Reply

	eventually(t, "the reply to "+id, func() bool {
		got, err := handoff.ReadReply(r.state, id)
		rep = got

		return err == nil
	})

	return rep
}

func TestNodeForwardsAHandedOffRequestToTheBroker(t *testing.T) {
	r := newRig(t)
	run := r.start(r.options())

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	req := handoff.Request{Kind: KindPing, ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: testMachine}, CreatedAt: time.Now()}
	if err := handoff.Submit(r.state, req); err != nil {
		t.Fatal(err)
	}

	rep := waitReply(t, r, req.ID)
	if rep.Error != nil {
		t.Fatalf("reply = %+v", rep.Error)
	}

	var pong wire.Pong
	if err := json.Unmarshal(rep.Body, &pong); err != nil || pong.Label != run.n.Label() {
		t.Errorf("pong = %+v, %v", pong, err)
	}

	if ids, _ := handoff.Pending(r.state); len(ids) != 0 {
		t.Errorf("still pending: %v", ids)
	}

	claimed, _ := state.ListNames(r.state, handoff.ClaimedDir)
	if len(claimed) != 0 {
		t.Errorf("claimed after answering: %v", claimed)
	}
}

func TestNodeRefusesToForwardARequestOlderThanADay(t *testing.T) {
	r := newRig(t)

	var forwarded atomic.Int32

	opts := r.options()
	opts.Forward = map[string]Forwarder{"grant": func(context.Context, handoff.Request) (json.RawMessage, error) {
		forwarded.Add(1)

		return nil, nil
	}}

	run := r.start(opts)

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	old := handoff.Request{Kind: "grant", ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: testMachine}, CreatedAt: time.Now().Add(-handoff.MaxAge - time.Minute)}
	fresh := handoff.Request{Kind: "grant", ID: state.NewID(), Requester: old.Requester, CreatedAt: time.Now().Add(-handoff.MaxAge + time.Hour)}

	for _, req := range []handoff.Request{old, fresh} {
		if err := handoff.Submit(r.state, req); err != nil {
			t.Fatal(err)
		}
	}

	rep := waitReply(t, r, old.ID)
	if rep.Error == nil || rep.Error.Code != handoff.CodeExpired || !strings.Contains(rep.Error.Message, "request expired") {
		t.Errorf("old request reply = %+v", rep.Error)
	}

	if freshRep := waitReply(t, r, fresh.ID); freshRep.Error != nil {
		t.Errorf("a request under a day old was refused: %+v", freshRep.Error)
	}

	if forwarded.Load() != 1 {
		t.Errorf("forwarded %d requests, want only the fresh one", forwarded.Load())
	}
}

func TestNodeAnswersNodeTooOldForAnUnknownKindOrANewerVersion(t *testing.T) {
	r := newRig(t)
	run := r.start(r.options())

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	unknown := handoff.Request{Kind: "renew-from-the-future", ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: testMachine}, CreatedAt: time.Now()}
	if err := handoff.Submit(r.state, unknown); err != nil {
		t.Fatal(err)
	}

	rep := waitReply(t, r, unknown.ID)
	if rep.Error == nil || rep.Error.Code != handoff.CodeTooOld || !strings.Contains(rep.Error.Message, "node too old") {
		t.Errorf("unknown kind reply = %+v", rep.Error)
	}

	// A request written by a newer CLI carries a version this node does not read.
	n := &Node{forwrd: map[string]Forwarder{KindPing: nil}, opts: Options{Now: time.Now}}
	got := n.answer(context.Background(), handoff.Request{Version: handoff.RequestVersion + 1, ID: "x", Kind: KindPing, CreatedAt: time.Now()})

	if got.Error == nil || got.Error.Code != handoff.CodeTooOld {
		t.Errorf("newer version reply = %+v", got.Error)
	}
}

type dialCase struct {
	Name    string
	Address string
	Refuse  bool
}

var dialCases = []dialCase{
	{"public IPv4", "203.0.113.5:80", true},
	{"public IPv6", "[2001:db8::1]:80", true},
	{"wildcard", "0.0.0.0:80", true},
	{"loopback", "127.0.0.1:80", false},
	{"private IPv4", "192.168.1.20:80", false},
	{"private IPv6", "[fd00::5]:80", false},
}

func TestPlainHTTPDialRefusesAPublicAddressAfterResolution(t *testing.T) {
	for _, tc := range dialCases {
		t.Run(tc.Name, func(t *testing.T) {
			err := refusePublicDial("tcp", tc.Address, nil)
			if (err != nil) != tc.Refuse || (err != nil && !errors.Is(err, ErrPlainPublic)) {
				t.Errorf("refusePublicDial(%q) = %v, want refuse %v", tc.Address, err, tc.Refuse)
			}
		})
	}
}

func TestPlainTransportRefusesToConnectToAPublicIPAndHTTPSTransportDoesNotGuard(t *testing.T) {
	tr, err := newTransport(config.Node{Broker: "http://broker.lan"}, true)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := newRequestFor("http://203.0.113.5:9/")

	_, err = (&http.Client{Transport: tr, Timeout: 2 * time.Second}).Do(req)
	if !errors.Is(err, ErrPlainPublic) {
		t.Errorf("plain transport to a public IP = %v, want ErrPlainPublic", err)
	}

	secure, err := newTransport(config.Node{Broker: "https://broker.example"}, false)
	if err != nil || secure.DialContext != nil {
		t.Errorf("an HTTPS transport got the plain-HTTP guard: %v", err)
	}
}

func TestPlainHTTPToANameThatResolvesToLoopbackWorks(t *testing.T) {
	r := newRig(t)

	opts := r.options()
	opts.Config.Broker = strings.Replace(r.srv.URL, "127.0.0.1", "localhost", 1)
	opts.Warn = func(string) {}

	run := r.start(opts)

	eventually(t, "registration through a name", func() bool { return run.n.Label() != "" })
}

func TestAStalledBrokerCallEndsTheSessionSoTheNodeReconnects(t *testing.T) {
	old := callTimeout
	callTimeout = 300 * time.Millisecond

	defer func() { callTimeout = old }()

	r := newRig(t)

	var (
		registers atomic.Int32
		stalled   atomic.Bool
	)

	stop := make(chan struct{})
	defer close(stop)

	hook := func(_ http.ResponseWriter, req *http.Request) bool {
		switch req.URL.Path {
		case wire.PathRegister:
			registers.Add(1)
		case wire.PathSnapshot:
			if stalled.CompareAndSwap(false, true) {
				select {
				case <-stop:
				case <-req.Context().Done():
				}

				return true
			}
		}

		return false
	}

	r.intercept.Store(&hook)

	opts := r.options()
	opts.Warn = func(string) {}

	r.start(opts)

	eventually(t, "the node to register again after a stalled call", func() bool { return registers.Load() >= 2 })
}

func TestASessionEndsOnlyAfterItsStreamGoroutineHasExited(t *testing.T) {
	r := newRig(t)

	var (
		active, peak atomic.Int32
		entered      = make(chan struct{}, 8)
		release      = make(chan struct{})
		failSnap     atomic.Bool
	)

	hook := func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == wire.PathSnapshot && failSnap.Load() {
			http.Error(w, "broken", http.StatusInternalServerError)

			return true
		}

		return false
	}

	r.intercept.Store(&hook)

	opts := r.options()
	opts.Warn = func(string) {}
	opts.OnEvent = func(wire.Event) error {
		now := active.Add(1)

		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}

		entered <- struct{}{}
		<-release
		active.Add(-1)

		return errors.New("keep it queued")
	}

	r.start(opts)

	key := broker.NodeKey{MachineID: testMachine.ID, UID: os.Getuid()}

	eventually(t, "a live stream", func() bool { v, ok := r.view(); return ok && v.StreamLive })

	if _, err := r.b.Enqueue(key, "note", 1); err != nil {
		t.Fatal(err)
	}

	<-entered

	// The next heartbeat's snapshot fails, which ends the session while the handler is still running.
	failSnap.Store(true)

	time.Sleep(600 * time.Millisecond)

	failSnap.Store(false)

	if got := peak.Load(); got != 1 {
		t.Errorf("%d event handlers ran at once, want the old stream to be gone first", got)
	}

	close(release)
}
