package cli

import (
	"strings"
	"testing"
)

func wantStatus(t *testing.T, res result, want int) {
	t.Helper()

	if res.Status != want {
		t.Fatalf("status = %d, want %d\nstdout: %s\nstderr: %s", res.Status, want, res.Stdout, res.Stderr)
	}
}

func wantContains(t *testing.T, what, got string, subs ...string) {
	t.Helper()

	for _, sub := range subs {
		if !strings.Contains(got, sub) {
			t.Errorf("%s %q does not contain %q", what, got, sub)
		}
	}
}

func wantAbsent(t *testing.T, what, got string, subs ...string) {
	t.Helper()

	for _, sub := range subs {
		if strings.Contains(got, sub) {
			t.Errorf("%s %q contains %q", what, got, sub)
		}
	}
}

// leaseIDs returns the lease IDs of the records on disk.
func (w *world) leaseIDs() []string {
	w.t.Helper()

	var out []string
	for _, r := range w.leases() {
		out = append(out, r.ID)
	}

	return out
}

// acquireOK acquires and returns the new record's ID. It fails the test on any other status.
func (w *world) acquireOK(c caller, args ...string) string {
	w.t.Helper()

	before := map[string]bool{}
	for _, id := range w.leaseIDs() {
		before[id] = true
	}

	res := w.run(c, append([]string{"acquire"}, args...)...)
	wantStatus(w.t, res, 0)

	for _, id := range w.leaseIDs() {
		if !before[id] {
			return id
		}
	}

	w.t.Fatalf("acquire %v created no lease\n%s", args, res.Stdout)

	return ""
}
