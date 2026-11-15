package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/gitfence"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/state"
)

func (w *world) marksDir() string {
	return filepath.Join(w.state, state.MarksDir)
}

// markIDs lists the published fence marks, by ID.
func (w *world) markIDs() []string {
	w.t.Helper()

	entries, err := os.ReadDir(w.marksDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		w.t.Fatal(err)
	}

	var out []string

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}

	return out
}

func (w *world) markDoc(id string) map[string]any {
	w.t.Helper()

	data, err := os.ReadFile(filepath.Join(w.marksDir(), id+".json"))
	if err != nil {
		w.t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		w.t.Fatal(err)
	}

	return doc
}

// markKeys lists the paths a published mark still guards.
func (w *world) markKeys(id string) []string {
	w.t.Helper()

	var out []string

	list, _ := w.markDoc(id)["keys"].([]any)
	for _, k := range list {
		held, _ := k.(map[string]any)
		path, _ := held["path"].(string)
		out = append(out, path)
	}

	return out
}

// markSpec describes a mark written straight into the state directory.
type markSpec struct {
	ID      string
	Lease   string
	PID     int
	Keys    []string
	Version string
}

func (w *world) plantMark(spec markSpec) {
	w.t.Helper()

	if spec.ID == "" {
		spec.ID = state.NewID()
	}

	if spec.Version == "" {
		spec.Version = "1.3"
	}

	if spec.Lease == "" {
		spec.Lease = absentID
	}

	var held []map[string]any
	for _, p := range spec.Keys {
		held = append(held, map[string]any{"path": p})
	}

	doc := map[string]any{
		"version": spec.Version, "id": spec.ID, "footprint": "none", "clear_when_dead": true,
		"liveness": "process", "machine": map[string]any{"host": testMachine.Host, "id": testMachine.ID},
		"holder": map[string]any{
			"agent": "claude", "pid": spec.PID, "start": map[string]any{"platform": "darwin", "sec": spec.PID},
		},
		"lease": spec.Lease, "keys": held, "anonymous": true,
		"broken_by": map[string]any{"pid": 900}, "broken_at": "2026-10-20T11:00:00Z",
	}

	data, err := json.Marshal(doc)
	if err != nil {
		w.t.Fatal(err)
	}

	w.writeMarkFile(spec.ID+".json", data)
}

func (w *world) writeMarkFile(name string, data []byte) {
	w.t.Helper()

	if err := os.MkdirAll(w.marksDir(), 0o700); err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(w.marksDir(), name), data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// judgeFor builds the judge the hook builds for a caller.
func (w *world) judgeFor(c caller) *lease.Judge {
	w.t.Helper()

	var stdout, stderr bytes.Buffer

	a := &app{deps: w.deps(c, &stdout, &stderr).withDefaults()}

	h, err := a.holder()
	if err != nil {
		w.t.Fatal(err)
	}

	e, err := a.engine()
	if err != nil {
		w.t.Fatal(err)
	}

	j, err := e.NewJudge(h)
	if err != nil {
		w.t.Fatal(err)
	}

	return j
}

// refusals judges paths inside a checkout for a caller.
func (w *world) refusals(c caller, checkout string, rel ...string) []lease.Refusal {
	w.t.Helper()

	var touched []keys.Key
	for _, p := range rel {
		touched = append(touched, keys.Key{Path: filepath.Join(checkout, p)})
	}

	return w.judgeFor(c).Refusals(keys.Key{Path: checkout}, touched)
}

// marksOf returns the marks array of a list document.
func marksOf(doc map[string]any) []map[string]any {
	list, _ := doc["marks"].([]any)

	out := make([]map[string]any, len(list))
	for i, m := range list {
		out[i], _ = m.(map[string]any)
	}

	return out
}

func refusalKinds(rs []lease.Refusal) []lease.RefusalKind {
	var out []lease.RefusalKind
	for _, r := range rs {
		out = append(out, r.Kind)
	}

	return out
}

func TestForceBreakWritesAFenceMarkBesideTheTombstone(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")
	holder := w.claude(200, "s1")

	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src", "--name", "alpha")

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	marks := w.markIDs()
	if len(marks) != 1 || len(w.tombIDs()) != 1 {
		t.Fatalf("marks = %v, tombstones = %v, want one of each", marks, w.tombIDs())
	}

	doc := w.markDoc(marks[0])
	who, _ := doc["holder"].(map[string]any)

	if doc["version"] != "1.3" || doc["footprint"] != "none" || doc["lease"] != id || who["pid"] != float64(200) {
		t.Errorf("mark = %v", doc)
	}

	if got := w.markKeys(marks[0]); len(got) != 1 || got[0] != src {
		t.Errorf("mark keys = %v, want [%s]", got, src)
	}

	if marks[0] == id || marks[0] == w.tombIDs()[0] {
		t.Errorf("the mark %s reuses another record's ID", marks[0])
	}
}

type markedClaimTest struct {
	Name   string
	Args   []string
	Status int
}

var markedClaimTests = []markedClaimTest{
	{Name: "the same keys", Args: []string{"--scope", "src"}, Status: exitcode.Broken},
	{Name: "a wider claim", Args: nil, Status: exitcode.Broken},
	{Name: "a narrower claim", Args: []string{"--scope", "src/api"}, Status: exitcode.Broken},
	{Name: "a claim elsewhere", Args: []string{"--scope", "docs"}, Status: exitcode.OK},
}

func TestAMarkRefusesTheMarkedHoldersClaims(t *testing.T) {
	for _, test := range markedClaimTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			repo := w.dir("repo")

			w.dir("repo", "src", "api")
			w.dir("repo", "docs")

			holder := w.claude(200, "s1")
			id := w.acquireOK(holder, "--checkout", repo, "--scope", "src", "--name", "alpha")

			wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

			w.sleep = func(d time.Duration) { t.Fatalf("a refused claim waited %v", d) }

			args := append([]string{"acquire", "--checkout", repo, "--wait", "1h"}, test.Args...)
			res := w.run(holder, args...)

			wantStatus(t, res, test.Status)

			if test.Status == exitcode.OK {
				return
			}

			wantContains(t, "stderr", res.Stderr, "lease "+id, "was broken by an anonymous process", "fence mark",
				"Report this to the user")
			wantAbsent(t, "stderr", res.Stderr, "--force", "nitipan break", "nitipan request", "release")
		})
	}
}

func TestAMarkRefusesReplaceAndRequest(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")

		w.dir("repo", "src")
		w.dir("repo", "docs")

		holder := w.claude(200, "s1")
		srcID := w.acquireOK(holder, "--checkout", repo, "--scope", "src")
		docsID := w.acquireOK(holder, "--checkout", repo, "--scope", "docs")

		wantStatus(t, w.run(w.terminal(900), "break", "--force", srcID), exitcode.OK)

		res := w.run(holder, "acquire", "--checkout", repo, "--scope", "src", "--scope", "docs", "--replace", docsID)
		wantStatus(t, res, exitcode.Broken)
		wantContains(t, "stderr", res.Stderr, "fence mark")

		if w.leaseByID(docsID) == nil {
			t.Error("the refused replace removed the lease")
		}
	})

	t.Run("request", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")

		w.dir("repo", "src")

		parent := w.claude(300, "p")
		requester := w.claude(200, "s1")
		parentID := w.acquireOK(parent, "--checkout", repo)

		// A mark of a newer minor is never rewritten by this binary, so the parent's cover cannot
		// lift it before the request meets it.
		w.plantMark(markSpec{PID: 200, Keys: []string{filepath.Join(repo, "src")}, Version: "1.4"})

		res := w.run(requester, "request", parentID, "--checkout", repo, "--scope", "src")
		wantStatus(t, res, exitcode.Broken)
		wantContains(t, "stderr", res.Stderr, "fence mark")

		if got := w.fileNames(state.RequestsDir); len(got) != 0 {
			t.Errorf("the refused request left %v", got)
		}
	})
}

func TestAMarkLiftsPerKeyWhenAnotherHoldersRecordCoversIt(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")
	docs := w.dir("repo", "docs")

	holder := w.claude(200, "s1")
	breaker := w.claude(300, "s2")
	third := w.claude(400, "s3")

	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src", "--scope", "docs")

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	marks := w.markIDs()
	if len(marks) != 1 || len(w.markKeys(marks[0])) != 2 {
		t.Fatalf("marks = %v", marks)
	}

	w.acquireOK(breaker, "--checkout", repo, "--scope", "src")

	if got := w.markKeys(marks[0]); len(got) != 1 || got[0] != docs {
		t.Fatalf("after the breaker claimed src, the mark guards %v, want [%s]", got, docs)
	}

	w.acquireOK(third, "--checkout", repo, "--scope", "docs")

	if got := w.markIDs(); len(got) != 0 {
		t.Fatalf("the mark stayed after both keys were claimed: %v", got)
	}

	res := w.run(holder, "acquire", "--checkout", repo, "--scope", "src")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, src)
}

func TestAMarkLiftsLazilyAtTheNextLockedCall(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")

	w.liveHolder()

	holder := w.claude(200, "s1")
	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src")

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	if len(w.markIDs()) != 1 {
		t.Fatalf("marks = %v", w.markIDs())
	}

	w.plant(plantSpec{PID: plantedPID, Paths: []string{src}})

	wantStatus(t, w.run(w.terminal(901), "list"), exitcode.OK)

	if got := w.markIDs(); len(got) != 0 {
		t.Errorf("a record that covers the key left the mark in place: %v", got)
	}
}

func TestADeadHoldersMarkIsReapedByTheNextLockHolder(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")

	holder := w.claude(200, "s1")
	id := w.acquireOK(holder, "--checkout", repo)

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	if len(w.markIDs()) != 1 {
		t.Fatalf("marks = %v", w.markIDs())
	}

	w.kill(200)

	wantStatus(t, w.run(w.terminal(901), "list"), exitcode.OK)

	if got := w.markIDs(); len(got) != 0 {
		t.Errorf("the dead holder's mark survived: %v", got)
	}
}

func TestBreakRemovesAMarkWithoutForceButNotForTheHolderItGuardsAgainst(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")

	holder := w.claude(200, "s1")
	other := w.claude(300, "s2")
	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src")

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	mark := w.markIDs()[0]

	res := w.run(holder, "break", mark)
	wantStatus(t, res, exitcode.NotYours)
	wantContains(t, "stderr", res.Stderr, "guards against you")

	if len(w.markIDs()) != 1 {
		t.Fatal("the guarded holder removed its own mark")
	}

	res = w.run(other, "break", mark)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "removed fence mark "+mark)

	if len(w.markIDs()) != 0 {
		t.Fatal("the mark is still there")
	}

	wantStatus(t, w.run(holder, "acquire", "--checkout", repo, "--scope", "src"), exitcode.OK)

	_ = src
}

func TestBreakNeedsForceForAMarkItCannotUse(t *testing.T) {
	w := newWorld(t)
	id := state.NewID()

	w.writeMarkFile(id+".json", []byte(`{"version":"2.0","id":"`+id+`"}`))

	res := w.run(w.terminal(900), "break", id)
	wantStatus(t, res, exitcode.Unusable)
	wantContains(t, "stderr", res.Stderr, "fence mark", "cannot be used")

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	if len(w.markIDs()) != 0 {
		t.Error("the unusable mark survived --force")
	}
}

func TestListShowsFenceMarks(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")

	holder := w.claude(200, "s1")
	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src", "--name", "alpha")

	if doc := w.listJSON(w.terminal(900)); doc["schema_version"] != "1.4" || len(marksOf(doc)) != 0 {
		t.Fatalf("before the break: schema %v, marks %v", doc["schema_version"], doc["marks"])
	}

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	mark := w.markIDs()[0]

	marks := marksOf(w.listJSON(w.terminal(900)))
	if len(marks) != 1 {
		t.Fatalf("marks = %v", marks)
	}

	got := marks[0]
	if keys, _ := got["keys"].([]any); got["id"] != mark || got["lease"] != id || got["usable"] != true ||
		len(keys) != 1 || keys[0] != src {
		t.Errorf("mark in the document = %v", got)
	}

	text := w.run(w.terminal(900), "list")
	wantStatus(t, text, exitcode.OK)
	wantContains(t, "stdout", text.Stdout, mark+"  fence mark, holder alive", "guards: "+src, "was broken by an anonymous process")

	elsewhere := w.listJSON(w.terminal(900), "--checkout", w.dir("other"))
	if len(marksOf(elsewhere)) != 0 {
		t.Errorf("a filter that misses the keys still lists the mark: %v", elsewhere["marks"])
	}

	if res := w.run(w.terminal(900), "list", "--resource", "cpu:heavy"); strings.Contains(res.Stdout, "fence mark") {
		t.Errorf("a resource filter lists a mark:\n%s", res.Stdout)
	}
}

func TestADeliveredTombstoneFromAnOlderBinaryGetsAMark(t *testing.T) {
	deliveries := []struct {
		Name string
		Run  func(w *world, c caller) result
	}{
		{Name: "list", Run: func(w *world, c caller) result { return w.run(c, "list") }},
		{Name: "acquire", Run: func(w *world, c caller) result {
			return w.run(c, "acquire", "--checkout", w.dir("other"))
		}},
	}

	for _, delivery := range deliveries {
		t.Run(delivery.Name, func(t *testing.T) {
			w := newWorld(t)
			src := w.dir("repo", "src")
			holder := w.claude(200, "s1")

			w.plantTombstone(tombSpec{PID: 200, Lease: absentID, Paths: []string{src}})

			wantStatus(t, delivery.Run(w, holder), exitcode.OK)

			marks := w.markIDs()
			if len(marks) != 1 || len(w.tombIDs()) != 0 {
				t.Fatalf("marks = %v, tombstones = %v", marks, w.tombIDs())
			}

			if got := w.markKeys(marks[0]); len(got) != 1 || got[0] != src {
				t.Errorf("mark keys = %v", got)
			}

			if w.markDoc(marks[0])["lease"] != absentID {
				t.Errorf("mark = %v", w.markDoc(marks[0]))
			}

			w.plantTombstone(tombSpec{PID: 200, Lease: absentID, Paths: []string{src}})
			wantStatus(t, delivery.Run(w, holder), exitcode.OK)

			if got := w.markIDs(); len(got) != 1 {
				t.Errorf("a second delivery of the same break wrote %v", got)
			}
		})
	}
}

func TestEndsThatNeedNoMarkWriteNone(t *testing.T) {
	t.Run("a dead holder's lease", func(t *testing.T) {
		w := newWorld(t)
		holder := w.claude(200, "s1")
		id := w.acquireOK(holder, "--checkout", w.dir("repo"))

		w.kill(200)
		wantStatus(t, w.run(w.terminal(900), "break", id), exitcode.OK)

		if len(w.markIDs()) != 0 {
			t.Errorf("marks = %v", w.markIDs())
		}
	})

	t.Run("a resource lease", func(t *testing.T) {
		w := newWorld(t)
		holder := w.claude(200, "s1")
		id := w.acquireOK(holder, "--resource", "cpu:heavy")

		wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

		if len(w.markIDs()) != 0 || len(w.tombIDs()) != 1 {
			t.Errorf("marks = %v, tombstones = %v", w.markIDs(), w.tombIDs())
		}

		wantStatus(t, w.run(holder, "list"), exitcode.OK)

		if len(w.markIDs()) != 0 {
			t.Errorf("delivering the resource tombstone wrote %v", w.markIDs())
		}
	})

	t.Run("a revoked sub-lease", func(t *testing.T) {
		tr := newTree(t)

		wantStatus(t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)
		wantStatus(t, tr.w.run(tr.child, "list"), exitcode.OK)

		if len(tr.w.markIDs()) != 0 {
			t.Errorf("marks = %v", tr.w.markIDs())
		}
	})

	t.Run("a sub-lease whose keys went back to a live parent", func(t *testing.T) {
		tr := newTree(t)

		wantStatus(t, tr.w.run(tr.third, "break", "--force", tr.childID), exitcode.OK)

		if len(tr.w.markIDs()) != 0 {
			t.Errorf("marks = %v", tr.w.markIDs())
		}
	})
}

func TestAGrantToTheMarkedHolderCoversAndLiftsItsMark(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	src := w.dir("repo", "src")
	parent := w.claude(300, "p")
	requester := w.claude(200, "s1")
	parentID := w.acquireOK(parent, "--checkout", repo)

	reqID := w.requestOK(requester, parentID, "--checkout", repo, "--scope", "src")

	w.plantMark(markSpec{PID: 200, Keys: []string{src}})

	wantStatus(t, w.run(parent, "grant", reqID), exitcode.OK)

	if got := w.markIDs(); len(got) != 0 {
		t.Errorf("the parent's grant left the mark: %v", got)
	}
}

type judgeTest struct {
	Name     string
	Caller   string
	Paths    []string
	Want     []lease.RefusalKind
	Contains []string
	Absent   []string
}

func TestJudgeRefusalKinds(t *testing.T) {
	for _, test := range judgeTests {
		t.Run(test.Name, func(t *testing.T) {
			f := newJudgeFixture(t)
			c := f.callers[test.Caller]

			if test.Name == "a tombstone addressed to the caller" || test.Name == "a fence mark addressed to the caller" ||
				test.Name == "a fence mark of a binary that cannot rewrite it" {
				f.breakA(test.Name)
			}

			got := f.w.refusals(c, f.repo, test.Paths...)

			if len(got) != len(test.Want) {
				t.Fatalf("refusals = %+v, want kinds %v", got, test.Want)
			}

			for i, r := range got {
				if r.Kind != test.Want[i] {
					t.Errorf("refusal %d is kind %d, want %d: %s", i, r.Kind, test.Want[i], r.Message)
				}

				wantContains(t, "message", r.Message, test.Contains...)
				wantAbsent(t, "message", r.Message, test.Absent...)
			}
		})
	}
}

// judgeFixture is two live holders in one repo. alpha holds src and beta holds docs.
type judgeFixture struct {
	w       *world
	repo    string
	callers map[string]caller
	alphaID string
	betaID  string
}

func newJudgeFixture(t *testing.T) *judgeFixture {
	t.Helper()

	w := newWorld(t)
	f := &judgeFixture{w: w, repo: w.dir("repo"), callers: map[string]caller{}}

	w.dir("repo", "src")
	w.dir("repo", "docs")
	w.dir("repo", "lib")

	f.callers["alpha"] = w.claude(200, "s1")
	f.callers["beta"] = w.claude(300, "s2")
	f.callers["stranger"] = w.claude(400, "s3")
	f.alphaID = w.acquireOK(f.callers["alpha"], "--checkout", f.repo, "--scope", "src", "--name", "alpha-agent")
	f.betaID = w.acquireOK(f.callers["beta"], "--checkout", f.repo, "--scope", "docs", "--name", "beta-agent")

	return f
}

// breakA breaks alpha's lease as the user would. The tombstone case also removes the mark, as an
// older binary's break would have left none. The mark case delivers the tombstone to alpha.
func (f *judgeFixture) breakA(kind string) {
	f.w.t.Helper()

	wantStatus(f.w.t, f.w.run(f.w.terminal(900), "break", "--force", f.alphaID), exitcode.OK)

	switch kind {
	case "a tombstone addressed to the caller":
		for _, id := range f.w.markIDs() {
			if err := os.Remove(filepath.Join(f.w.marksDir(), id+".json")); err != nil {
				f.w.t.Fatal(err)
			}
		}
	case "a fence mark addressed to the caller":
		wantStatus(f.w.t, f.w.run(f.callers["alpha"], "list"), exitcode.OK)
	case "a fence mark of a binary that cannot rewrite it":
		wantStatus(f.w.t, f.w.run(f.callers["alpha"], "list"), exitcode.OK)

		for _, id := range f.w.markIDs() {
			f.w.editJSON(filepath.Join(f.w.marksDir(), id+".json"), func(doc map[string]any) { doc["version"] = "1.4" })
		}
	}
}

var judgeTests = []judgeTest{
	{Name: "the owner's own paths pass", Caller: "alpha", Paths: []string{"src/a.go"}},
	{Name: "paths no lease covers pass", Caller: "alpha", Paths: []string{"lib/x.go", "README"}},
	{
		Name: "another holder's live record", Caller: "beta", Paths: []string{"src/a.go", "src/b.go"},
		Want:     []lease.RefusalKind{lease.RefusedHeld},
		Contains: []string{"src/a.go, ", "src/b.go", "alpha-agent", "Coordinate with the holder or ask the user", "nitipan request"},
		Absent:   []string{"--force", "break"},
	},
	{
		Name: "one cause covers every path it refuses", Caller: "stranger", Paths: []string{"src/a.go", "docs/b.go"},
		Want:     []lease.RefusalKind{lease.RefusedHeld, lease.RefusedHeld},
		Contains: []string{"-agent"},
	},
	{
		Name: "a tombstone addressed to the caller", Caller: "alpha", Paths: []string{"src/a.go", "lib/x.go"},
		Want:     []lease.RefusalKind{lease.RefusedTombstone},
		Contains: []string{"src/a.go", "was broken by an anonymous process", "no longer exists", "Report this to the user"},
		Absent:   []string{"acquire", "request", "nitipan break", "--force"},
	},
	{
		Name: "a fence mark addressed to the caller", Caller: "alpha", Paths: []string{"src/a.go", "lib/x.go"},
		Want:     []lease.RefusalKind{lease.RefusedMark},
		Contains: []string{"src/a.go", "was broken by an anonymous process", "fence mark", "Report this to the user"},
		Absent:   []string{"acquire", "request", "nitipan break", "--force"},
	},
	{
		Name: "a fence mark of a binary that cannot rewrite it", Caller: "alpha", Paths: []string{"src/a.go"},
		Want: []lease.RefusalKind{lease.RefusedMark},
	},
	{
		Name: "the breaker is not held by the mark", Caller: "stranger", Paths: []string{"lib/x.go"},
	},
}

func TestJudgeKindsThatNeedPlantedState(t *testing.T) {
	t.Run("a holder whose liveness is unknown", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")

		w.dir("repo", "src")
		w.plant(plantSpec{PID: 77, Paths: []string{filepath.Join(repo, "src")}, Name: "away", Machine: otherMachine})

		got := w.refusals(w.claude(200, "s1"), repo, "src/a.go")
		if kinds := refusalKinds(got); len(kinds) != 1 || kinds[0] != lease.RefusedUnknown {
			t.Fatalf("refusals = %+v", got)
		}

		wantContains(t, "message", got[0].Message, "away", "cannot tell whether that holder is alive", "Ask the user")
		wantAbsent(t, "message", got[0].Message, "nitipan request")
	})

	t.Run("a dead holder's record does not refuse", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")

		w.dir("repo", "src")
		w.plant(plantSpec{PID: 77, Paths: []string{filepath.Join(repo, "src")}, KeepAfterDeath: true})

		if got := w.refusals(w.claude(200, "s1"), repo, "src/a.go"); len(got) != 0 {
			t.Errorf("refusals = %+v", got)
		}
	})

	t.Run("a record this nitipan cannot use", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")

		w.dir("repo", "src")
		w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

		got := w.refusals(w.claude(200, "s1"), repo, "src/a.go")
		if kinds := refusalKinds(got); len(kinds) != 1 || kinds[0] != lease.RefusedUnusable {
			t.Fatalf("refusals = %+v", got)
		}

		wantContains(t, "message", got[0].Message, "01JAAAAAAAAAAAAAAAAAAAAAAA.json", "cannot be used", "ask the user")
	})

	t.Run("a mark that cannot be used refuses the keys it names", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		src := w.dir("repo", "src")
		id := state.NewID()

		w.writeMarkFile(id+".json", []byte(`{"version":"2.0","id":"`+id+`","keys":[{"path":"`+src+`"}]}`))

		got := w.refusals(w.claude(200, "s1"), repo, "src/a.go", "docs/b.go")
		if kinds := refusalKinds(got); len(kinds) != 1 || kinds[0] != lease.RefusedUnusable {
			t.Fatalf("refusals = %+v", got)
		}

		wantContains(t, "message", got[0].Message, "src/a.go", id, "ask the user")
		wantAbsent(t, "message", got[0].Message, "docs/b.go")
	})

	t.Run("a mark whose keys cannot be read is ignored", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		id := state.NewID()

		w.writeMarkFile(id+".json", []byte(`{"version":"2.0","id":"`+id+`"}`))

		j := w.judgeFor(w.claude(200, "s1"))

		if got := j.Refusals(keys.Key{Path: repo}, []keys.Key{{Path: filepath.Join(repo, "src/a.go")}}); len(got) != 0 {
			t.Errorf("refusals = %+v", got)
		}

		warnings := j.Warnings()
		if len(warnings) != 1 || !strings.Contains(warnings[0], id) || !strings.Contains(warnings[0], "guards nothing") {
			t.Errorf("warnings = %v", warnings)
		}
	})
}

type treeJudgeTest struct {
	Name   string
	Setup  func(tr *tree)
	Who    string
	Paths  []string
	Want   []lease.RefusalKind
	Holder string
}

var treeJudgeTests = []treeJudgeTest{
	{Name: "a parent on keys it delegated", Who: "parent", Paths: []string{"src/api/x.go"},
		Want: []lease.RefusalKind{lease.RefusedHeld}, Holder: "child-agent"},
	{Name: "the sub-lessee on its own keys", Who: "child", Paths: []string{"src/api/x.go"}},
	{Name: "the sub-lessee outside its keys", Who: "child", Paths: []string{"lib/y.go"},
		Want: []lease.RefusalKind{lease.RefusedHeld}, Holder: "parent-agent"},
	{Name: "the parent on keys it kept", Who: "parent", Paths: []string{"lib/y.go"}},
	{
		Name: "a revoked sub-lessee before its tombstone is delivered",
		Setup: func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)
		},
		Who: "child", Paths: []string{"src/api/x.go"}, Want: []lease.RefusalKind{lease.RefusedTombstone},
	},
	{
		Name: "a revoked sub-lessee after its tombstone is delivered",
		Setup: func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)
			wantStatus(tr.w.t, tr.w.run(tr.child, "list"), exitcode.OK)
		},
		Who: "child", Paths: []string{"src/api/x.go"}, Want: []lease.RefusalKind{lease.RefusedHeld}, Holder: "parent-agent",
	},
	{
		Name: "the parent on the returned keys of a revoked sub-lease",
		Setup: func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)
		},
		Who: "parent", Paths: []string{"src/api/x.go"},
	},
	{
		Name: "a child ended by an older binary is refused through the parent's record",
		Setup: func(tr *tree) {
			if err := os.Remove(filepath.Join(tr.w.leasesDir(), tr.childID+".json")); err != nil {
				tr.w.t.Fatal(err)
			}
		},
		Who: "child", Paths: []string{"src/api/x.go"}, Want: []lease.RefusalKind{lease.RefusedHeld}, Holder: "parent-agent",
	},
	{
		Name: "the parent on the keys a child left behind",
		Setup: func(tr *tree) {
			if err := os.Remove(filepath.Join(tr.w.leasesDir(), tr.childID+".json")); err != nil {
				tr.w.t.Fatal(err)
			}
		},
		Who: "parent", Paths: []string{"src/api/x.go"},
	},
	{
		Name: "a child that survives its parent keeps its keys",
		Setup: func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.third, "break", "--force", tr.parentID), exitcode.OK)
		},
		Who: "child", Paths: []string{"src/api/x.go"},
	},
}

func TestSubLeaseOutcomesHoldThroughOwnership(t *testing.T) {
	for _, test := range treeJudgeTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			if test.Setup != nil {
				test.Setup(tr)
			}

			got := tr.w.refusals(tr.actor(test.Who), tr.repo, test.Paths...)
			if kinds := refusalKinds(got); !equalKinds(kinds, test.Want) {
				t.Fatalf("refusals = %+v, want kinds %v", got, test.Want)
			}

			if test.Holder != "" {
				wantContains(t, "message", got[0].Message, test.Holder)
			}
		})
	}
}

func equalKinds(a, b []lease.RefusalKind) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

type refTransactionTest struct {
	Name       string
	Args       []string
	Stdin      string
	Caller     func(w *world) caller
	WantStatus int
	WantErr    string
}

const (
	zeroID   = "0000000000000000000000000000000000000000"
	someID   = "1111111111111111111111111111111111111111"
	branchTx = zeroID + " " + someID + " refs/heads/main\n"
)

var refTransactionTests = []refTransactionTest{
	{
		Name: "a committed transaction is never judged", Args: []string{"committed"}, Stdin: branchTx,
		Caller: func(w *world) caller { return w.stray(900) },
	},
	{
		Name: "an aborted transaction is never judged", Args: []string{"aborted"}, Stdin: branchTx,
		Caller: func(w *world) caller { return w.stray(900) },
	},
	{
		Name: "refs outside branches and HEAD pass without asking who the caller is", Args: []string{"prepared"},
		Stdin:  zeroID + " " + someID + " refs/tags/v1\n" + zeroID + " " + someID + " ORIG_HEAD\n" + someID + " ref:refs/heads/x HEAD\n",
		Caller: func(w *world) caller { return w.stray(900) },
	},
	{
		Name: "an empty transaction passes", Args: []string{"prepared"},
		Caller: func(w *world) caller { return w.stray(900) },
	},
	{
		Name: "the user's own terminal passes", Args: []string{"prepared"}, Stdin: branchTx,
		Caller: func(w *world) caller { return w.terminal(900) },
	},
	{
		Name: "agent variables with no matching ancestor fail closed", Args: []string{"prepared"}, Stdin: branchTx,
		Caller:     func(w *world) caller { return w.stray(900) },
		WantStatus: exitcode.Error,
		WantErr:    "could not identify the calling agent",
	},
	{
		Name: "a transaction git cannot be parsed fails closed", Args: []string{"prepared"}, Stdin: "nonsense\n",
		Caller:     func(w *world) caller { return w.terminal(900) },
		WantStatus: exitcode.Error,
		WantErr:    "could not read it",
	},
	{
		Name: "a missing state argument is a usage error", Args: nil,
		Caller:     func(w *world) caller { return w.terminal(900) },
		WantStatus: exitcode.Usage,
		WantErr:    "unrecognized arguments",
	},
}

func TestReferenceTransactionCommand(t *testing.T) {
	for _, test := range refTransactionTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.stdin = test.Stdin

			res := w.run(test.Caller(w), append([]string{"hook", "reference-transaction"}, test.Args...)...)

			wantStatus(t, res, test.WantStatus)
			wantContains(t, "stderr", res.Stderr, test.WantErr)
			wantAbsent(t, "stderr", res.Stderr, "nitipan hook")
		})
	}
}

// gitRepo makes a repository the acquire check can recognize.
func (w *world) gitRepo(name string) string {
	w.t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		w.t.Skip("git is not installed")
	}

	dir := w.dir(name)

	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")

	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git init: %v\n%s", err, out)
	}

	return dir
}

func (w *world) gitIn(dir string, args ...string) {
	w.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestAPathClaimInAGitCheckoutNeedsTheHooksInForce(t *testing.T) {
	w := newWorld(t)
	repo := w.gitRepo("repo")
	elsewhere := w.gitRepo("elsewhere")
	user := w.terminal(900)
	agent := w.claude(200, "s1")

	w.dir("repo", "src")

	res := w.run(agent, "acquire", "--checkout", repo, "--scope", "src")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, repo, "git hooks are not in force", "no reference-transaction hook",
		"nitipan setup git --install "+repo)

	if len(w.leases()) != 0 {
		t.Fatal("the refused claim published a lease")
	}

	installed := w.run(user, "setup", "git", "--install", repo)
	wantStatus(t, installed, exitcode.OK)
	wantContains(t, "stdout", installed.Stdout, "wrote "+filepath.Join(repo, ".git", "hooks", "reference-transaction"))

	w.acquireOK(agent, "--checkout", repo, "--scope", "src")

	t.Run("another checkout is checked on its own", func(t *testing.T) {
		res := w.run(agent, "acquire", "--checkout", elsewhere)
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, elsewhere)
	})

	t.Run("a resource claim and a path outside a checkout are unaffected", func(t *testing.T) {
		w.acquireOK(agent, "--resource", "cpu:heavy")
		w.acquireOK(agent, "--checkout", w.dir("plain"))
	})

	t.Run("a refresh is checked again", func(t *testing.T) {
		wantStatus(t, w.run(agent, "acquire", "--checkout", repo, "--scope", "src"), exitcode.OK)

		w.gitIn(repo, "config", "--local", "core.hooksPath", filepath.Join(w.root, "elsewhere-hooks"))

		res := w.run(agent, "acquire", "--checkout", repo, "--scope", "src")
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, "git hooks are not in force")

		w.gitIn(repo, "config", "--local", "--unset", "core.hooksPath")
		wantStatus(t, w.run(agent, "acquire", "--checkout", repo, "--scope", "src"), exitcode.OK)
	})

	t.Run("uninstall puts the requirement back", func(t *testing.T) {
		wantStatus(t, w.run(user, "setup", "git", "--uninstall", repo), exitcode.OK)

		res := w.run(w.claude(300, "s2"), "acquire", "--checkout", repo, "--scope", "docs")
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, "git hooks are not in force")
	})
}

func TestBreakForceSaysWhetherTheBrokenLeasesCheckoutHadTheHooks(t *testing.T) {
	w := newWorld(t)
	repo := w.gitRepo("repo")
	plain := w.dir("plain")
	user := w.terminal(900)

	wantStatus(t, w.run(user, "setup", "git", "--install", repo), exitcode.OK)

	inRepo := w.acquireOK(w.claude(200, "s1"), "--checkout", repo)
	outside := w.acquireOK(w.claude(300, "s2"), "--checkout", plain)

	res := w.run(user, "break", "--force", inRepo)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "the git hook is in force in "+repo)

	res = w.run(user, "break", "--force", outside)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "not inside a git checkout")

	wantStatus(t, w.run(user, "setup", "git", "--uninstall", repo), exitcode.OK)

	// A lease can be planted in a checkout whose hooks are gone, as a tool that rewires them would
	// leave it.
	planted := w.plant(plantSpec{PID: plantedPID, Paths: []string{repo}})
	w.liveHolder()

	res = w.run(user, "break", "--force", planted)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "the git hook is not in force in "+repo, "are not fenced")
}

func TestSetupGitFlags(t *testing.T) {
	tests := []struct {
		Name    string
		Args    []string
		Status  int
		WantOut string
		WantErr string
	}{
		{Name: "no flag explains itself", Args: []string{"setup", "git"}, Status: exitcode.OK, WantOut: "nitipan setup git --install"},
		{
			Name: "both flags", Args: []string{"setup", "git", "--install", ".", "--uninstall", "."},
			Status: exitcode.Usage, WantErr: "not both",
		},
		{
			Name: "a flag with no checkout", Args: []string{"setup", "git", "--install", ""},
			Status: exitcode.Usage, WantErr: "need the checkout",
		},
		{
			Name: "outside a checkout", Args: []string{"setup", "git", "--install", "{plain}"},
			Status: exitcode.Error, WantErr: "not inside a git checkout",
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git is not installed")
			}

			args := make([]string, len(test.Args))
			for i, a := range test.Args {
				args[i] = strings.ReplaceAll(a, "{plain}", w.dir("plain"))
			}

			res := w.run(w.terminal(900), args...)
			wantStatus(t, res, test.Status)
			wantContains(t, "stdout", res.Stdout, test.WantOut)
			wantContains(t, "stderr", res.Stderr, test.WantErr)
		})
	}
}

func TestARemovedMarkIsNotWrittenAgainWhenItsTombstoneIsDelivered(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	holder := w.claude(200, "s1")
	id := w.acquireOK(holder, "--checkout", repo)

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)
	wantStatus(t, w.run(w.terminal(900), "break", w.markIDs()[0]), exitcode.OK)

	wantStatus(t, w.run(holder, "list"), exitcode.OK)

	if got := w.markIDs(); len(got) != 0 {
		t.Errorf("delivering the tombstone brought the removed mark back: %v", got)
	}
}

func TestALiftedMarkIsNotWrittenAgainWhenItsTombstoneIsDelivered(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	holder := w.claude(200, "s1")
	breaker := w.claude(300, "s2")
	id := w.acquireOK(holder, "--checkout", repo)

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	claimed := w.acquireOK(breaker, "--checkout", repo)
	if len(w.markIDs()) != 0 {
		t.Fatalf("the claim left the mark: %v", w.markIDs())
	}

	wantStatus(t, w.run(breaker, "release", claimed), exitcode.OK)
	wantStatus(t, w.run(holder, "list"), exitcode.OK)

	if got := w.markIDs(); len(got) != 0 {
		t.Errorf("delivering the tombstone brought the lifted mark back: %v", got)
	}
}

func TestAFailureToAskGitRefusesTheClaimAndSaysWhatFailed(t *testing.T) {
	tests := []struct {
		Name   string
		Err    error
		Stderr string
		Status int
		Want   string
	}{
		{Name: "not a repository", Err: errors.New("exit status 128"), Stderr: "fatal: not a git repository", Status: exitcode.OK},
		{Name: "no git binary", Err: exec.ErrNotFound, Status: exitcode.OK},
		{
			Name: "an unsafe directory", Err: errors.New("exit status 128"), Stderr: "fatal: detected dubious ownership",
			Status: exitcode.Error, Want: "dubious ownership",
		},
		{Name: "a timeout", Err: context.DeadlineExceeded, Status: exitcode.Error, Want: "deadline exceeded"},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			repo := w.dir("repo")

			w.git = func(string, []byte, ...string) ([]byte, error) {
				return nil, &gitfence.GitError{Err: test.Err, Stderr: test.Stderr}
			}

			res := w.run(w.claude(200, "s1"), "acquire", "--checkout", repo)
			wantStatus(t, res, test.Status)
			wantContains(t, "stderr", res.Stderr, test.Want)
		})
	}
}

func TestAForceBreakWhoseFenceMarkCannotBeWrittenLeavesTheLeaseAndNoTombstone(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")
	holder := w.claude(200, "s1")

	id := w.acquireOK(holder, "--checkout", repo, "--scope", "src", "--name", "alpha")

	w.hooks = lease.Hooks{BeforeBreakMark: func() error { return errKilled }}
	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.Error)
	w.hooks = lease.Hooks{}

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != id {
		t.Errorf("leases = %v, want the lease to stand", ids)
	}

	if tombs, marks := w.tombIDs(), w.markIDs(); len(tombs) != 0 || len(marks) != 0 {
		t.Errorf("tombstones = %v, marks = %v, want none of either", tombs, marks)
	}

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	if len(w.tombIDs()) != 1 || len(w.markIDs()) != 1 {
		t.Errorf("tombstones = %v, marks = %v, want one of each after the retry", w.tombIDs(), w.markIDs())
	}
}
