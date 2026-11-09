package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

// shortDir makes a directory whose socket paths fit the 104-byte limit of a macOS unix socket.
func shortDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "nt")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

func adminClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func TestAdminSocketListsApprovesAndForgetsForTheBrokersOwnUser(t *testing.T) {
	h := newHarness(t, nil)

	label := h.registerMail(alice)
	path := filepath.Join(shortDir(t), "admin.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- h.b.ServeAdmin(ctx, path) }()

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Log(err)
		}
	})

	cl := adminClient(path)

	waitUntil(t, "the admin socket", func() bool {
		_, err := os.Stat(path)

		return err == nil
	})

	resp, err := cl.Get("http://admin" + AdminPathPending)
	if err != nil {
		t.Fatal(err)
	}

	var pending AdminPending
	if err := json.NewDecoder(resp.Body).Decode(&pending); err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if len(pending.Labels) != 1 || pending.Labels[0].Label != label || len(pending.Labels[0].Keys) != 1 ||
		pending.Labels[0].Keys[0].Fingerprint != wire.Fingerprint(wire.KeyHash(alice.Key)) {
		t.Fatalf("pending = %+v", pending)
	}

	body, _ := json.Marshal(adminApprove{Label: label, Fingerprint: pending.Labels[0].Keys[0].Fingerprint})

	resp, err = cl.Post("http://admin"+AdminPathApprove, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || h.data().Pins[label].PinnedHash == "" {
		t.Fatalf("approve status %d, pin %+v", resp.StatusCode, h.data().Pins[label])
	}

	body, _ = json.Marshal(adminForget{Label: label})

	resp, err = cl.Post("http://admin"+AdminPathForget, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent || !h.data().Pins[label].Forgotten {
		t.Fatalf("forget status %d", resp.StatusCode)
	}

	body, _ = json.Marshal(adminForget{Label: "no-such-label-at-all"})

	resp, err = cl.Post("http://admin"+AdminPathForget, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("forget of an unknown label = %d", resp.StatusCode)
	}
}

func TestAdminSocketRefusesAPeerThatIsAnotherOSUser(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	warned := make(chan error, 1)
	pl := peerListener{Listener: ln, uid: os.Getuid() + 1, warn: func(err error) { warned <- err }}

	go func() { _, _ = pl.Accept() }()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	select {
	case err := <-warned:
		if err == nil {
			t.Error("no warning for the refused peer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never judged the peer")
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if _, err := io.ReadAll(conn); err != nil {
		t.Errorf("the refused connection was not closed: %v", err)
	}
}

func TestAdminSocketNeedsAPrivateDirectory(t *testing.T) {
	h := newHarness(t, nil)
	dir := shortDir(t)

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := h.b.ServeAdmin(context.Background(), filepath.Join(dir, "a.sock")); err == nil {
		t.Error("ServeAdmin accepted a directory others can enter")
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestAdminPathChecksOwnerFileTypeAndLength(t *testing.T) {
	dir := shortDir(t)
	uid := os.Getuid()

	if err := checkAdminPath(filepath.Join(dir, "admin.sock"), uid+1); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Errorf("a directory owned by another user: %v", err)
	}

	regular := filepath.Join(dir, "admin.sock")
	if err := os.WriteFile(regular, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkAdminPath(regular, uid); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("a regular file at the path: %v", err)
	}

	if got, err := os.ReadFile(regular); err != nil || string(got) != "precious" {
		t.Errorf("the file at the path was touched: %q, %v", got, err)
	}

	if err := os.Remove(regular); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(dir, "admin.sock")

	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}

	// A closed listener leaves its socket file behind, as a crashed broker does.
	uln, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("%T is no unix listener", ln)
	}

	uln.SetUnlinkOnClose(false)
	_ = uln.Close()

	if err := checkAdminPath(stale, uid); err != nil {
		t.Errorf("a stale socket: %v", err)
	}

	if _, err := os.Lstat(stale); err == nil {
		t.Error("the stale socket was not removed")
	}

	long := filepath.Join(dir, strings.Repeat("d", 120), "admin.sock")
	if err := checkAdminPath(long, uid); err == nil || !strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a path over the limit: %v", err)
	}
}
