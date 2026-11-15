package cli

import (
	"path/filepath"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

func leasesOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()

	raw, ok := doc["leases"].([]any)
	if !ok {
		t.Fatalf("leases = %v", doc["leases"])
	}

	out := make([]map[string]any, len(raw))

	for i, r := range raw {
		lease, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("lease %d = %v", i, r)
		}

		out[i] = lease
	}

	return out
}

func liveStatus(t *testing.T, lease map[string]any) string {
	t.Helper()

	l, ok := lease["liveness"].(map[string]any)
	if !ok {
		t.Fatalf("liveness = %v", lease["liveness"])
	}

	s, _ := l["status"].(string)

	return s
}

func TestListJSONEmitsALeaseOfAnUnknownKindWithLivenessAndFootprint(t *testing.T) {
	w := newWorld(t)
	w.table.Add(77, proctest.Process{Start: startOf(77)})

	id := w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}, Version: "1.4", Kind: "future"})

	doc := w.listJSON(w.terminal(900))
	if doc["schema_version"] != "1.4" {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}

	leases := leasesOf(t, doc)
	if len(leases) != 1 {
		t.Fatalf("%d leases, want 1", len(leases))
	}

	lease := leases[0]

	if lease["id"] != id || lease["kind"] != "future" || lease["usable"] != true || liveStatus(t, lease) != "alive" {
		t.Errorf("lease = %v", lease)
	}

	footprint, ok := lease["footprint"].([]any)
	if !ok || len(footprint) != 1 {
		t.Errorf("footprint = %v", lease["footprint"])
	}

	if tombs, ok := doc["tombstones"].([]any); !ok || len(tombs) != 0 {
		t.Errorf("tombstones = %v, want an empty array", doc["tombstones"])
	}

	w.kill(77)

	if got := liveStatus(t, leasesOf(t, w.listJSON(w.terminal(900)))[0]); got != "dead" {
		t.Errorf("liveness after the holder died = %q, want dead", got)
	}
}

func TestListJSONShowsAnUnusableRecordAsUnknownWithItsFile(t *testing.T) {
	w := newWorld(t)

	w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(`{"version":"2.0","id":"01JAAAAAAAAAAAAAAAAAAAAAAA"}`))

	leases := leasesOf(t, w.listJSON(w.terminal(900)))
	if len(leases) != 1 {
		t.Fatalf("%d leases, want 1", len(leases))
	}

	if leases[0]["usable"] != false || liveStatus(t, leases[0]) != "unknown" ||
		leases[0]["file"] != filepath.Join(w.leasesDir(), "01JAAAAAAAAAAAAAAAAAAAAAAA.json") {
		t.Errorf("lease = %v", leases[0])
	}
}

func TestListFiltersByDomainWithTheOverlapRule(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.table.Add(77, proctest.Process{Start: startOf(77)})

	a := w.plant(plantSpec{PID: 77, Paths: []string{filepath.Join(repo, "a")}})
	w.plant(plantSpec{PID: 77, Paths: []string{filepath.Join(repo, "ab")}})

	leases := leasesOf(t, w.listJSON(w.terminal(900), "--checkout", repo, "--scope", "a"))
	if len(leases) != 1 || leases[0]["id"] != a {
		t.Errorf("filtered leases = %v, want only %s (a shared prefix is not an overlap)", leases, a)
	}

	if all := leasesOf(t, w.listJSON(w.terminal(900))); len(all) != 2 {
		t.Errorf("unfiltered list has %d leases, want 2", len(all))
	}
}

func TestListFlagsANameSharedByMoreThanOneLiveHolder(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo", "a"), "--name", "twin")
	w.acquireOK(w.claude(300, "s2"), "--checkout", w.dir("repo", "b"), "--name", "twin")
	w.acquireOK(w.claude(400, "s3"), "--checkout", w.dir("repo", "c"), "--name", "solo")

	for _, lease := range leasesOf(t, w.listJSON(w.terminal(900))) {
		holder, ok := lease["holder"].(map[string]any)
		if !ok {
			t.Fatalf("holder = %v", lease["holder"])
		}

		want := holder["name"] == "twin"
		if got := lease["name_shared"] == true; got != want {
			t.Errorf("name %v: name_shared = %v, want %v", holder["name"], got, want)
		}
	}
}

func TestListPrintsTheHandleApartFromTheAgentName(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo"), "--name", "alpha", "--description", "docs")
	handle := w.leases()[0].Holder.Handle

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "handle: "+handle, "agent name: alpha", "alive", "description: docs")
}

func TestListWithNoLeasesSaysSo(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "no leases")
}

type machineTest struct {
	Name    string
	Machine record.Machine
	Want    string
}

var machineTests = []machineTest{
	{Name: "same machine id under another host name", Machine: record.Machine{Host: "renamed", ID: "machine-1"}, Want: "alive"},
	{Name: "other machine id", Machine: record.Machine{Host: "laptop", ID: "machine-2"}, Want: "unknown"},
}

func TestAHostNameChangeDoesNotMakeOwnRecordsReadAsRemote(t *testing.T) {
	for _, test := range machineTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.table.Add(77, proctest.Process{Start: startOf(77)})
			w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}, Machine: test.Machine})

			if got := liveStatus(t, leasesOf(t, w.listJSON(w.terminal(900)))[0]); got != test.Want {
				t.Errorf("liveness = %q, want %q", got, test.Want)
			}
		})
	}
}

func TestListRunsAsAnAnonymousCallerWithStrayAgentVariables(t *testing.T) {
	w := newWorld(t)
	w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo"))

	res := w.run(w.stray(900), "list", "--json")
	wantStatus(t, res, exitcode.OK)
}
