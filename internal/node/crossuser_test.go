package node

import (
	"os"
	"testing"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/record"
)

// TestNodeRefusesPlainHTTPWhenCrossUserEnabled tests that a node refuses plain HTTP once
// cross-user features are enabled (Item 8).
func TestNodeRefusesPlainHTTPWhenCrossUserEnabled(t *testing.T) {
	dir := t.TempDir()

	if err := RecordCrossUserEnabled(dir); err != nil {
		t.Fatalf("RecordCrossUserEnabled failed: %v", err)
	}

	opts := Options{
		StateDir: dir,
		Config: config.Node{
			Broker: "http://192.168.1.1:8080",
		},
		Machine: record.Machine{
			ID:   "test-machine",
			Host: "test-host",
		},
		UID:     1000,
		User:    "testuser",
		Version: "test",
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !contains(err.Error(), "refusing plain HTTP") {
		t.Errorf("expected error about refusing plain HTTP, got: %v", err)
	}

	if !contains(err.Error(), "cross-user features were enabled") {
		t.Errorf("expected error mentioning cross-user features, got: %v", err)
	}
}

// TestNodeAllowsPlainHTTPWhenCrossUserNotEnabled tests that a node allows plain HTTP when
// cross-user features are not enabled (Item 8).
func TestNodeAllowsPlainHTTPWhenCrossUserNotEnabled(t *testing.T) {
	dir := t.TempDir()

	opts := Options{
		StateDir: dir,
		Config: config.Node{
			Broker: "http://127.0.0.1:8080",
		},
		Machine: record.Machine{
			ID:   "test-machine",
			Host: "test-host",
		},
		UID:     1000,
		User:    "testuser",
		Version: "test",
	}

	_, err := New(opts)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
}

// TestNodeAllowsHTTPSWhenCrossUserEnabled tests that a node allows HTTPS even when cross-user
// features are enabled (Item 8).
func TestNodeAllowsHTTPSWhenCrossUserEnabled(t *testing.T) {
	dir := t.TempDir()

	if err := RecordCrossUserEnabled(dir); err != nil {
		t.Fatalf("RecordCrossUserEnabled failed: %v", err)
	}

	opts := Options{
		StateDir: dir,
		Config: config.Node{
			Broker: "https://broker.example.com:8080",
		},
		Machine: record.Machine{
			ID:   "test-machine",
			Host: "test-host",
		},
		UID:     1000,
		User:    "testuser",
		Version: "test",
	}

	_, err := New(opts)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
}

// TestNodeRefusesPlainHTTPEvenWhenLaterRegisteredOmitsFlag tests that once a node records
// cross-user enabled, it stays refused even if a later registration omits the flag (Item 8).
func TestNodeRefusesPlainHTTPEvenWhenLaterRegisteredOmitsFlag(t *testing.T) {
	dir := t.TempDir()

	if err := RecordCrossUserEnabled(dir); err != nil {
		t.Fatalf("RecordCrossUserEnabled failed: %v", err)
	}

	if !IsCrossUserEnabled(dir) {
		t.Error("flag not set after recording")
	}

	opts := Options{
		StateDir: dir,
		Config: config.Node{
			Broker: "http://192.168.1.1:8080",
		},
		Machine: record.Machine{
			ID:   "test-machine",
			Host: "test-host",
		},
		UID:     1000,
		User:    "testuser",
		Version: "test",
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !contains(err.Error(), "cross-user features were enabled") {
		t.Errorf("expected error about cross-user features, got: %v", err)
	}
}

// TestIsCrossUserEnabledFailsClosedOnUnreadableFile tests that IsCrossUserEnabled treats
// any error other than fs.ErrNotExist as "on", failing closed. If a session makes the flag
// file unreadable, the node must not treat cross-user as off and send its key over plain HTTP.
func TestIsCrossUserEnabledFailsClosedOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()

	if err := RecordCrossUserEnabled(dir); err != nil {
		t.Fatal(err)
	}

	path := dir + "/" + crossUserFile
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if !IsCrossUserEnabled(dir) {
		t.Error("IsCrossUserEnabled returned false for unreadable file, but must fail closed and return true")
	}
}

// TestIsCrossUserEnabledFailsClosedOnUnsearchableDir tests that IsCrossUserEnabled treats
// any error other than fs.ErrNotExist as "on", failing closed. If a session makes the state
// directory unsearchable, the node must not treat cross-user as off.
func TestIsCrossUserEnabledFailsClosedOnUnsearchableDir(t *testing.T) {
	dir := t.TempDir()
	subdir := dir + "/state"

	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := RecordCrossUserEnabled(subdir); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(subdir, 0o000); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(subdir, 0o700) })

	if !IsCrossUserEnabled(subdir) {
		t.Error("IsCrossUserEnabled returned false for unsearchable directory, but must fail closed and return true")
	}
}

func contains(s, substr string) bool {
	return s != "" && substr != "" &&
		len(s) >= len(substr) && (s == substr || hasSubstring(s, substr))
}

func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
