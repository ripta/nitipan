package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/state"
)

func TestReleaseRemovesALeaseByID(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", w.dir("repo"))

	res := w.run(c, "release", id)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "released "+id)

	if len(w.leases()) != 0 {
		t.Errorf("records = %v, want none", w.leaseIDs())
	}
}

func TestReleaseWithNoTargetRefusesAndListsTheCallersLeases(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", w.dir("repo"))

	res := w.run(c, "release")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, id)

	if len(w.leases()) != 1 {
		t.Error("a bare release removed a lease")
	}
}

func TestReleaseOfAnotherHoldersLeaseIsNotYours(t *testing.T) {
	w := newWorld(t)

	id := w.acquireOK(w.claude(300, "s2"), "--checkout", w.dir("repo"), "--name", "owner")

	res := w.run(w.claude(200, "s1"), "release", id)
	wantStatus(t, res, exitcode.NotYours)
	wantContains(t, "stderr", res.Stderr, "not yours", `agent name "owner"`)
	wantAbsent(t, "stderr", res.Stderr, "--force")

	if w.leaseByID(id) == nil {
		t.Error("the other holder's lease was removed")
	}
}

func TestReleaseOfAnUnknownIDListsTheCallersLeasesAndNeverSaysAlreadyReleased(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	mine := w.acquireOK(c, "--checkout", w.dir("repo"))

	released := w.acquireOK(c, "--checkout", w.dir("other"))
	wantStatus(t, w.run(c, "release", released), exitcode.OK)

	for _, id := range []string{released, "01JZZZZZZZZZZZZZZZZZZZZZZZ"} {
		res := w.run(c, "release", id)
		wantStatus(t, res, exitcode.NoSuchLease)
		wantContains(t, "stderr", res.Stderr, "no such lease "+id, mine)
		wantAbsent(t, "stderr", res.Stderr, "already released")
	}
}

func TestReleaseOfAMalformedIDIsAUsageError(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "release", "../../etc/passwd")
	wantStatus(t, res, exitcode.Usage)
}

func TestReleaseWithSeveralTargetsRemovesNothingWhenOneFails(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", w.dir("repo"))

	res := w.run(c, "release", id, "01JZZZZZZZZZZZZZZZZZZZZZZZ")
	wantStatus(t, res, exitcode.NoSuchLease)

	if w.leaseByID(id) == nil {
		t.Error("a partly failed release removed a lease")
	}
}

func TestReleaseByDomainMatchesEqualKeysAndNamesCandidatesForAPartialMatch(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", repo, "--scope", "docs")

	res := w.run(c, "release", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, id, "exactly those keys")

	res = w.run(c, "release", "--checkout", w.dir("elsewhere"))
	wantStatus(t, res, exitcode.NoSuchLease)

	res = w.run(c, "release", "--checkout", repo, "--scope", "docs")
	wantStatus(t, res, exitcode.OK)

	if len(w.leases()) != 0 {
		t.Errorf("records = %v, want none", w.leaseIDs())
	}
}

func TestReleaseByDomainWorksForADeletedWorktree(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	worktree := w.dir("worktrees", "feature")
	w.acquireOK(c, "--checkout", worktree)

	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}

	res := w.run(c, "release", "--checkout", worktree)
	wantStatus(t, res, exitcode.OK)

	if len(w.leases()) != 0 {
		t.Errorf("records = %v, want none", w.leaseIDs())
	}
}

func TestReleaseAllRemovesEveryLeaseTheCallerHoldsAndNoOneElses(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", w.dir("repo", "a"))
	w.acquireOK(c, "--checkout", w.dir("repo", "b"))
	theirs := w.acquireOK(w.claude(300, "s2"), "--checkout", w.dir("repo", "c"))

	wantStatus(t, w.run(c, "release", "--all"), exitcode.OK)

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != theirs {
		t.Errorf("records = %v, want only %s", ids, theirs)
	}

	wantStatus(t, w.run(c, "release", "--all"), exitcode.OK)
}

func TestReleaseRejectsAMixOfTargetKinds(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "release", "--all", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Usage)
}

func TestReleaseRemovesTheCallersOwnRecordOfANewerMinor(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.plant(plantSpec{PID: 200, Paths: []string{w.dir("repo")}, Version: "1.5", Handle: "mine"})

	res := w.run(c, "release", id)
	wantStatus(t, res, exitcode.OK)

	if w.leaseByID(id) != nil {
		t.Error("the newer-minor record survived the release")
	}
}

func TestReleaseWithoutAnAdapterRefusesAndNamesPlainBreak(t *testing.T) {
	w := newWorld(t)

	id := w.plant(plantSpec{PID: 777, Paths: []string{w.dir("repo")}})

	res := w.run(w.terminal(900), "release", id)
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "break")
	wantAbsent(t, "stderr", res.Stderr, "--force")

	if w.leaseByID(id) == nil {
		t.Error("an anonymous release removed a lease")
	}
}

func TestReleaseOfABrokenLeaseReportsBroken(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	gone := "01JGGGGGGGGGGGGGGGGGGGGGGG"
	tomb := map[string]any{
		"version": "1.0", "id": "01JTTTTTTTTTTTTTTTTTTTTTTT", "footprint": "none", "clear_when_dead": true,
		"liveness": "process", "machine": map[string]any{"host": "laptop", "id": "machine-1"},
		"holder": map[string]any{
			"agent": "claude", "pid": 200, "start": map[string]any{"platform": "darwin", "sec": 200},
		},
		"lease":     gone,
		"broken_by": map[string]any{"agent": "claude", "pid": 300, "handle": "the-breaker-of-leases-here"},
		"broken_at": "2026-10-20T11:00:00Z",
	}

	data, err := json.Marshal(tomb)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(w.state, state.TombstonesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "01JTTTTTTTTTTTTTTTTTTTTTTT.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	res := w.run(c, "release", gone)
	wantStatus(t, res, exitcode.Broken)
	wantContains(t, "stderr", res.Stderr, "was broken", "the-breaker-of-leases-here")

	if _, err := os.Stat(filepath.Join(dir, "01JTTTTTTTTTTTTTTTTTTTTTTT.json")); !os.IsNotExist(err) {
		t.Errorf("release left the delivered tombstone: %v", err)
	}

	res = w.run(c, "release", gone)
	wantStatus(t, res, exitcode.NoSuchLease)
}
