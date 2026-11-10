package broker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/space-armada/nitipan/internal/wire"
)

// TestHandleWhoAmIReturnsBoundUser tests that whoami returns the user label when bound (Item 11).
func TestHandleWhoAmIReturnsBoundUser(t *testing.T) {
	h := newHarness(t, nil)

	label := "test-label"
	nk := NodeKey{MachineID: "machine1", UID: 1000}
	key := "test-key-12345"
	hash := keyHash(key)

	h.store.Data().Nodes[nk.String()] = NodeRecord{
		KeyHash: hash,
		Label:   label,
	}

	h.store.Data().Users["user-id"] = UserRecord{
		ID:        "user-id",
		UserLabel: "test-user",
	}

	h.store.Data().Bindings[label] = BindingRecord{
		UserID: "user-id",
	}

	req := wire.WhoAmI{Version: wire.Current}
	data, _ := json.Marshal(req)

	httpReq := httptest.NewRequest("POST", wire.PathWhoAmI, bytes.NewReader(data))
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set(wire.HeaderMachine, "machine1")
	httpReq.Header.Set(wire.HeaderUID, "1000")

	rec := httptest.NewRecorder()
	h.b.handleWhoAmI(rec, httpReq, nk)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp wire.WhoAmIAnswer
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if resp.UserLabel != "test-user" {
		t.Errorf("UserLabel = %q, want %q", resp.UserLabel, "test-user")
	}
}

// TestHandleWhoAmIReturnsEmptyWhenUnbound tests that whoami returns empty when not bound (Item 11).
func TestHandleWhoAmIReturnsEmptyWhenUnbound(t *testing.T) {
	h := newHarness(t, nil)

	label := "test-label"
	nk := NodeKey{MachineID: "machine1", UID: 1000}
	key := "test-key-12345"
	hash := keyHash(key)

	h.store.Data().Nodes[nk.String()] = NodeRecord{
		KeyHash: hash,
		Label:   label,
	}

	req := wire.WhoAmI{Version: wire.Current}
	data, _ := json.Marshal(req)

	httpReq := httptest.NewRequest("POST", wire.PathWhoAmI, bytes.NewReader(data))
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set(wire.HeaderMachine, "machine1")
	httpReq.Header.Set(wire.HeaderUID, "1000")

	rec := httptest.NewRecorder()
	h.b.handleWhoAmI(rec, httpReq, nk)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp wire.WhoAmIAnswer
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if resp.UserLabel != "" {
		t.Errorf("UserLabel = %q, want empty", resp.UserLabel)
	}
}
