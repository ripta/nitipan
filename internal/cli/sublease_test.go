package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const missingID = "01JZZZZZZZZZZZZZZZZZZZZZZZ"

// editJSON rewrites a state file in place, bypassing every nitipan rule.
func (w *world) editJSON(path string, edit func(doc map[string]any)) {
	w.t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		w.t.Fatal(err)
	}

	edit(doc)

	data, err = json.Marshal(doc)
	if err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func pathDomain(p string) map[string]any {
	return map[string]any{"kind": "path", "checkout": p, "keys": []any{map[string]any{"path": p}}}
}

// rewriteLeaseKeys narrows a lease to one key, keeping the footprint and body in agreement.
func (w *world) rewriteLeaseKeys(id, p string) {
	w.t.Helper()

	w.editJSON(filepath.Join(w.leasesDir(), id+".json"), func(doc map[string]any) {
		doc["domain"] = pathDomain(p)
		doc["footprint"] = []any{map[string]any{"type": "path", "path": p}}
	})
}

func (w *world) requestPath(id string) string {
	return filepath.Join(w.state, state.RequestsDir, id+".json")
}

// rewriteRequestKeys changes the keys a pending request names.
func (w *world) rewriteRequestKeys(id, p string) {
	w.t.Helper()

	w.editJSON(w.requestPath(id), func(doc map[string]any) { doc["domain"] = pathDomain(p) })
}

// rewriteRequestHolder changes who a pending request says made it.
func (w *world) rewriteRequestHolder(id string, holder record.Holder) {
	w.t.Helper()

	data, err := json.Marshal(holder)
	if err != nil {
		w.t.Fatal(err)
	}

	var as map[string]any
	if err := json.Unmarshal(data, &as); err != nil {
		w.t.Fatal(err)
	}

	w.editJSON(w.requestPath(id), func(doc map[string]any) { doc["holder"] = as })
}

// requestOK asks for part of a lease and returns the request ID.
func (w *world) requestOK(c caller, parentID string, args ...string) string {
	w.t.Helper()

	res := w.run(c, append([]string{"request", parentID}, args...)...)
	wantStatus(w.t, res, exitcode.OK)

	first, _, _ := strings.Cut(res.Stdout, "\n")
	id, found := strings.CutPrefix(first, "requested ")

	if !found || !state.ValidID(id) {
		w.t.Fatalf("request printed %q, want the request ID first", res.Stdout)
	}

	return id
}

// delegate runs request and grant, and returns the sub-lease's ID.
func (w *world) delegate(requester, grantor caller, parentID, scope string, extra ...string) string {
	w.t.Helper()

	args := append([]string{"--checkout", w.dir("repo"), "--scope", scope}, extra...)
	id := w.requestOK(requester, parentID, args...)

	wantStatus(w.t, w.run(grantor, "grant", id), exitcode.OK)

	// The requester's next call delivers the granted notice. Acquiring the same keys is the equal-key
	// refresh that the grant makes possible.
	refresh := w.run(requester, "acquire", "--checkout", w.dir("repo"), "--scope", scope)
	wantStatus(w.t, refresh, exitcode.OK)
	wantContains(w.t, "stderr", refresh.Stderr, "was granted")
	wantContains(w.t, "stdout", refresh.Stdout, "already held; nothing changed")

	return id
}

// fileNames lists the published files of a state subdirectory, in name order.
func (w *world) fileNames(sub string) []string {
	w.t.Helper()

	entries, err := os.ReadDir(filepath.Join(w.state, sub))
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		w.t.Fatal(err)
	}

	var out []string

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}

	sort.Strings(out)

	return out
}

// tree is a parent that holds a repo, with two sub-leases and a third party.
type tree struct {
	w       *world
	repo    string
	parent  caller
	child   caller
	sibling caller
	third   caller

	parentID  string
	childID   string
	siblingID string
}

func newTree(t *testing.T) *tree {
	t.Helper()

	w := newWorld(t)

	tr := &tree{
		w:       w,
		repo:    w.dir("repo"),
		parent:  w.claude(200, "p"),
		child:   w.claude(300, "c"),
		sibling: w.claude(500, "x"),
		third:   w.claude(400, "t"),
	}

	for _, dir := range []string{"src/api", "src/handlers", "docs", "lib"} {
		w.dir("repo", dir)
	}

	w.dir("other")

	tr.parentID = w.acquireOK(tr.parent, "--checkout", tr.repo, "--name", "parent-agent")
	tr.childID = w.delegate(tr.child, tr.parent, tr.parentID, "src", "--name", "child-agent")
	tr.siblingID = w.delegate(tr.sibling, tr.parent, tr.parentID, "docs", "--name", "sibling-agent")

	return tr
}

func (tr *tree) actor(who string) caller {
	return map[string]caller{"parent": tr.parent, "child": tr.child, "sibling": tr.sibling, "third": tr.third}[who]
}

type overlapTest struct {
	Name     string
	Who      string
	Args     []string
	Status   int
	Contains []string
	Absent   []string
}

var treeOverlapTests = []overlapTest{
	{
		Name: "the parent refreshing its exact keys exits with its own status and lists the delegations",
		Who:  "parent", Args: []string{"--checkout", "{repo}"},
		Status:   exitcode.Delegated,
		Contains: []string{"already held", "delegated:", "child-agent", "sibling-agent"},
	},
	{
		Name: "the parent claiming a delegated key is refused naming the sub-lessee",
		Who:  "parent", Args: []string{"--checkout", "{repo}", "--scope", "src"},
		Status:   exitcode.Refused,
		Contains: []string{"child-agent", "a sub-lessee of your lease", "Coordinate with the holder or ask the user"},
		Absent:   []string{"revoke", "--force", "nitipan request"},
	},
	{
		Name: "the parent claiming inside a delegated key is refused naming the sub-lessee",
		Who:  "parent", Args: []string{"--checkout", "{repo}", "--scope", "src/api"},
		Status:   exitcode.Refused,
		Contains: []string{"child-agent"},
		Absent:   []string{"revoke", "nitipan request"},
	},
	{
		Name: "the parent claiming an undelegated key is covered by its own lease",
		Who:  "parent", Args: []string{"--checkout", "{repo}", "--scope", "lib"},
		Status: exitcode.AlreadyCovered,
	},
	{
		Name: "the sub-lessee refreshing its exact keys is not blocked by its parent",
		Who:  "child", Args: []string{"--checkout", "{repo}", "--scope", "src"},
		Status:   exitcode.OK,
		Contains: []string{"already held; nothing changed"},
	},
	{
		Name: "the sub-lessee claiming inside its sub-lease is covered, not refused by its parent",
		Who:  "child", Args: []string{"--checkout", "{repo}", "--scope", "src/handlers"},
		Status: exitcode.AlreadyCovered,
	},
	{
		Name: "the sub-lessee reaching outside its sub-lease is refused by its parent",
		Who:  "child", Args: []string{"--checkout", "{repo}", "--scope", "lib"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent"},
	},
	{
		Name: "the sub-lessee claiming a sibling's part is refused naming the parent and the sibling",
		Who:  "child", Args: []string{"--checkout", "{repo}", "--scope", "docs"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent", "sibling-agent"},
	},
	{
		Name: "a third party claiming a delegated key is refused naming both holders",
		Who:  "third", Args: []string{"--checkout", "{repo}", "--scope", "src"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent", "child-agent"},
		Absent:   []string{"nitipan request"},
	},
	{
		Name: "a third party claiming only the parent's own part is refused naming only the parent",
		Who:  "third", Args: []string{"--checkout", "{repo}", "--scope", "lib"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent", "nitipan request " + "{parentID}"},
		Absent:   []string{"child-agent", "sibling-agent"},
	},
	{
		Name: "a third party claiming the whole repo is refused naming the parent and every sub-lessee",
		Who:  "third", Args: []string{"--checkout", "{repo}"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent", "child-agent", "sibling-agent"},
		Absent:   []string{"nitipan request"},
	},
	{
		Name: "a third party outside the tree is granted",
		Who:  "third", Args: []string{"--checkout", "{other}"},
		Status: exitcode.OK,
	},
	{
		Name: "a sibling claiming inside another sub-lease is refused naming that sub-lessee and the parent",
		Who:  "sibling", Args: []string{"--checkout", "{repo}", "--scope", "src/api"},
		Status:   exitcode.Refused,
		Contains: []string{"child-agent", "parent-agent"},
	},
}

func (tr *tree) expand(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "{repo}", tr.repo)
		a = strings.ReplaceAll(a, "{other}", filepath.Join(tr.w.root, "other"))
		out[i] = strings.ReplaceAll(a, "{parentID}", tr.parentID)
	}

	return out
}

func TestOverlapWithAParentLink(t *testing.T) {
	for _, test := range treeOverlapTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			res := tr.w.run(tr.actor(test.Who), append([]string{"acquire"}, tr.expand(test.Args)...)...)
			wantStatus(t, res, test.Status)
			wantContains(t, "output", res.Stdout+res.Stderr, tr.expand(test.Contains)...)
			wantAbsent(t, "output", res.Stdout+res.Stderr, tr.expand(test.Absent)...)
		})
	}
}

// controlTests plant the same shape without a parent link, as a V1 binary could leave it. Nothing is
// exempt, so the same claims are refused as ordinary overlaps.
var controlTests = []overlapTest{
	{
		Name: "the holder of the outer lease refreshing its keys is refused by the inner one",
		Who:  "parent", Args: []string{"--checkout", "{repo}"},
		Status:   exitcode.Refused,
		Contains: []string{"child-agent"},
		Absent:   []string{"delegated:"},
	},
	{
		Name: "the holder of the inner lease claiming inside it is refused by the outer one",
		Who:  "child", Args: []string{"--checkout", "{repo}", "--scope", "src/handlers"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent"},
	},
	{
		Name: "a third party is refused naming both",
		Who:  "third", Args: []string{"--checkout", "{repo}", "--scope", "src"},
		Status:   exitcode.Refused,
		Contains: []string{"parent-agent", "child-agent"},
	},
}

func TestOverlapWithoutAParentLinkExemptsNothing(t *testing.T) {
	for _, test := range controlTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			repo := w.dir("repo")
			w.dir("repo", "src", "handlers")

			parent, child, third := w.claude(200, "p"), w.claude(300, "c"), w.claude(400, "t")
			parentID := w.acquireOK(parent, "--checkout", repo, "--name", "parent-agent")

			w.plant(plantSpec{PID: 300, Paths: []string{filepath.Join(repo, "src")}, Name: "child-agent"})

			tr := &tree{w: w, repo: repo, parent: parent, child: child, third: third, parentID: parentID}

			res := w.run(tr.actor(test.Who), append([]string{"acquire"}, tr.expand(test.Args)...)...)
			wantStatus(t, res, test.Status)
			wantContains(t, "output", res.Stdout+res.Stderr, test.Contains...)
			wantAbsent(t, "output", res.Stdout+res.Stderr, test.Absent...)
		})
	}
}

func TestADeadParentCountsAsAbsent(t *testing.T) {
	t.Run("the sub-lessee still holds its keys and is covered inside them", func(t *testing.T) {
		tr := newTree(t)
		tr.w.kill(200)

		res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src/handlers")
		wantStatus(t, res, exitcode.AlreadyCovered)

		res = tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src")
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stdout", res.Stdout, "already held; nothing changed")
	})

	t.Run("the sub-lessee claiming the parent's undelegated part clears the dead parent", func(t *testing.T) {
		tr := newTree(t)
		tr.w.kill(200)

		res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "lib")
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stderr", res.Stderr, "cleared", tr.parentID)

		if tr.w.leaseByID(tr.parentID) != nil {
			t.Error("the dead parent's record survived")
		}
	})

	t.Run("a third party gets the parent's undelegated part but not the sub-lease", func(t *testing.T) {
		tr := newTree(t)
		tr.w.kill(200)

		res := tr.w.run(tr.third, "acquire", "--checkout", tr.repo, "--scope", "src")
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "child-agent")
		wantAbsent(t, "stderr", res.Stderr, "parent-agent")

		res = tr.w.run(tr.third, "acquire", "--checkout", tr.repo, "--scope", "lib")
		wantStatus(t, res, exitcode.OK)
	})
}

func TestADanglingParentCountsAsAbsent(t *testing.T) {
	setup := func(t *testing.T) (*world, string, caller) {
		t.Helper()

		w := newWorld(t)
		repo := w.dir("repo")
		w.dir("repo", "src", "handlers")
		w.dir("repo", "lib")

		child := w.claude(300, "c")
		w.plant(plantSpec{PID: 300, Paths: []string{filepath.Join(repo, "src")}, Name: "child-agent", Parent: missingID})

		return w, repo, child
	}

	t.Run("the sub-lessee is covered inside its keys", func(t *testing.T) {
		w, repo, child := setup(t)

		wantStatus(t, w.run(child, "acquire", "--checkout", repo, "--scope", "src/handlers"), exitcode.AlreadyCovered)
	})

	t.Run("a third party is refused naming only the sub-lessee", func(t *testing.T) {
		w, repo, _ := setup(t)

		res := w.run(w.claude(400, "t"), "acquire", "--checkout", repo, "--scope", "src")
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "child-agent", "a sub-lessee of lease "+missingID)
		wantAbsent(t, "stderr", res.Stderr, "nitipan request")
	})

	t.Run("the rest of the former parent's domain is free", func(t *testing.T) {
		w, repo, _ := setup(t)

		wantStatus(t, w.run(w.claude(400, "t"), "acquire", "--checkout", repo, "--scope", "lib"), exitcode.OK)
	})
}

func TestRefusalsNamingASubLesseeAreWaitable(t *testing.T) {
	tr := newTree(t)

	tr.w.fakeWait(func(n int) {
		if n == 1 {
			wantStatus(t, tr.w.as(tr.child, "release", tr.childID), exitcode.OK)
		}
	})

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src", "--wait", "30s")
	wantStatus(t, res, exitcode.AlreadyCovered)
}

func TestARefreshWithNoLiveDelegationsIsPlainSuccess(t *testing.T) {
	tr := newTree(t)

	wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)
	wantStatus(t, tr.w.run(tr.sibling, "release", tr.siblingID), exitcode.OK)

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo)
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stdout", res.Stdout, "delegated:")
}

func TestADeadSubLesseeIsNotListedAsADelegation(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(300)

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo)
	wantStatus(t, res, exitcode.Delegated)
	wantContains(t, "stdout", res.Stdout, "sibling-agent")
	wantAbsent(t, "stdout", res.Stdout, "child-agent")
}

func TestASubLeaseTakesItsIdentityAndAdvertisementFromTheRequest(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")

	parent, child := w.claude(200, "p"), w.claude(300, "c")
	parentID := w.acquireOK(parent, "--checkout", repo, "--name", "parent-agent", "--description", "the checkout")

	before, err := os.ReadFile(filepath.Join(w.leasesDir(), parentID+".json"))
	if err != nil {
		t.Fatal(err)
	}

	id := w.requestOK(child, parentID, "--checkout", repo, "--scope", "src", "--name", "child-agent",
		"--permission-mode", "plan", "--description", "the api")

	granted := w.run(parent, "grant", id)
	wantStatus(t, granted, exitcode.OK)
	wantContains(t, "stdout", granted.Stdout, "granted "+id, "agent name child-agent", "handle ")

	got := w.leaseByID(id)
	if got == nil {
		t.Fatalf("no lease %s after the grant", id)
	}

	if got.Parent != parentID || got.Version != record.ParentVersion || got.Holder.Name != "child-agent" ||
		got.PermissionMode != "plan" || got.Description != "the api" || got.Holder.SessionID != "c" {
		t.Errorf("sub-lease = %+v", got)
	}

	if len(got.Domain.Keys) != 1 || got.Domain.Keys[0].Path != filepath.Join(repo, "src") {
		t.Errorf("keys = %+v", got.Domain.Keys)
	}

	after, err := os.ReadFile(filepath.Join(w.leasesDir(), parentID+".json"))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Errorf("the parent's record changed:\n%s\n%s", before, after)
	}

	if parentRec := w.leaseByID(parentID); parentRec.Version != record.PathVersion {
		t.Errorf("the parent was stamped %s, want %s", parentRec.Version, record.PathVersion)
	}

	if names := w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests left after the grant: %v", names)
	}

	// The requester learns of the grant on its next call, once.
	res := w.run(child, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "nitipan: notice: your request "+id+" was granted")

	if names := w.fileNames(state.NoticesDir); len(names) != 0 {
		t.Errorf("notice left after delivery: %v", names)
	}
}

func TestRequestPrintsTheRequestIDAndTheParentsHandle(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")

	parentID := w.acquireOK(w.claude(200, "p"), "--checkout", repo, "--name", "parent-agent")

	res := w.run(w.claude(300, "c"), "request", parentID, "--checkout", repo, "--scope", "src")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "requested ", "parent handle: ", "parent agent name: parent-agent", "nitipan grant ")

	if names := w.fileNames(state.RequestsDir); len(names) != 1 {
		t.Errorf("requests = %v, want one", names)
	}

	if len(w.leases()) != 1 {
		t.Error("a request created a lease")
	}
}

type requestRefusalTest struct {
	Name     string
	Setup    func(*tree) (parentID string, args []string)
	Who      string
	Status   int
	Contains []string
	Absent   []string
}

var requestRefusalTests = []requestRefusalTest{
	{
		Name: "against a sub-lease",
		Setup: func(tr *tree) (string, []string) {
			return tr.childID, []string{"--checkout", tr.repo, "--scope", "src/api"}
		},
		Who: "third", Status: exitcode.Refused,
		Contains: []string{"sub-lease", "cannot delegate again"},
		Absent:   []string{"nitipan request", "revoke"},
	},
	{
		Name: "against a lease of the caller's own",
		Setup: func(tr *tree) (string, []string) {
			return tr.parentID, []string{"--checkout", tr.repo, "--scope", "lib"}
		},
		Who: "parent", Status: exitcode.Refused,
		Contains: []string{"is yours"},
	},
	{
		Name: "against keys the lease does not cover",
		Setup: func(tr *tree) (string, []string) {
			return tr.parentID, []string{"--checkout", filepath.Join(tr.w.root, "other")}
		},
		Who: "third", Status: exitcode.Refused,
		Contains: []string{"does not cover"},
	},
	{
		Name: "against keys wider than the lease",
		Setup: func(tr *tree) (string, []string) {
			tr.w.dir("wide", "a")
			id := tr.w.acquireOK(tr.third, "--checkout", filepath.Join(tr.w.root, "wide", "a"))

			return id, []string{"--checkout", filepath.Join(tr.w.root, "wide")}
		},
		Who: "sibling", Status: exitcode.Refused,
		Contains: []string{"does not cover"},
	},
	{
		Name: "against keys a live sub-lease covers",
		Setup: func(tr *tree) (string, []string) {
			return tr.parentID, []string{"--checkout", tr.repo, "--scope", "src/api"}
		},
		Who: "third", Status: exitcode.Refused,
		Contains: []string{"child-agent", "a sub-lessee of lease " + "{parentID}"},
	},
	{
		Name: "against keys that overlap a live sub-lease from above",
		Setup: func(tr *tree) (string, []string) {
			return tr.parentID, []string{"--checkout", tr.repo}
		},
		Who: "third", Status: exitcode.Refused,
		Contains: []string{"child-agent", "sibling-agent"},
	},
	{
		Name: "against a lease that does not exist",
		Setup: func(tr *tree) (string, []string) {
			return missingID, []string{"--checkout", tr.repo, "--scope", "lib"}
		},
		Who: "third", Status: exitcode.NoSuchLease,
	},
	{
		Name: "against a dead holder's lease",
		Setup: func(tr *tree) (string, []string) {
			tr.w.kill(200)

			return tr.parentID, []string{"--checkout", tr.repo, "--scope", "lib"}
		},
		Who: "third", Status: exitcode.Refused,
		Contains: []string{"has died"},
	},
}

func TestRequestRefusals(t *testing.T) {
	for _, test := range requestRefusalTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			parentID, args := test.Setup(tr)

			res := tr.w.run(tr.actor(test.Who), append([]string{"request", parentID}, args...)...)
			wantStatus(t, res, test.Status)

			for _, want := range test.Contains {
				wantContains(t, "stderr", res.Stderr, strings.ReplaceAll(want, "{parentID}", tr.parentID))
			}

			wantAbsent(t, "stderr", res.Stderr, test.Absent...)

			if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
				t.Errorf("a refused request left %v", names)
			}
		})
	}
}

func TestRequestAgainstAResourceLeaseIsRefused(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	id := w.acquireOK(w.claude(200, "p"), "--resource", "cpu:heavy")

	res := w.run(w.claude(300, "c"), "request", id, "--checkout", repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "resource lease cannot be shared in parts")
	wantAbsent(t, "stderr", res.Stderr, "nitipan request")
}

func TestRequestNeedsOneLeaseID(t *testing.T) {
	w := newWorld(t)

	wantStatus(t, w.run(w.claude(300, "c"), "request"), exitcode.Usage)
	wantStatus(t, w.run(w.claude(300, "c"), "request", "not-an-id"), exitcode.Usage)
	wantStatus(t, w.run(w.claude(300, "c"), "grant", "a", "b"), exitcode.Usage)
	wantStatus(t, w.run(w.claude(300, "c"), "decline"), exitcode.Usage)
}

func TestARefusedAcquireSuggestsRequestOnlyWhereTheBlockerCanDelegate(t *testing.T) {
	t.Run("the keys one plain lease covers", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		w.dir("repo", "src")

		parentID := w.acquireOK(w.claude(200, "p"), "--checkout", repo)

		res := w.run(w.claude(300, "c"), "acquire", "--checkout", repo, "--scope", "src")
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "nitipan request "+parentID+" --checkout ", " --scope src")
	})

	t.Run("not for keys the blocker only overlaps", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		w.dir("repo", "src")

		w.acquireOK(w.claude(200, "p"), "--checkout", repo, "--scope", "src")

		res := w.run(w.claude(300, "c"), "acquire", "--checkout", repo)
		wantStatus(t, res, exitcode.Refused)
		wantAbsent(t, "stderr", res.Stderr, "nitipan request")
	})

	t.Run("not for a resource lease", func(t *testing.T) {
		w := newWorld(t)

		w.acquireOK(w.claude(200, "p"), "--resource", "cpu:heavy")

		res := w.run(w.claude(300, "c"), "acquire", "--resource", "cpu:heavy")
		wantStatus(t, res, exitcode.Refused)
		wantAbsent(t, "stderr", res.Stderr, "nitipan request")
	})

	t.Run("not to a holder about its own lease", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		w.dir("repo", "src")

		c := w.claude(200, "p")
		w.acquireOK(c, "--checkout", repo, "--scope", "src")

		res := w.run(c, "acquire", "--checkout", repo)
		wantStatus(t, res, exitcode.Refused)
		wantAbsent(t, "stderr", res.Stderr, "nitipan request")
	})
}

func TestGrantRefusals(t *testing.T) {
	t.Run("a grant to the parent's own holder", func(t *testing.T) {
		tr := newTree(t)
		w := tr.w

		// A subagent shares its parent's holder, so it runs as the parent. Plant the request the way its
		// own request would have written it, since request itself refuses a lease of the caller's own.
		id := w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
		w.rewriteRequestHolder(id, w.leaseByID(tr.parentID).Holder)

		res := w.run(tr.parent, "grant", id)
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "your own holder", "Subagents share")

		if w.leaseByID(id) != nil {
			t.Error("a self-grant created a lease")
		}
	})

	t.Run("a dead requester", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
		tr.w.kill(400)

		res := tr.w.run(tr.parent, "grant", id)
		wantStatus(t, res, exitcode.NoSuchLease)

		if tr.w.leaseByID(id) != nil {
			t.Error("a dead requester was granted")
		}

		if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
			t.Errorf("the dead requester's request survived: %v", names)
		}
	})

	t.Run("keys the parent no longer covers", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

		// The parent narrows itself to the delegated parts, which drops lib.
		tr.w.rewriteLeaseKeys(tr.parentID, filepath.Join(tr.repo, "src"))

		res := tr.w.run(tr.parent, "grant", id)
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "does not cover", filepath.Join(tr.repo, "lib"))
	})

	t.Run("keys another live lease overlaps", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
		tr.w.plant(plantSpec{PID: 600, Paths: []string{filepath.Join(tr.repo, "lib")}, Name: "squatter"})
		tr.w.table.Add(600, proctest.Process{Start: startOf(600), PPID: 1, Name: "claude"})

		res := tr.w.run(tr.parent, "grant", id)
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "squatter")

		if tr.w.leaseByID(id) != nil {
			t.Error("a conflicting grant created a lease")
		}
	})

	t.Run("keys a live sub-lease overlaps", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
		tr.w.rewriteRequestKeys(id, filepath.Join(tr.repo, "src", "api"))

		res := tr.w.run(tr.parent, "grant", id)
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "child-agent")
	})

	t.Run("a caller who does not hold the parent", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

		res := tr.w.run(tr.child, "grant", id)
		wantStatus(t, res, exitcode.NotYours)
		wantContains(t, "stderr", res.Stderr, "Only that holder can grant")
	})

	t.Run("a request that does not exist", func(t *testing.T) {
		tr := newTree(t)

		wantStatus(t, tr.w.run(tr.parent, "grant", missingID), exitcode.NoSuchLease)
		wantStatus(t, tr.w.run(tr.parent, "grant", "nope"), exitcode.Usage)
	})

	t.Run("a request granted once is not granted again", func(t *testing.T) {
		tr := newTree(t)

		wantStatus(t, tr.w.run(tr.parent, "grant", tr.childID), exitcode.NoSuchLease)
	})
}

func TestDeclineByTheParentLeavesANoticeAndByTheRequesterLeavesNone(t *testing.T) {
	t.Run("the parent refuses", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

		res := tr.w.run(tr.parent, "decline", id)
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stdout", res.Stdout, "declined request "+id)

		res = tr.w.run(tr.third, "release", "--all")
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stderr", res.Stderr, "nitipan: notice: your request "+id, "declined")

		if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
			t.Errorf("requests = %v", names)
		}
	})

	t.Run("the requester withdraws", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

		res := tr.w.run(tr.third, "decline", id)
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stdout", res.Stdout, "withdrew request "+id)

		if names := tr.w.fileNames(state.NoticesDir); len(names) != 0 {
			t.Errorf("a withdrawal left notices: %v", names)
		}
	})

	t.Run("anyone else is refused", func(t *testing.T) {
		tr := newTree(t)

		id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

		wantStatus(t, tr.w.run(tr.child, "decline", id), exitcode.NotYours)

		if names := tr.w.fileNames(state.RequestsDir); len(names) != 1 {
			t.Errorf("requests = %v, want the request untouched", names)
		}
	})
}

func TestAnUnrelatedHolderGetsNoNoticeOfSomeoneElsesRequest(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	wantStatus(t, tr.w.run(tr.parent, "decline", id), exitcode.OK)

	res := tr.w.run(tr.child, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "notice")

	if names := tr.w.fileNames(state.NoticesDir); len(names) != 1 {
		t.Errorf("notices = %v, want the third party's still waiting", names)
	}
}

func TestADeadRequestersRequestIsReapedByTheNextLockHolder(t *testing.T) {
	tr := newTree(t)

	tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	tr.w.kill(400)

	wantStatus(t, tr.w.run(tr.child, "release", "--all"), exitcode.OK)

	if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests = %v, want the dead requester's reaped", names)
	}

	if names := tr.w.fileNames(state.NoticesDir); len(names) != 0 {
		t.Errorf("notices = %v, want none for a dead requester", names)
	}
}

func TestARequestWhoseParentLeaseIsGoneIsReapedWithADeclinedNotice(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	if err := os.Remove(filepath.Join(tr.w.leasesDir(), tr.parentID+".json")); err != nil {
		t.Fatal(err)
	}

	res := tr.w.run(tr.third, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "your request "+id, "declined")

	if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests = %v", names)
	}
}

func TestListWithoutTheLockDeliversNoNotices(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	wantStatus(t, tr.w.run(tr.parent, "decline", id), exitcode.OK)

	release := tr.w.holdLock()
	defer release()

	res := tr.w.run(tr.third, "list")
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "notice: your request")

	if names := tr.w.fileNames(state.NoticesDir); len(names) != 1 {
		t.Errorf("notices = %v, want it kept for the next locked call", names)
	}
}

func TestReplaceOnASubLeaseIsRefused(t *testing.T) {
	tr := newTree(t)

	for _, scope := range []string{"src", "src/api"} {
		res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", scope, "--replace", tr.childID)
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "sub-lease", "cannot change its keys")
		wantAbsent(t, "stderr", res.Stderr, "nitipan request")
	}

	// Widening is refused by the same rule and not by an overlap with the parent.
	res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--replace", tr.childID)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "cannot change its keys")

	if got := tr.w.leaseByID(tr.childID); len(got.Domain.Keys) != 1 || got.Domain.Keys[0].Path != filepath.Join(tr.repo, "src") {
		t.Errorf("the sub-lease changed: %+v", got.Domain.Keys)
	}
}

func TestParentReplaceThatDropsDelegatedKeysIsRefusedAndListsTheChildren(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "lib", "--replace", tr.parentID)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "child-agent", "sibling-agent", "drop part of "+tr.parentID)

	if got := tr.w.leaseByID(tr.parentID); got.Domain.Keys[0].Path != tr.repo {
		t.Errorf("a refused replace changed the parent: %+v", got.Domain.Keys)
	}
}

func TestParentReplaceThatKeepsDelegatedKeysSucceedsAndKeepsTheLeaseID(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src", "--scope", "docs", "--scope", "lib",
		"--replace", tr.parentID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "replaced")

	if got := tr.w.leaseByID(tr.parentID); len(got.Domain.Keys) != 3 {
		t.Errorf("keys = %+v", got.Domain.Keys)
	}

	if got := tr.w.leaseByID(tr.childID); got.Parent != tr.parentID {
		t.Errorf("the sub-lease no longer names its parent: %+v", got)
	}
}

func TestParentReplaceThatAbsorbsALeaseWithLiveChildrenIsRefused(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "a", "x")
	w.dir("repo", "b")

	parent, child := w.claude(200, "p"), w.claude(300, "c")

	a := w.acquireOK(parent, "--checkout", repo, "--scope", "a")
	b := w.acquireOK(parent, "--checkout", repo, "--scope", "b")

	id := w.requestOK(child, a, "--checkout", repo, "--scope", "a/x", "--name", "child-agent")
	wantStatus(t, w.run(parent, "grant", id), exitcode.OK)

	res := w.run(parent, "acquire", "--checkout", repo, "--replace", b, "--replace", a)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "would absorb a lease with sub-leases", "child-agent")

	if w.leaseByID(a) == nil || w.leaseByID(b) == nil {
		t.Errorf("a refused replace removed a lease: %v", w.leaseIDs())
	}
}

func TestAcquireWithLiveDelegationsStillOwnsNothingInThem(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo)
	wantStatus(t, res, exitcode.Delegated)
	wantContains(t, "stderr", res.Stderr, "part of the lease is delegated", "you hold "+tr.parentID)
}
