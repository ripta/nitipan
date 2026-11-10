package broker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestAuthedRefusesUnboundLabelWhenCrossUserEnabled tests that authed refuses unbound labels
// when CrossUserEnabled is true (Item 5).
func TestAuthedRefusesUnboundLabelWhenCrossUserEnabled(t *testing.T) {
	h := newHarness(t, nil)

	h.store.Data().Users["owner-id"] = UserRecord{ID: "owner-id", UserLabel: "owner-user"}
	h.store.Data().OwnerID = "owner-id"
	h.store.Data().CrossUserEnabled = true

	label := "test-label"
	nk := NodeKey{MachineID: "machine1", UID: 1000}
	key := "test-key-12345"
	hash := keyHash(key)

	h.store.Data().Nodes[nk.String()] = NodeRecord{
		KeyHash: hash,
		Label:   label,
	}

	h.store.Data().Labels[label] = LabelRecord{
		MachineID:  "machine1",
		UID:        1000,
		Derivation: 1,
	}

	handler := h.b.authed(func(w http.ResponseWriter, r *http.Request, key NodeKey) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(wire.HeaderMachine, "machine1")
	req.Header.Set(wire.HeaderUID, "1000")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}

	var e wire.Error
	if err := json.NewDecoder(rec.Body).Decode(&e); err != nil {
		t.Fatalf("decoding error: %v", err)
	}

	if e.Code != wire.CodeForbidden {
		t.Errorf("expected code %q, got %q", wire.CodeForbidden, e.Code)
	}

	if e.Message == "" {
		t.Error("expected error message about approval step")
	}
}

// TestAuthedAllowsBoundLabelWhenCrossUserEnabled tests that authed allows bound labels when
// CrossUserEnabled is true (Item 5).
func TestAuthedAllowsBoundLabelWhenCrossUserEnabled(t *testing.T) {
	h := newHarness(t, nil)

	h.store.Data().Users["owner-id"] = UserRecord{ID: "owner-id", UserLabel: "owner-user"}
	h.store.Data().OwnerID = "owner-id"
	h.store.Data().CrossUserEnabled = true

	label := "test-label"
	nk := NodeKey{MachineID: "machine1", UID: 1000}
	key := "test-key-12345"
	hash := keyHash(key)

	h.store.Data().Nodes[nk.String()] = NodeRecord{
		KeyHash: hash,
		Label:   label,
	}

	h.store.Data().Labels[label] = LabelRecord{
		MachineID:  "machine1",
		UID:        1000,
		Derivation: 1,
	}

	h.store.Data().Bindings[label] = BindingRecord{
		UserID:            "owner-id",
		DerivationVersion: 1,
	}

	handler := h.b.authed(func(w http.ResponseWriter, r *http.Request, key NodeKey) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(wire.HeaderMachine, "machine1")
	req.Header.Set(wire.HeaderUID, "1000")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

// TestAuthedAllowsUnboundLabelWhenCrossUserDisabled tests that authed allows unbound labels
// when CrossUserEnabled is false (Item 5).
func TestAuthedAllowsUnboundLabelWhenCrossUserDisabled(t *testing.T) {
	h := newHarness(t, nil)

	h.store.Data().Users["owner-id"] = UserRecord{ID: "owner-id", UserLabel: "owner-user"}
	h.store.Data().OwnerID = "owner-id"
	h.store.Data().CrossUserEnabled = false

	label := "test-label"
	nk := NodeKey{MachineID: "machine1", UID: 1000}
	key := "test-key-12345"
	hash := keyHash(key)

	h.store.Data().Nodes[nk.String()] = NodeRecord{
		KeyHash: hash,
		Label:   label,
	}

	h.store.Data().Labels[label] = LabelRecord{
		MachineID:  "machine1",
		UID:        1000,
		Derivation: 1,
	}

	handler := h.b.authed(func(w http.ResponseWriter, r *http.Request, key NodeKey) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(wire.HeaderMachine, "machine1")
	req.Header.Set(wire.HeaderUID, "1000")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

// TestSetCrossUserEnabledRefusals tests SetCrossUserEnabled refusal conditions (Item 10).
func TestSetCrossUserEnabledRefusals(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*harness)
		listen  string
		tls     bool
		wantErr string
	}{
		{
			name: "no OIDC configured",
			setup: func(h *harness) {
				h.store.Data().OwnerID = "owner"
			},
			wantErr: "no identity provider configured",
		},
		{
			name: "no OwnerID",
			setup: func(h *harness) {
			},
			wantErr: "upgrade binding has not run",
		},
		{
			name: "label reported plain HTTP",
			setup: func(h *harness) {
				h.store.Data().OwnerID = "owner"
				h.store.Data().Bindings["label1"] = BindingRecord{UserID: "owner"}
				h.store.Data().SchemeReports["label1"] = "http"
			},
			wantErr: "reported plain HTTP",
		},
		{
			name: "label never reported scheme",
			setup: func(h *harness) {
				h.store.Data().OwnerID = "owner"
				h.store.Data().Bindings["label1"] = BindingRecord{UserID: "owner"}
			},
			wantErr: "never reported scheme",
		},
		{
			name: "private listen without TLS",
			setup: func(h *harness) {
				h.store.Data().OwnerID = "owner"
			},
			listen:  "192.168.1.1:8080",
			tls:     false,
			wantErr: "requires TLS or a loopback address",
		},
		{
			name: "public listen without TLS",
			setup: func(h *harness) {
				h.store.Data().OwnerID = "owner"
			},
			listen:  "0.0.0.0:8080",
			tls:     false,
			wantErr: "requires TLS or a loopback address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutate := func(cfg *Config) {
				if tt.name != "no OIDC configured" {
					cfg.OIDCConfig = &oidc.Config{
						Issuer:       "https://test.example",
						ClientID:     "test",
						RedirectURL:  "https://broker.example/callback",
						ClientSecret: "secret",
					}
				}
				if tt.listen != "" {
					cfg.Listen = tt.listen
					cfg.TLS = tt.tls
				}
			}

			h := newHarness(t, mutate)
			tt.setup(h)

			err := h.b.SetCrossUserEnabled(true)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestSetCrossUserEnabledSuccess tests successful enable with all conditions met (Item 10).
func TestSetCrossUserEnabledSuccess(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		tls    bool
	}{
		{"loopback without TLS", "127.0.0.1:8080", false},
		{"public with TLS", "0.0.0.0:8443", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutate := func(cfg *Config) {
				cfg.OIDCConfig = &oidc.Config{
					Issuer:       "https://test.example",
					ClientID:     "test",
					RedirectURL:  "https://broker.example/callback",
					ClientSecret: "secret",
				}
				cfg.Listen = tt.listen
				cfg.TLS = tt.tls
			}

			h := newHarness(t, mutate)
			h.store.Data().OwnerID = "owner"
			h.store.Data().Bindings["label1"] = BindingRecord{UserID: "owner"}
			h.store.Data().SchemeReports["label1"] = "https"

			if err := h.b.SetCrossUserEnabled(true); err != nil {
				t.Fatalf("SetCrossUserEnabled failed: %v", err)
			}

			if !h.store.Data().CrossUserEnabled {
				t.Error("CrossUserEnabled not set")
			}
		})
	}
}

// TestRegisteredCarriesCrossUserFlag tests that Registered carries the CrossUser flag (Item 8).
func TestRegisteredCarriesCrossUserFlag(t *testing.T) {
	tests := []struct {
		name             string
		crossUserEnabled bool
		wantCrossUser    bool
	}{
		{"cross-user enabled", true, true},
		{"cross-user disabled", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.store.Data().CrossUserEnabled = tt.crossUserEnabled

			req := wire.Register{
				Version:   wire.Current,
				MachineID: "machine1",
				UID:       1000,
				User:      "testuser",
				Host:      "testhost",
				HTTPS:     true,
			}

			data, _ := json.Marshal(req)
			httpReq := httptest.NewRequest("POST", wire.PathRegister, bytes.NewReader(data))
			httpReq.Header.Set("Authorization", "Bearer test-key")
			httpReq.Header.Set(wire.HeaderMachine, "machine1")
			httpReq.Header.Set(wire.HeaderUID, "1000")

			rec := httptest.NewRecorder()
			h.b.handleRegister(rec, httpReq)

			if rec.Code != http.StatusOK {
				t.Fatalf("registration failed: %d", rec.Code)
			}

			var resp wire.Registered
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}

			if resp.CrossUser != tt.wantCrossUser {
				t.Errorf("CrossUser = %v, want %v", resp.CrossUser, tt.wantCrossUser)
			}
		})
	}
}
