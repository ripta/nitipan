package node

import (
	"os"
	"testing"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/record"
)

// TestNodeRefusesPlainHTTPWhenCrossUserEnabled proves a node refuses plain HTTP once it has recorded that
// cross-user features are on. It catches a node that sends its key in the clear to a broker serving
// several users.
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

// TestPlainHTTPRefusalOffersNoRekeyRoute proves the refusal does not send the operator to 'node
// rekey' or 'broker forget'. Neither clears the record, so the route never ends the refusal. It
// cannot prove that no other command clears the record.
func TestPlainHTTPRefusalOffersNoRekeyRoute(t *testing.T) {
	dir := t.TempDir()

	if err := RecordCrossUserEnabled(dir); err != nil {
		t.Fatalf("RecordCrossUserEnabled failed: %v", err)
	}

	if err := Rekey(dir); err != nil {
		t.Fatalf("Rekey failed: %v", err)
	}

	err := refusePlain(dir)
	if err == nil {
		t.Fatal("expected the refusal to survive a rekey, got nil")
	}

	for _, offer := range []string{"node rekey", "broker forget"} {
		if contains(err.Error(), offer) {
			t.Errorf("refusal offers %q, which does not clear the record: %v", offer, err)
		}
	}

	if !contains(err.Error(), "Use https") {
		t.Errorf("refusal should say to use https, got: %v", err)
	}
}

// TestNodeAllowsPlainHTTPWhenCrossUserNotEnabled proves a node still talks plain HTTP to a broker that
// never turned cross-user features on. It catches a refusal that breaks single-user brokers on loopback.
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

// TestNodeAllowsHTTPSWhenCrossUserEnabled proves a node keeps talking HTTPS once cross-user features are
// on. It catches a refusal that matches the flag alone rather than the scheme.
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

// TestNodeRefusesPlainHTTPEvenWhenLaterRegisteredOmitsFlag proves a node that once recorded cross-user
// features on keeps refusing plain HTTP when a later registration answer omits the flag. It catches a
// record that a downgraded or spoofed answer clears.
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
