package cli

import (
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
)

func TestAdvertiseWithNoLeaseRefusesAndSaysToPassNameToAcquire(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "advertise", "--name", "alpha")
	wantStatus(t, res, exitcode.NoSuchLease)
	wantContains(t, "stderr", res.Stderr, "no lease to advertise on; pass --name to acquire")
}

func TestAdvertiseRewritesTheNameAndModeInEveryLeaseButTheDescriptionInOne(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", w.dir("repo", "a"), "--name", "before")
	b := w.acquireOK(c, "--checkout", w.dir("repo", "b"))

	res := w.run(c, "advertise", "--name", "after", "--permission-mode", "acceptEdits")
	wantStatus(t, res, exitcode.OK)

	for _, id := range []string{a, b} {
		got := w.leaseByID(id)
		if got.Holder.Name != "after" || got.PermissionMode != "acceptEdits" {
			t.Errorf("lease %s = name %q, mode %q", id, got.Holder.Name, got.PermissionMode)
		}
	}

	// The description belongs to one lease, so two leases need a choice.
	wantStatus(t, w.run(c, "advertise", "--description", "docs"), exitcode.Usage)

	wantStatus(t, w.run(c, "advertise", "--description", "docs", "--lease", b), exitcode.OK)

	if w.leaseByID(a).Description != "" || w.leaseByID(b).Description != "docs" {
		t.Errorf("descriptions = %q and %q", w.leaseByID(a).Description, w.leaseByID(b).Description)
	}

	// A flag left out keeps its stored value.
	if got := w.leaseByID(b); got.Holder.Name != "after" || got.PermissionMode != "acceptEdits" {
		t.Errorf("a description-only advertise changed the name or mode: %q, %q", got.Holder.Name, got.PermissionMode)
	}
}

func TestAdvertiseNeedsAFlagAndRejectsAnUnlistedMode(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", w.dir("repo"))

	wantStatus(t, w.run(c, "advertise"), exitcode.Usage)

	res := w.run(c, "advertise", "--permission-mode", "nope")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "acceptEdits", "plan")
}

func TestAdvertiseRefusesToRewriteARecordOfANewerMinorAndWritesNothing(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	plain := w.acquireOK(c, "--checkout", w.dir("repo", "a"), "--name", "before")
	newer := w.plant(plantSpec{PID: 200, Paths: []string{w.dir("repo", "b")}, Version: "1.3", Name: "before"})

	res := w.run(c, "advertise", "--name", "after")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "newer minor version", "upgrade nitipan")

	// Every record was checked before the first rename, so the plain one kept its name.
	if w.leaseByID(plain).Holder.Name != "before" || w.leaseByID(newer).Holder.Name != "before" {
		t.Error("a refused advertise left a partial rewrite")
	}
}

func TestTheHandleIsStableAcrossCallsReacquiresAndAFullRelease(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", w.dir("repo", "a"))
	handle := w.leaseByID(a).Holder.Handle

	b := w.acquireOK(c, "--checkout", w.dir("repo", "b"))
	if got := w.leaseByID(b).Holder.Handle; got != handle {
		t.Errorf("second lease handle = %q, want %q", got, handle)
	}

	wantStatus(t, w.run(c, "acquire", "--checkout", w.dir("repo", "a")), exitcode.OK)
	wantStatus(t, w.run(c, "release", "--all"), exitcode.OK)

	again := w.acquireOK(c, "--checkout", w.dir("repo", "c"))
	if got := w.leaseByID(again).Holder.Handle; got != handle {
		t.Errorf("handle after a full release = %q, want %q", got, handle)
	}

	other := w.acquireOK(w.claude(300, "s2"), "--checkout", w.dir("repo", "d"))
	if got := w.leaseByID(other).Holder.Handle; got == handle {
		t.Errorf("a new process got the same handle %q", got)
	}
}

func TestARotatedSessionIDStillMatchesItsOwnLeaseAndIsRewritten(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "before-clear")

	id := w.acquireOK(c, "--checkout", w.dir("repo"))
	handle := w.leaseByID(id).Holder.Handle

	rotated := c.withEnv("CLAUDE_CODE_SESSION_ID", "after-clear")

	res := w.run(rotated, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.OK)

	got := w.leaseByID(id)
	if got.Holder.SessionID != "after-clear" || got.Holder.Handle != handle || len(w.leases()) != 1 {
		t.Errorf("lease = session %q, handle %q; %d records", got.Holder.SessionID, got.Holder.Handle, len(w.leases()))
	}

	third := c.withEnv("CLAUDE_CODE_SESSION_ID", "after-another-clear")
	wantStatus(t, w.run(third, "release", id), exitcode.OK)
}
