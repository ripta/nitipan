package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/wire"
)

type localVolume struct{}

func (localVolume) Local(string) error { return nil }

type harness struct {
	t     *testing.T
	b     *Broker
	store *Store
	clock *monoclock.Fake
	srv   *httptest.Server
	path  string
}

const testTerm = 5 * time.Minute

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()

	path := filepath.Join(t.TempDir(), "broker.json")

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	clock := monoclock.NewFake(time.Hour)
	cfg := Config{Store: store, Term: testTerm, Keepalive: 20 * time.Millisecond, Clock: clock, Version: "test"}

	if mutate != nil {
		mutate(&cfg)
	}

	b := New(cfg)
	srv := httptest.NewServer(b.Handler())

	t.Cleanup(srv.Close)

	return &harness{t: t, b: b, store: store, clock: clock, srv: srv, path: path}
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

// tryRegister registers a node and returns the broker's refusal, if any.
func (h *harness) tryRegister(n nodeID) (*wire.Registered, *wire.Error) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathRegister, n, wire.Register{
		Version: wire.Current, MachineID: n.Machine, UID: n.UID, User: "u" + strconv.Itoa(n.UID), Host: "host-" + n.Machine,
		NodeVersion: "test", HTTPS: false,
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
