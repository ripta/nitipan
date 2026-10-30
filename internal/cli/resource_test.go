package cli

import (
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

var otherMachine = record.Machine{Host: "workstation", ID: "machine-2"}

func TestAcquireResourcePublishesAResourceRecord(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	res := w.run(c, "acquire", "--resource", "cpu:heavy", "--description", "full build")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "acquired", "domain: resource cpu:heavy")

	recs := w.leases()
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}

	rec := recs[0]

	if rec.Version.String() != "1.1" || rec.Domain.Kind != record.KindResource || rec.Domain.Name != "cpu:heavy" {
		t.Errorf("record = version %s, domain %+v", rec.Version, rec.Domain)
	}

	if !rec.Footprint.SameName("cpu:heavy") || rec.Reach != record.ReachMachine || rec.Domain.Slots != 1 {
		t.Errorf("record = footprint %+v, reach %q, slots %d", rec.Footprint, rec.Reach, rec.Domain.Slots)
	}

	if len(rec.Domain.Keys) != 0 || rec.Domain.Checkout != "" {
		t.Errorf("a resource record must carry no path body: %+v", rec.Domain)
	}
}

func TestAcquirePathStampsTheFirstMinor(t *testing.T) {
	w := newWorld(t)

	id := w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo"))

	if got := w.leaseByID(id).Version.String(); got != "1.0" {
		t.Errorf("a path record is version %s, want 1.0", got)
	}
}

func TestSecondClaimOnAResourceIsRefusedWithTheHolderNamed(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy", "--name", "builder")

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "cpu:heavy", "builder", "pid 200")

	if len(w.leases()) != 1 {
		t.Errorf("%d records, want the one that was there", len(w.leases()))
	}
}

func TestDifferentResourceNamesNeverOverlap(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")
	w.acquireOK(w.claude(300, "s2"), "--resource", "cpu:light")
	w.acquireOK(w.claude(400, "s3"), "--resource", "gpu:main")
}

func TestResourceLeaseNeverOverlapsAPathLease(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")
	w.acquireOK(w.claude(300, "s2"), "--checkout", repo)
	w.acquireOK(w.claude(400, "s3"), "--resource", "repo-lock:main")

	res := w.run(w.claude(500, "s4"), "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantAbsent(t, "stderr", res.Stderr, "cpu:heavy")
}

func TestReacquiringTheSameResourceRefreshesTheLease(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--resource", "cpu:heavy")

	res := w.run(c, "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "already held; nothing changed")

	res = w.run(c, "acquire", "--resource", "cpu:heavy", "--description", "now with tests")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "advertisement updated")

	recs := w.leases()
	if len(recs) != 1 || recs[0].ID != id || recs[0].Description != "now with tests" {
		t.Errorf("records = %v, want only %s refreshed", w.leaseIDs(), id)
	}

	if recs[0].Version.String() != "1.1" {
		t.Errorf("a refresh changed the version to %s", recs[0].Version)
	}
}

func TestClaimClearsADeadHoldersResourceLease(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")
	w.kill(200)

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "cleared")

	if recs := w.leases(); len(recs) != 1 || recs[0].Holder.PID != 300 {
		t.Errorf("records = %+v, want only the new holder's", recs)
	}
}

type resourceNameTest struct {
	Name   string
	Given  string
	Status int
	Want   []string
	Absent []string
}

var resourceNameTests = []resourceNameTest{
	{Name: "no colon", Given: "cpu", Status: exitcode.Usage, Want: []string{"exactly one colon"}},
	{Name: "two colons", Given: "a:b:c", Status: exitcode.Usage, Want: []string{"exactly one colon"}},
	{Name: "empty namespace", Given: ":heavy", Status: exitcode.Usage, Want: []string{"non-empty"}},
	{Name: "empty name", Given: "cpu:", Status: exitcode.Usage, Want: []string{"non-empty"}},
	{Name: "empty", Given: "", Status: exitcode.Usage, Want: []string{"--resource needs a name"}},
	{Name: "a space", Given: "cpu:heavy load", Status: exitcode.Usage, Want: []string{"lowercase letters"}},
	{Name: "a slash", Given: "db:pg/main", Status: exitcode.Usage, Want: []string{"lowercase letters"}},
	{Name: "too long", Given: "cpu:" + strings.Repeat("a", 61), Status: exitcode.Usage, Want: []string{"limit is 64"}},
	{
		Name: "uppercase suggests the lowercase spelling", Given: "CPU:Heavy", Status: exitcode.Usage,
		Want: []string{`Did you mean "cpu:heavy"?`},
	},
	{Name: "the repo namespace", Given: "repo:main", Status: exitcode.Usage, Want: []string{"reserved"}},
	{
		Name: "the repo namespace in capitals", Given: "REPO:main", Status: exitcode.Usage, Want: []string{"reserved"},
		Absent: []string{"Did you mean"},
	},
	{Name: "every allowed character", Given: "a.b_c-1:x.y_z-9", Status: exitcode.OK},
}

func TestAcquireChecksTheResourceNameGrammar(t *testing.T) {
	for _, test := range resourceNameTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			res := w.run(w.claude(200, "s1"), "acquire", "--resource", test.Given)
			wantStatus(t, res, test.Status)
			wantContains(t, "stderr", res.Stderr, test.Want...)
			wantAbsent(t, "stderr", res.Stderr, test.Absent...)

			if test.Status != exitcode.OK && len(w.leases()) != 0 {
				t.Errorf("a refused name published %d records", len(w.leases()))
			}
		})
	}
}

func TestRefusedUppercaseNameIsNeverFolded(t *testing.T) {
	w := newWorld(t)

	w.run(w.claude(200, "s1"), "acquire", "--resource", "CPU:heavy")

	if len(w.leases()) != 0 {
		t.Error("an uppercase name was folded and published")
	}
}

type resourceFlagTest struct {
	Name string
	Args []string
	Want []string
}

var resourceFlagTests = []resourceFlagTest{
	{
		Name: "a second resource", Args: []string{"--resource", "cpu:heavy", "--resource", "gpu:main"},
		Want: []string{"one --resource per call"},
	},
	{
		Name: "a checkout", Args: []string{"--resource", "cpu:heavy", "--checkout", "/tmp"},
		Want: []string{"cannot be combined with --checkout or --scope"},
	},
	{
		Name: "a scope", Args: []string{"--resource", "cpu:heavy", "--scope", "src"},
		Want: []string{"cannot be combined with --checkout or --scope"},
	},
	{
		Name: "slots on a path claim", Args: []string{"--slots", "1"},
		Want: []string{"--slots applies to a resource claim"},
	},
	{
		Name: "slots below one", Args: []string{"--resource", "cpu:heavy", "--slots", "0"},
		Want: []string{"at least 1"},
	},
}

func TestAcquireRefusesBadResourceFlagCombinations(t *testing.T) {
	for _, test := range resourceFlagTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			res := w.run(w.claude(200, "s1"), append([]string{"acquire"}, test.Args...)...)
			wantStatus(t, res, exitcode.Usage)
			wantContains(t, "stderr", res.Stderr, test.Want...)

			if len(w.leases()) != 0 {
				t.Errorf("a refused call published %d records", len(w.leases()))
			}
		})
	}
}

func TestSlotsMustMatchTheCountThatApplies(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	res := w.run(c, "acquire", "--resource", "gpu:main", "--slots", "2")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "--slots 2", "count 1")

	if len(w.leases()) != 0 {
		t.Fatal("a mismatched --slots published a record")
	}

	res = w.run(c, "acquire", "--resource", "gpu:main", "--slots", "1")
	wantStatus(t, res, exitcode.OK)

	res = w.run(c, "acquire", "--resource", "gpu:main", "--slots", "2")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "count 1")
}

type replaceResourceTest struct {
	Name string

	// Setup plants the leases and returns the --replace ID and the acquire arguments.
	Setup func(w *world, c caller) (id string, args []string)
}

var replaceResourceTests = []replaceResourceTest{
	{
		Name: "a resource claim naming a path lease",
		Setup: func(w *world, c caller) (string, []string) {
			id := w.acquireOK(c, "--checkout", w.dir("repo"))

			return id, []string{"--resource", "cpu:heavy"}
		},
	},
	{
		Name: "a path claim naming a resource lease",
		Setup: func(w *world, c caller) (string, []string) {
			id := w.acquireOK(c, "--resource", "cpu:heavy")

			return id, []string{"--checkout", w.dir("repo")}
		},
	},
	{
		Name: "a resource claim naming another resource lease",
		Setup: func(w *world, c caller) (string, []string) {
			id := w.acquireOK(c, "--resource", "cpu:heavy")

			return id, []string{"--resource", "cpu:light"}
		},
	},
}

func TestReplaceIsRefusedWheneverAResourceIsInvolved(t *testing.T) {
	for _, test := range replaceResourceTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			c := w.claude(200, "s1")

			id, args := test.Setup(w, c)
			before := w.leases()

			res := w.run(c, append([]string{"acquire", "--replace", id}, args...)...)
			wantStatus(t, res, exitcode.Refused)
			wantContains(t, "stderr", res.Stderr, "--replace")

			after := w.leases()
			if len(after) != len(before) || after[0].ID != before[0].ID {
				t.Errorf("records changed from %v to %v", before, after)
			}
		})
	}
}

func TestReleaseResourceMatchesALeaseByEqualName(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	cpu := w.acquireOK(c, "--resource", "cpu:heavy")
	gpu := w.acquireOK(c, "--resource", "gpu:main")

	res := w.run(c, "release", "--resource", "cpu:light")
	wantStatus(t, res, exitcode.NoSuchLease)
	wantContains(t, "stderr", res.Stderr, "cpu:light")

	res = w.run(c, "release", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "released "+cpu)

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != gpu {
		t.Errorf("records = %v, want only %s", ids, gpu)
	}
}

func TestReleaseResourceRefusesAnotherHoldersLease(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")

	res := w.run(w.claude(300, "s2"), "release", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.NoSuchLease)

	if len(w.leases()) != 1 {
		t.Error("a release by another holder removed the lease")
	}
}

func TestReleaseRefusesAResourceMixedWithOtherTargets(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	w.acquireOK(c, "--resource", "cpu:heavy")

	for _, args := range [][]string{
		{"--resource", "cpu:heavy", "--all"},
		{"--resource", "cpu:heavy", "--checkout", w.root},
		{"--resource", "cpu:heavy", "--resource", "gpu:main"},
	} {
		res := w.run(c, append([]string{"release"}, args...)...)
		wantStatus(t, res, exitcode.Usage)
	}

	if len(w.leases()) != 1 {
		t.Error("a refused release removed the lease")
	}
}

func TestListResourceMatchesLeasesByEqualName(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	cpu := w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")
	w.acquireOK(w.claude(400, "s3"), "--checkout", repo)

	leases := leasesOf(t, w.listJSON(w.terminal(900), "--resource", "cpu:heavy"))
	if len(leases) != 1 || leases[0]["id"] != cpu {
		t.Errorf("leases = %v, want only %s", leases, cpu)
	}

	if all := leasesOf(t, w.listJSON(w.terminal(900))); len(all) != 3 {
		t.Errorf("unfiltered list has %d leases, want 3", len(all))
	}

	byPath := leasesOf(t, w.listJSON(w.terminal(900), "--checkout", repo))
	if len(byPath) != 1 || byPath[0]["kind"] != "path" {
		t.Errorf("a path filter returned %v, want only the path lease", byPath)
	}

	res := w.run(w.terminal(900), "list", "--resource", "cpu:heavy", "--checkout", repo)
	wantStatus(t, res, exitcode.Usage)

	res = w.run(w.terminal(900), "list", "--resource", "CPU:heavy")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, `"cpu:heavy"`)
}

func TestListShowsResourceLeasesBesidePathLeases(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy", "--description", "full build")
	w.acquireOK(w.claude(300, "s2"), "--checkout", w.dir("repo"))

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "domain: resource cpu:heavy", "domain: "+w.dir("repo"), "description: full build")
	wantAbsent(t, "stdout", res.Stdout, "remote")

	doc := w.listJSON(w.terminal(900))
	if doc["schema_version"] != "1.5" {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}

	for _, lease := range leasesOf(t, doc) {
		if lease["kind"] != "resource" {
			continue
		}

		domain, _ := lease["domain"].(map[string]any)
		if domain["name"] != "cpu:heavy" || lease["version"] != "1.1" {
			t.Errorf("lease = %v", lease)
		}

		if _, ok := lease["remote"]; ok {
			t.Errorf("a local lease carries the remote flag: %v", lease)
		}

		if lease["effective_slots"] != float64(1) {
			t.Errorf("lease = %v, want an effective count of 1", lease)
		}
	}
}

func TestRemoteResourceRecordNeitherBlocksNorCounts(t *testing.T) {
	w := newWorld(t)
	w.table.Add(77, proctest.Process{Start: startOf(77)})

	remote := w.plant(plantSpec{PID: 77, Resource: "cpu:heavy", Machine: otherMachine, Name: "far"})

	id := w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")
	if id == remote {
		t.Fatal("the claim reused the remote record")
	}

	if w.leaseByID(remote) == nil {
		t.Error("a claim removed another machine's record")
	}

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "resource cpu:heavy (remote, on machine workstation)")

	remoteCount := 0

	for _, lease := range leasesOf(t, w.listJSON(w.terminal(900))) {
		if lease["remote"] == true {
			remoteCount++

			if lease["id"] != remote {
				t.Errorf("the remote flag is on %v, want %s", lease["id"], remote)
			}
		}
	}

	if remoteCount != 1 {
		t.Errorf("%d remote leases, want 1", remoteCount)
	}
}

func TestRemoteResourceRecordIsNotTheCallersOwn(t *testing.T) {
	w := newWorld(t)

	remote := w.plant(plantSpec{PID: 200, Resource: "cpu:heavy", Machine: otherMachine, Agent: "claude"})

	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--resource", "cpu:heavy")
	if id == remote {
		t.Error("a record from another machine was refreshed as the caller's own")
	}

	res := w.run(c, "release", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.OK)

	if w.leaseByID(remote) == nil {
		t.Error("release removed a record from another machine")
	}
}

func TestPathRecordFromAnotherMachineStillBlocksAPathClaim(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	w.plant(plantSpec{PID: 77, Paths: []string{repo}, Machine: otherMachine})

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
}

func TestBreakOfAResourceLeaseNeedsForceWhileItsHolderLives(t *testing.T) {
	w := newWorld(t)
	w.table.Add(77, proctest.Process{Start: startOf(77)})

	id := w.plant(plantSpec{PID: 77, Resource: "cpu:heavy"})

	res := w.run(w.claude(200, "s1"), "break", id)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "cpu:heavy")

	w.kill(77)

	res = w.run(w.claude(200, "s1"), "break", id)
	wantStatus(t, res, exitcode.OK)
}

func TestBreakingALiveResourceLeaseNamesItInTheTombstone(t *testing.T) {
	w := newWorld(t)
	owner := w.claude(200, "s1")

	id := w.acquireOK(owner, "--resource", "cpu:heavy")

	res := w.run(w.claude(300, "s2"), "break", "--force", id)
	wantStatus(t, res, exitcode.OK)

	res = w.run(owner, "release", id)
	wantStatus(t, res, exitcode.Broken)
	wantContains(t, "stderr", res.Stderr, "cpu:heavy")
}

func TestResourceRecordWithAMismatchedFootprintIsUnusable(t *testing.T) {
	w := newWorld(t)

	w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(`{
  "version": "1.1", "id": "01JAAAAAAAAAAAAAAAAAAAAAAA",
  "footprint": [{"type": "name", "name": "cpu:light"}], "clear_when_dead": true, "liveness": "process",
  "machine": {"host": "laptop", "id": "machine-1"},
  "holder": {"agent": "claude", "pid": 77, "start": {"platform": "darwin", "sec": 77}},
  "reach": "machine", "domain": {"kind": "resource", "name": "cpu:heavy", "slots": 1}
}`))

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.Unusable)
	wantContains(t, "stderr", res.Stderr, "footprint disagrees with the body")

	leases := leasesOf(t, w.listJSON(w.terminal(900)))
	if len(leases) != 1 || leases[0]["usable"] != false {
		t.Errorf("leases = %v, want one unusable entry", leases)
	}
}
