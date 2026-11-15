package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/record"
)

var handlePattern = regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+-[a-z]+$`)

func TestAcquireClaimsADomainAndPublishesItsRecord(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	res := w.run(c, "acquire", "--checkout", repo, "--name", "alpha", "--description", "docs work")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "acquired", "handle: ", "agent name: alpha")

	recs := w.leases()
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}

	rec := recs[0]

	if rec.Version.String() != "1.0" || !rec.ClearWhenDead || rec.Liveness != record.LivenessProcess {
		t.Errorf("envelope = version %s, clear_when_dead %v, liveness %q", rec.Version, rec.ClearWhenDead, rec.Liveness)
	}

	if rec.Holder.PID != 200 || rec.Holder.SessionID != "s1" || rec.Holder.Agent != "claude" || rec.Holder.Name != "alpha" {
		t.Errorf("holder = %+v", rec.Holder)
	}

	if !handlePattern.MatchString(rec.Holder.Handle) {
		t.Errorf("handle %q is not four words", rec.Holder.Handle)
	}

	if rec.Reach != record.ReachMachine || rec.ActingFor != tester || rec.Description != "docs work" {
		t.Errorf("record = %+v", rec)
	}

	if len(rec.Domain.Keys) != 1 || rec.Domain.Keys[0].Path != repo {
		t.Errorf("keys = %+v, want %s", rec.Domain.Keys, repo)
	}

	info, err := os.Stat(w.state)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Errorf("state directory mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestAcquireDefaultsToTheWorkingDirectory(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"))

	recs := w.leases()
	if len(recs) != 1 || recs[0].Domain.Keys[0].Path != w.root {
		t.Errorf("records = %+v, want one on %s", recs, w.root)
	}
}

func TestAcquireLockHolderRemovesTempFilesOfACrashedWriter(t *testing.T) {
	w := newWorld(t)
	w.dir("repo")

	if err := os.MkdirAll(w.leasesDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(w.leasesDir(), ".tmp-01JAAAAAAAAAAAAAAAAAAAAAAA")
	if err := os.WriteFile(stale, []byte("{partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	w.acquireOK(w.claude(200, "s1"), "--checkout", filepath.Join(w.root, "repo"))

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file survived: %v", err)
	}

	if len(w.leases()) != 1 {
		t.Errorf("%d records after the sweep, want 1", len(w.leases()))
	}
}

type unusableTest struct {
	Name    string
	File    string
	Content string
}

var unusableTests = []unusableTest{
	{Name: "garbage", File: "01JAAAAAAAAAAAAAAAAAAAAAAA.json", Content: "{not json"},
	{Name: "higher major", File: "01JAAAAAAAAAAAAAAAAAAAAAAA.json", Content: `{"version":"2.0","id":"01JAAAAAAAAAAAAAAAAAAAAAAA"}`},
	{Name: "name is not a lease id", File: "notes.json", Content: `{"version":"1.0","id":"notes"}`},
	{
		Name: "id differs from the file name", File: "01JAAAAAAAAAAAAAAAAAAAAAAA.json",
		Content: `{"version":"1.0","id":"01JBBBBBBBBBBBBBBBBBBBBBBB","holder":{"agent":"x","pid":1,"start":{"platform":"darwin"}}}`,
	},
}

func TestAcquireRefusesWhileAnyRecordIsUnusableAndNamesTheFile(t *testing.T) {
	for _, test := range unusableTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			repo := w.dir("repo")
			elsewhere := w.dir("elsewhere")

			w.writeLeaseFile(test.File, []byte(test.Content))

			res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
			wantStatus(t, res, exitcode.Unusable)
			wantContains(t, "stderr", res.Stderr, filepath.Join(w.leasesDir(), test.File), "upgrade nitipan or ask the user")

			// The record might overlap any claim, so a claim far from everything is refused too.
			res = w.run(w.claude(200, "s1"), "acquire", "--checkout", elsewhere)
			wantStatus(t, res, exitcode.Unusable)
		})
	}
}

func TestAcquireReplacesALeaseWhoseHolderIsDead(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	dead := w.plant(plantSpec{PID: 777, Paths: []string{repo}})

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "cleared", dead)

	recs := w.leases()
	if len(recs) != 1 || recs[0].ID == dead {
		t.Errorf("records = %v, want only the new lease", w.leaseIDs())
	}
}

func TestAcquireRefusesALeaseWhoseHolderIsAlive(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	holder := w.claude(200, "s1")
	w.acquireOK(holder, "--checkout", repo, "--name", "alpha", "--description", "owns the docs")
	handle := w.leases()[0].Holder.Handle

	res := w.run(w.claude(300, "s2"), "acquire", "--checkout", repo, "--scope", "docs")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, `handle "`+handle+`"`, `agent name "alpha"`, "claude session s1", "owns the docs")
	wantAbsent(t, "stderr", res.Stderr, "--force", "break")

	if len(w.leases()) != 1 {
		t.Errorf("a refused claim changed the leases: %v", w.leaseIDs())
	}
}

func TestAcquireTreatsAnotherMachinesLeaseAsAliveAndSaysWhy(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	w.plant(plantSpec{PID: 777, Paths: []string{repo}, Machine: record.Machine{Host: "other", ID: "machine-2"}})

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "liveness is unknown, which counts as alive")
}

func TestAcquireRefusesADeadHolderThatOnlyANewerNitipanMayClear(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	id := w.plant(plantSpec{PID: 777, Paths: []string{repo}, Version: "1.4", KeepAfterDeath: true})

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "1.4", "newer nitipan", "Upgrade nitipan")
	wantAbsent(t, "stderr", res.Stderr, "--force")

	if w.leaseByID(id) == nil {
		t.Error("the record was cleared")
	}
}

func TestAcquireIsIdempotentForTheSameKeysAndKeepsIDAndAcquiredAt(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", repo, "--name", "alpha", "--permission-mode", "plan")
	first := w.leaseByID(id)

	res := w.run(c, "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "already held; nothing changed")

	w.now = w.now.Add(time.Hour)

	res = w.run(c, "acquire", "--checkout", repo, "--description", "now with a description")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "advertisement updated")

	recs := w.leases()
	if len(recs) != 1 || recs[0].ID != id {
		t.Fatalf("records = %v, want only %s", w.leaseIDs(), id)
	}

	got := recs[0]

	if !got.AcquiredAt.Equal(first.AcquiredAt) {
		t.Errorf("acquired_at moved from %v to %v", first.AcquiredAt, got.AcquiredAt)
	}

	// The flags left out kept their stored values.
	if got.Holder.Name != "alpha" || got.PermissionMode != "plan" || got.Description != "now with a description" {
		t.Errorf("record = name %q, mode %q, description %q", got.Holder.Name, got.PermissionMode, got.Description)
	}
}

func TestAcquireReportsACoveredClaimWithItsOwnStatusAndChangesNothing(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", repo)

	res := w.run(c, "acquire", "--checkout", repo, "--scope", "docs", "--name", "late")
	wantStatus(t, res, exitcode.AlreadyCovered)
	wantContains(t, "stderr", res.Stderr, "already covered by your leases "+id, "nothing changed", "advertise")

	recs := w.leases()
	if len(recs) != 1 || recs[0].Holder.Name != "" {
		t.Errorf("a covered claim changed the leases: %+v", recs)
	}
}

func TestAcquireKeepsAFlagLeftOutOnANewLease(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", w.dir("repo", "a"), "--name", "alpha", "--permission-mode", "plan", "--description", "a")
	id := w.acquireOK(c, "--checkout", w.dir("repo", "b"))

	got := w.leaseByID(id)
	if got.Holder.Name != "alpha" || got.PermissionMode != "plan" || got.Description != "" {
		t.Errorf("new lease = name %q, mode %q, description %q", got.Holder.Name, got.PermissionMode, got.Description)
	}
}

func TestAcquireWithANameRewritesEveryLeaseTheCallerHolds(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", w.dir("repo", "a"))
	w.acquireOK(c, "--checkout", w.dir("repo", "b"))

	res := w.run(c, "acquire", "--checkout", filepath.Join(w.root, "repo", "a"), "--name", "renamed", "--permission-mode", "plan")
	wantStatus(t, res, exitcode.OK)

	for _, rec := range w.leases() {
		if rec.Holder.Name != "renamed" || rec.PermissionMode != "plan" {
			t.Errorf("lease %s = name %q, mode %q", rec.ID, rec.Holder.Name, rec.PermissionMode)
		}
	}
}

func TestAcquireRejectsAPermissionModeTheAdapterDoesNotList(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"), "--permission-mode", "yolo")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, `"yolo"`, "acceptEdits", "bypassPermissions")

	if len(w.leases()) != 0 {
		t.Error("a refused claim wrote a lease")
	}
}

func TestAcquireRejectsABadScopeBeforeTouchingTheState(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"), "--scope", "../escape")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "..")
}

func TestAcquireTakesNoPositionalArguments(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "acquire", w.dir("repo"))
	wantStatus(t, res, exitcode.Usage)
}
