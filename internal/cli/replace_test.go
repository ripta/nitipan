package cli

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
)

func TestSelfOverlapSuggestsReplaceWhenItDropsNoKeys(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	docs := w.acquireOK(c, "--checkout", repo, "--scope", "docs")
	src := w.acquireOK(c, "--checkout", repo, "--scope", "src")

	res := w.run(c, "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "--replace "+docs, "--replace "+src)
	wantAbsent(t, "stderr", res.Stderr, "--force")
}

func TestSelfOverlapNamesTheDroppedKeysInsteadOfReplaceWhenKeysWouldBeLost(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	nested := w.dir("repo", "w")
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", repo)

	// The nested worktree is its own checkout, so it is not covered by the lease on the repo. The docs
	// scope is covered, which makes the claim a scope change that would release the whole repo.
	w.finder[nested] = true

	res := w.run(c, "acquire", "--checkout", repo, "--scope", "docs", "--scope", "w")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "would release "+repo, "acquire --checkout "+repo+" --scope w")
	wantAbsent(t, "stderr", res.Stderr, "--replace", "--force")
}

func TestReplaceKeepsTheLeaseIDAndAcquiredAt(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", repo, "--scope", "docs", "--name", "alpha")
	acquired := w.leaseByID(id).AcquiredAt

	w.now = w.now.Add(2 * time.Hour)

	res := w.run(c, "acquire", "--checkout", repo, "--replace", id)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "replaced")

	recs := w.leases()
	if len(recs) != 1 || recs[0].ID != id {
		t.Fatalf("records = %v, want only %s", w.leaseIDs(), id)
	}

	got := recs[0]

	if !got.AcquiredAt.Equal(acquired) {
		t.Errorf("acquired_at moved from %v to %v", acquired, got.AcquiredAt)
	}

	if len(got.Domain.Keys) != 1 || got.Domain.Keys[0].Path != repo || got.Holder.Name != "alpha" {
		t.Errorf("record = keys %+v, name %q", got.Domain.Keys, got.Holder.Name)
	}
}

func TestReplaceRefusesOverlapWithALeaseItDoesNotName(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", repo, "--scope", "a")
	b := w.acquireOK(c, "--checkout", repo, "--scope", "b")

	res := w.run(c, "acquire", "--checkout", repo, "--replace", a)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "does not name", b)

	if got := w.leaseByID(a); len(got.Domain.Keys) != 1 || filepath.Base(got.Domain.Keys[0].Path) != "a" {
		t.Errorf("a refused replace changed %s: %+v", a, got.Domain.Keys)
	}
}

func TestReplaceWithSeveralIDsMergesThem(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", repo, "--scope", "a")
	b := w.acquireOK(c, "--checkout", repo, "--scope", "b")

	res := w.run(c, "acquire", "--checkout", repo, "--replace", a, "--replace", b)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "merged "+b)

	recs := w.leases()
	if len(recs) != 1 || recs[0].ID != a {
		t.Fatalf("records = %v, want only %s", w.leaseIDs(), a)
	}

	if len(recs[0].Absorbs) != 1 || recs[0].Absorbs[0] != b {
		t.Errorf("absorbs = %v, want [%s]", recs[0].Absorbs, b)
	}
}

func TestReplaceNamingAnotherHoldersLeaseOrNoLeaseIsRefused(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	other := w.acquireOK(w.claude(300, "s2"), "--checkout", repo, "--scope", "other")
	c := w.claude(200, "s1")

	res := w.run(c, "acquire", "--checkout", repo, "--scope", "mine", "--replace", other)
	wantStatus(t, res, exitcode.NotYours)

	res = w.run(c, "acquire", "--checkout", repo, "--scope", "mine", "--replace", "01JZZZZZZZZZZZZZZZZZZZZZZZ")
	wantStatus(t, res, exitcode.NoSuchLease)
}

func TestReplaceKilledAfterTheRewriteLeavesLeftoversThatTheNextLockedCallRemoves(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", repo, "--scope", "a")
	b := w.acquireOK(c, "--checkout", repo, "--scope", "b")

	w.hooks = lease.Hooks{AfterRewrite: func() error { return errors.New("killed") }}

	res := w.run(c, "acquire", "--checkout", repo, "--replace", a, "--replace", b)
	wantStatus(t, res, exitcode.Error)

	// The rename already happened, so the lead holds the new keys and names its leftover.
	lead := w.leaseByID(a)
	if len(lead.Domain.Keys) != 1 || lead.Domain.Keys[0].Path != repo || len(lead.Absorbs) != 1 || lead.Absorbs[0] != b {
		t.Fatalf("lead after the crash = keys %+v, absorbs %v", lead.Domain.Keys, lead.Absorbs)
	}

	if w.leaseByID(b) == nil {
		t.Fatal("the merged lease was already gone")
	}

	w.hooks = lease.Hooks{}

	wantStatus(t, w.run(w.terminal(900), "list"), exitcode.OK)

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != a {
		t.Errorf("records after the next locked call = %v, want only %s", ids, a)
	}
}

func TestReplaceOfOneLeaseNeverLeavesTwoRecords(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	a := w.acquireOK(c, "--checkout", repo, "--scope", "a")

	w.hooks = lease.Hooks{AfterRewrite: func() error { return errors.New("killed") }}

	wantStatus(t, w.run(c, "acquire", "--checkout", repo, "--replace", a), exitcode.Error)

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != a {
		t.Errorf("records = %v, want exactly %s", ids, a)
	}
}

func TestANestedWorktreesLeaseSurvivesWhenItBecomesCoveredByAnotherOfTheCallersLeases(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	nested := w.dir("repo", "w")
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", repo)

	w.finder[nested] = true
	inner := w.acquireOK(c, "--checkout", nested)

	// The worktree goes away while both leases are held. The inner lease is now covered by the outer
	// one, and it must survive because nothing absorbed it.
	delete(w.finder, nested)

	wantStatus(t, w.run(w.terminal(900), "list"), exitcode.OK)
	wantStatus(t, w.run(c, "advertise", "--name", "still here"), exitcode.OK)

	if w.leaseByID(inner) == nil || len(w.leases()) != 2 {
		t.Fatalf("records = %v, want both leases", w.leaseIDs())
	}

	wantStatus(t, w.run(c, "acquire", "--checkout", nested), exitcode.OK)
	wantStatus(t, w.run(c, "acquire", "--checkout", nested, "--scope", "sub"), exitcode.AlreadyCovered)

	if len(w.leases()) != 2 {
		t.Errorf("a later claim removed a lease: %v", w.leaseIDs())
	}
}
