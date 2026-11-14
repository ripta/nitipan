package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/state"
)

func leaseByID(t *testing.T, doc map[string]any, id string) map[string]any {
	t.Helper()

	for _, l := range leasesOf(t, doc) {
		if l["id"] == id {
			return l
		}
	}

	t.Fatalf("no lease %s in %v", id, doc["leases"])

	return nil
}

func decodeStdout(t *testing.T, res result) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, res.Stdout)
	}

	return doc
}

func stringList(t *testing.T, v any) []string {
	t.Helper()

	raw, _ := v.([]any)
	out := make([]string, len(raw))

	for i, r := range raw {
		out[i], _ = r.(string)
	}

	return out
}

func wantArray(t *testing.T, doc map[string]any, field string, n int) {
	t.Helper()

	arr, ok := doc[field].([]any)
	if !ok || len(arr) != n {
		t.Errorf("%s = %v, want an array of %d", field, doc[field], n)
	}
}

func TestListJSONLinksSubLeasesToTheirParent(t *testing.T) {
	tr := newTree(t)

	doc := tr.w.listJSON(tr.third)

	if doc["schema_version"] != "1.6" {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}

	parent := leaseByID(t, doc, tr.parentID)
	child := leaseByID(t, doc, tr.childID)

	if child["parent"] != tr.parentID {
		t.Errorf("child parent = %v, want %s", child["parent"], tr.parentID)
	}

	if _, ok := parent["parent"]; ok {
		t.Errorf("a parent carries a parent link: %v", parent)
	}

	delegated, _ := parent["delegated"].([]any)
	if len(delegated) != 2 {
		t.Fatalf("delegated = %v, want both sub-leases", parent["delegated"])
	}

	first, _ := delegated[0].(map[string]any)
	if first["id"] != tr.childID && first["id"] != tr.siblingID {
		t.Errorf("delegated[0] = %v", first)
	}

	keys := stringList(t, first["keys"])
	if len(keys) != 1 || !strings.HasPrefix(keys[0], tr.repo) {
		t.Errorf("delegated keys = %v", keys)
	}

	if _, ok := child["delegated"]; ok {
		t.Errorf("a sub-lease lists delegations: %v", child)
	}

	for _, l := range leasesOf(t, doc) {
		for _, mark := range []string{"owner", "grantor_of"} {
			if _, ok := l[mark]; ok {
				t.Errorf("an unfiltered list carries %s: %v", mark, l)
			}
		}
	}

	wantArray(t, doc, "requests", 0)
	wantArray(t, doc, "notices", 0)
}

type filterMarkTest struct {
	Name          string
	Scope         string
	ParentOwner   bool
	ParentGrantor []string
	ChildOwner    bool
	ChildListed   bool
}

var filterMarkTests = []filterMarkTest{
	{
		Name:        "a filter on the checkout makes the parent owner and grantor of both sub-leases",
		ParentOwner: true, ParentGrantor: []string{"child", "sibling"},
		ChildOwner: true, ChildListed: true,
	},
	{
		Name: "a filter on a delegated key makes the child owner and the parent only grantor", Scope: "src",
		ParentGrantor: []string{"child"},
		ChildOwner:    true, ChildListed: true,
	},
	{
		Name: "a filter inside a delegated key makes the child owner and the parent only grantor", Scope: "src/api",
		ParentGrantor: []string{"child"},
		ChildOwner:    true, ChildListed: true,
	},
	{
		Name: "a filter on the parent's own part makes it the owner and lists no child", Scope: "lib",
		ParentOwner: true,
	},
}

func TestListJSONMarksOwnerAndGrantorForAFilteredQuery(t *testing.T) {
	for _, test := range filterMarkTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			args := []string{"--checkout", tr.repo}
			if test.Scope != "" {
				args = append(args, "--scope", test.Scope)
			}

			doc := tr.w.listJSON(tr.third, args...)
			parent := leaseByID(t, doc, tr.parentID)

			if parent["owner"] != test.ParentOwner {
				t.Errorf("parent owner = %v, want %v", parent["owner"], test.ParentOwner)
			}

			ids := map[string]string{"child": tr.childID, "sibling": tr.siblingID}
			want := make([]string, len(test.ParentGrantor))

			for i, name := range test.ParentGrantor {
				want[i] = ids[name]
			}

			got := stringList(t, parent["grantor_of"])
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("parent grantor_of = %v, want %v", got, want)
			}

			if !test.ChildListed {
				return
			}

			child := leaseByID(t, doc, tr.childID)
			if child["owner"] != test.ChildOwner {
				t.Errorf("child owner = %v, want %v", child["owner"], test.ChildOwner)
			}

			if _, ok := child["grantor_of"]; ok {
				t.Errorf("a sub-lease is grantor of %v", child["grantor_of"])
			}
		})
	}
}

func TestListJSONShowsPendingRequestsOnly(t *testing.T) {
	tr := newTree(t)

	pending := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	granting := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib/x")

	tr.w.editJSON(tr.w.requestPath(granting), func(doc map[string]any) {
		doc["state"] = "granting"
	})

	doc := tr.w.listJSON(tr.child)

	reqs, _ := doc["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("requests = %v, want only the pending one", doc["requests"])
	}

	req, _ := reqs[0].(map[string]any)
	if req["id"] != pending || req["parent"] != tr.parentID {
		t.Errorf("request = %v", req)
	}

	requester, _ := req["requester"].(map[string]any)
	if requester["handle"] == "" || requester["handle"] == nil {
		t.Errorf("requester = %v, want its handle", requester)
	}

	if keys := stringList(t, req["keys"]); len(keys) != 1 || !strings.HasSuffix(keys[0], "lib") {
		t.Errorf("keys = %v", keys)
	}

	filtered := tr.w.listJSON(tr.child, "--checkout", tr.repo, "--scope", "docs")
	wantArray(t, filtered, "requests", 0)

	filtered = tr.w.listJSON(tr.child, "--checkout", tr.repo, "--scope", "lib")
	wantArray(t, filtered, "requests", 1)
}

func TestListTextIndentsSubLeasesAndRequestsUnderTheirParent(t *testing.T) {
	tr := newTree(t)

	request := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	res := tr.w.run(tr.third, "list")
	wantStatus(t, res, exitcode.OK)

	lines := strings.Split(res.Stdout, "\n")

	var seen = map[string]string{}

	for _, line := range lines {
		for _, id := range []string{tr.parentID, tr.childID, tr.siblingID, request} {
			if strings.Contains(line, id) && !strings.Contains(line, "parent link") {
				if _, ok := seen[id]; !ok {
					seen[id] = line
				}
			}
		}
	}

	if strings.HasPrefix(seen[tr.parentID], " ") {
		t.Errorf("the parent is indented: %q", seen[tr.parentID])
	}

	for _, id := range []string{tr.childID, tr.siblingID} {
		if !strings.HasPrefix(seen[id], "  "+id) {
			t.Errorf("the sub-lease %s is not indented under its parent: %q", id, seen[id])
		}
	}

	if !strings.HasPrefix(seen[request], "  request "+request) {
		t.Errorf("the request is not printed under its lease: %q", seen[request])
	}

	parentAt := strings.Index(res.Stdout, tr.parentID+"  ")
	requestAt := strings.Index(res.Stdout, "request "+request)
	childAt := strings.Index(res.Stdout, "  "+tr.childID)

	if parentAt != 0 || requestAt < parentAt || childAt < requestAt {
		t.Errorf("order is parent, then its requests, then its sub-leases: %d %d %d\n%s", parentAt, requestAt, childAt,
			res.Stdout)
	}
}

func removeLeaseFile(t *testing.T, w *world, id string) {
	t.Helper()

	if err := os.Remove(filepath.Join(w.leasesDir(), id+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestListShowsADanglingSubLeaseAtTheTopLevelWithoutTheLock(t *testing.T) {
	tr := newTree(t)

	removeLeaseFile(t, tr.w, tr.parentID)

	release := tr.w.holdLock()
	defer release()

	doc := tr.w.listJSON(tr.third)

	child := leaseByID(t, doc, tr.childID)
	if child["parent"] != tr.parentID {
		t.Errorf("child parent = %v, want the stored link", child["parent"])
	}

	filtered := tr.w.listJSON(tr.third, "--checkout", tr.repo, "--scope", "src")
	if child := leaseByID(t, filtered, tr.childID); child["owner"] != true {
		t.Errorf("child = %v, want it the owner", child)
	}

	for _, l := range leasesOf(t, filtered) {
		if _, ok := l["grantor_of"]; ok {
			t.Errorf("a dangling sub-lease has a grantor: %v", l)
		}
	}

	res := tr.w.run(tr.third, "list")
	wantStatus(t, res, exitcode.OK)

	if !strings.Contains(res.Stdout, "\n"+tr.childID+"  ") && !strings.HasPrefix(res.Stdout, tr.childID+"  ") {
		t.Errorf("the child is not at the top level:\n%s", res.Stdout)
	}

	if !strings.Contains(res.Stdout, "parent link: "+tr.parentID) {
		t.Errorf("the child does not show its stored link:\n%s", res.Stdout)
	}

	if names := tr.w.noticesOf("parent-ended"); len(names) != 0 {
		t.Errorf("a list without the lock wrote notices %v", names)
	}
}

func TestListWithTheLockRepairsADanglingSubLeaseAndDeliversItsNotice(t *testing.T) {
	tr := newTree(t)

	removeLeaseFile(t, tr.w, tr.parentID)

	res := tr.w.run(tr.child, "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "nitipan: notice:", "lost its parent")

	names := tr.w.noticesOf("parent-ended")
	if len(names) != 1 || names[0] != tr.siblingID+"-parent-ended.json" {
		t.Errorf("notices = %v, want only the sibling's, since the child's was delivered", names)
	}
}

func TestListWithTheLockReportsDeliveredNoticesInJSON(t *testing.T) {
	tr := newTree(t)

	removeLeaseFile(t, tr.w, tr.parentID)

	res := tr.w.run(tr.child, "list", "--json")
	wantStatus(t, res, exitcode.OK)

	doc := decodeStdout(t, res)

	notices, _ := doc["notices"].([]any)
	if len(notices) != 1 {
		t.Fatalf("notices = %v, want the one it delivered", doc["notices"])
	}

	n, _ := notices[0].(map[string]any)
	summary, _ := n["summary"].(string)

	if n["cause"] != "parent-ended" || n["target"] != tr.childID || !strings.Contains(summary, "lost its parent") {
		t.Errorf("notice = %v", n)
	}

	child := leaseByID(t, doc, tr.childID)
	if _, ok := child["parent"]; ok {
		t.Errorf("the repaired sub-lease still names a parent: %v", child)
	}

	again := tr.w.listJSON(tr.child)
	wantArray(t, again, "notices", 0)
}

func TestListWithTheLockDeliversOnlyTheCallersNotices(t *testing.T) {
	tr := newTree(t)

	removeLeaseFile(t, tr.w, tr.parentID)

	doc := tr.w.listJSON(tr.third)
	wantArray(t, doc, "notices", 0)

	if names := tr.w.noticesOf("parent-ended"); len(names) != 2 {
		t.Errorf("notices = %v, want both kept for their sub-lessees", names)
	}

	doc = tr.w.listJSON(tr.sibling)
	wantArray(t, doc, "notices", 1)
}

func TestListWithoutTheLockReportsNoNoticesInJSON(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	wantStatus(t, tr.w.run(tr.parent, "decline", id), exitcode.OK)

	release := tr.w.holdLock()
	defer release()

	doc := tr.w.listJSON(tr.third)
	wantArray(t, doc, "notices", 0)

	if names := tr.w.fileNames(state.NoticesDir); len(names) != 1 {
		t.Errorf("notices = %v, want it kept", names)
	}
}

func TestListStillPrintsWhenARepairCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}

	tr := newTree(t)

	removeLeaseFile(t, tr.w, tr.parentID)

	dir := filepath.Join(tr.w.state, state.NoticesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	res := tr.w.run(tr.child, "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "warning: repairing requests and sub-leases failed", "notices are not delivered")

	doc := decodeStdout(t, res)
	wantArray(t, doc, "notices", 0)

	if leases := leasesOf(t, doc); len(leases) != 2 {
		t.Errorf("leases = %d, want both sub-leases still listed", len(leases))
	}
}
