package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/wire"
)

type localVolume struct{}

func (localVolume) Local(string) error { return nil }

// wallClock is a settable wall clock. The broker reads it from its handler goroutines.
type wallClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *wallClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *wallClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = t
}

var wallStart = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type harness struct {
	t     *testing.T
	b     *Broker
	store *Store
	clock *monoclock.Fake
	wall  *wallClock
	srv   *httptest.Server
	path  string
}

const testTerm = 5 * time.Minute

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()

	path := filepath.Join(t.TempDir(), "broker.json")
	wall := &wallClock{t: wallStart}

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}, Now: wall.Now})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	clock := monoclock.NewFake(time.Hour)
	cfg := Config{Store: store, Term: testTerm, Keepalive: 20 * time.Millisecond, Clock: clock, Now: wall.Now, Version: "test"}

	if mutate != nil {
		mutate(&cfg)
	}

	b := New(cfg)
	srv := httptest.NewServer(b.Handler())

	t.Cleanup(srv.Close)

	return &harness{t: t, b: b, store: store, clock: clock, wall: wall, srv: srv, path: path}
}

type nodeID struct {
	Machine string
	UID     int
	Key     string
}

func (n nodeID) key() NodeKey { return NodeKey{MachineID: n.Machine, UID: n.UID} }

func (h *harness) do(method, path string, n nodeID, body any) *http.Response {
	h.t.Helper()

	var rd io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}

		rd = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}

	if n.Key != "" {
		req.Header.Set("Authorization", "Bearer "+n.Key)
	}

	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(n.UID))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}

	h.t.Cleanup(func() { resp.Body.Close() })

	return resp
}

// register registers a node that must succeed.
func (h *harness) register(n nodeID) *wire.Registered {
	h.t.Helper()

	got, e := h.tryRegister(n)
	if e != nil {
		h.t.Fatalf("register %+v: %v", n, e)
	}

	return got
}

// tryRegister registers a node that reached the broker over HTTPS, and returns the broker's refusal, if
// any.
func (h *harness) tryRegister(n nodeID) (*wire.Registered, *wire.Error) {
	h.t.Helper()

	return h.tryRegisterOver(n, true)
}

// registerOver registers a node that reports whether it reached the broker over HTTPS. It must succeed.
func (h *harness) registerOver(n nodeID, https bool) *wire.Registered {
	h.t.Helper()

	got, e := h.tryRegisterOver(n, https)
	if e != nil {
		h.t.Fatalf("register %+v: %v", n, e)
	}

	return got
}

func (h *harness) tryRegisterOver(n nodeID, https bool) (*wire.Registered, *wire.Error) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathRegister, n, wire.Register{
		Version: wire.Current, MachineID: n.Machine, UID: n.UID, User: "u" + strconv.Itoa(n.UID), Host: "host-" + n.Machine,
		NodeVersion: "test", HTTPS: https,
	})

	if resp.StatusCode == http.StatusOK {
		var out wire.Registered
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}

		return &out, nil
	}

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		h.t.Fatalf("status %d with a body that is no error: %v", resp.StatusCode, err)
	}

	return nil, &e
}

func (h *harness) snapshot(n nodeID, leases ...wire.MirroredLease) int {
	h.t.Helper()

	return h.do(http.MethodPost, wire.PathSnapshot, n, wire.Snapshot{Version: wire.Current, Leases: leases}).StatusCode
}

func rec(id string) wire.MirroredLease {
	return wire.MirroredLease{ID: id, Record: json.RawMessage(`{"id":"` + id + `"}`)}
}

func (h *harness) view(k NodeKey) NodeView {
	h.t.Helper()

	for _, v := range h.b.Nodes() {
		if v.Key == k {
			return v
		}
	}

	h.t.Fatalf("no view for node %s", k)

	return NodeView{}
}

func handleOf(identity, machine string) string {
	return handle.Derive(identity, machine)
}

// restart replaces the broker with a new one over the store file. A non-nil restore first overwrites the
// file with older contents, as a restore from backup would.
func (h *harness) restart(restore []byte) {
	h.t.Helper()

	h.srv.Close()

	if err := h.store.Close(); err != nil {
		h.t.Fatal(err)
	}

	if restore != nil {
		if err := os.WriteFile(h.path, restore, 0o600); err != nil {
			h.t.Fatal(err)
		}
	}

	store, err := OpenStore(h.path, StoreOptions{Volume: localVolume{}, Now: h.wall.Now})
	if err != nil {
		h.t.Fatal(err)
	}

	h.t.Cleanup(func() { _ = store.Close() })

	h.store = store
	h.b = New(Config{Store: store, Term: testTerm, Keepalive: 20 * time.Millisecond, Clock: h.clock, Now: h.wall.Now, Version: "test"})
	h.srv = httptest.NewServer(h.b.Handler())

	h.t.Cleanup(h.srv.Close)
}

// admin sends one request to the admin socket's handler, as a broker-host command does.
func (h *harness) admin(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()

	var rd io.Reader = http.NoBody

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}

		rd = bytes.NewReader(data)
	}

	w := httptest.NewRecorder()
	h.b.AdminHandler().ServeHTTP(w, httptest.NewRequest(method, path, rd))

	return w
}

// pair sends the hash of a pairing code from node n, as nitipan node pair does. It returns the status and
// the label the broker says the code binds.
func (h *harness) pair(n nodeID, code string) (status int, label string) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathPair, n, map[string]any{"version": wire.Current, "pairing_hash": HashPairingCode(code)})

	var ans struct {
		Label string `json:"label"`
	}

	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
			h.t.Fatal(err)
		}
	}

	return resp.StatusCode, ans.Label
}

// data returns the store's current state. The broker swaps whole copies in under its lock, so a copy
// read here is never written again.
func (h *harness) data() *Data {
	h.b.mu.Lock()
	defer h.b.mu.Unlock()

	return h.store.Data()
}

// createIssuer is the issuer tests admit users under when the issuer plays no part in the test.
const createIssuer = "https://create.test"

// setupPairingCode is the code bindNode pairs with. Tests that are about the code itself send their own.
const setupPairingCode = "setup-pairing-code-words"

// bindNode binds the label of the registered node n to the user userID, as the user's bind on the sign-in
// page does: n sends a pairing code through the pair route, and the signed-in user submits it. It is setup
// for tests that need a bound label, so it calls the page's bind directly rather than driving the page. It
// returns the label.
func (h *harness) bindNode(t *testing.T, n nodeID, userID string) string {
	t.Helper()

	status, label := h.pair(n, setupPairingCode)
	if status != http.StatusOK {
		t.Fatalf("pairing %s: status %d", n.Machine, status)
	}

	u, ok := h.data().Users[userID]
	if !ok {
		t.Fatalf("binding %s: no user %q", label, userID)
	}

	h.signIn(t, u)

	if _, err := h.b.BindPaired(testDelegationIssuer, u.ID, label, setupPairingCode, h.wall.Now()); err != nil {
		t.Fatalf("binding %s to %s: %v", label, u.UserLabel, err)
	}

	return label
}
