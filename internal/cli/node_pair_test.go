package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/node"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestNodePairCommand tests that `nitipan node pair` reaches the broker route under the node key
// and stores the code hash (Item 9).
func TestNodePairCommand(t *testing.T) {
	dir := t.TempDir()

	key, err := node.LoadKey(dir)
	if err != nil {
		t.Fatalf("LoadKey failed: %v", err)
	}

	var receivedHash string
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wire.PathPair {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		receivedAuth = r.Header.Get("Authorization")

		var req struct {
			PairingHash string `json:"pairing_hash"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		receivedHash = req.PairingHash
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "nitipan")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := config.Node{
		Version: "1.0",
		Broker:  srv.URL,
	}

	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "node.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	a := &app{
		deps: Deps{
			UserHome: func() (string, error) {
				return home, nil
			},
			StateDir: func() (string, error) {
				return dir, nil
			},
			Machine: func() (record.Machine, error) {
				return record.Machine{ID: "test-machine", Host: "test-host"}, nil
			},
			Now: time.Now,
		},
	}

	cmd := a.nodePairCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if receivedAuth == "" {
		t.Error("no Authorization header received")
	}

	expectedAuth := "Bearer " + key
	if receivedAuth != expectedAuth {
		t.Errorf("Authorization = %q, want %q", receivedAuth, expectedAuth)
	}

	if receivedHash == "" {
		t.Error("no pairing hash received")
	}

	if len(receivedHash) != 64 {
		t.Errorf("hash length = %d, want 64", len(receivedHash))
	}

	output := out.String()
	if !containsString(output, "Fingerprint:") {
		t.Error("output missing fingerprint")
	}

	if !containsString(output, "Pairing code:") {
		t.Error("output missing pairing code")
	}
}

func containsString(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}
