package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/state"
)

const (
	unusableRecord = `{"version":"2.0","id":"01JAAAAAAAAAAAAAAAAAAAAAAA"}`
	absentID       = "01JZZZZZZZZZZZZZZZZZZZZZZZ"
)

func (w *world) tombDir() string {
	return filepath.Join(w.state, state.TombstonesDir)
}

// tombIDs lists the published tombstone files, by ID.
func (w *world) tombIDs() []string {
	w.t.Helper()

	entries, err := os.ReadDir(w.tombDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		w.t.Fatal(err)
	}

	var out []string

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}

	return out
}

// tombDoc decodes a published tombstone without going through the record package.
func (w *world) tombDoc(id string) map[string]any {
	w.t.Helper()

	data, err := os.ReadFile(filepath.Join(w.tombDir(), id+".json"))
	if err != nil {
		w.t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		w.t.Fatal(err)
	}

	return doc
}

func (w *world) writeTombFile(name string, data []byte) {
	w.t.Helper()

	if err := os.MkdirAll(w.tombDir(), 0o700); err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(w.tombDir(), name), data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// tombSpec describes a tombstone written straight into the state directory.
type tombSpec struct {
	ID    string
	Lease string

	// PID is the broken holder, an agent named claude.
	PID int

	// KeepAfterDeath is true to write clear_when_dead false.
	KeepAfterDeath bool

	// Paths are the paths the tombstone lists.
	Paths []string
}

func (w *world) plantTombstone(spec tombSpec) string {
	w.t.Helper()

	if spec.ID == "" {
		spec.ID = state.NewID()
	}

	if spec.Lease == "" {
		spec.Lease = absentID
	}

	doc := map[string]any{
		"version": "1.0", "id": spec.ID, "footprint": "none", "clear_when_dead": !spec.KeepAfterDeath,
		"liveness": "process", "machine": map[string]any{"host": testMachine.Host, "id": testMachine.ID},
		"holder": map[string]any{
			"agent": "claude", "pid": spec.PID, "start": map[string]any{"platform": "darwin", "sec": spec.PID},
		},
		"lease":     spec.Lease,
		"broken_by": map[string]any{"agent": "claude", "pid": 300},
		"broken_at": "2026-10-20T11:00:00Z",
	}

	if len(spec.Paths) > 0 {
		doc["paths"] = spec.Paths
	}

	data, err := json.Marshal(doc)
	if err != nil {
		w.t.Fatal(err)
	}

	w.writeTombFile(spec.ID+".json", data)

	return spec.ID
}

// plantedPID is the holder of every record and tombstone the tests plant.
const plantedPID = 77

// liveHolder adds the planted holder's process, so what it holds reads alive.
func (w *world) liveHolder() {
	w.table.Add(plantedPID, proctest.Process{Start: startOf(plantedPID)})
}

type breakTest struct {
	Name string

	// Plant describes the lease. Live makes its holder alive.
	Plant plantSpec
	Live  bool

	// WantRefusal is what the refusal without --force must say. Empty means no flag is needed.
	WantRefusal []string

	// WantTombstone is true when --force leaves a tombstone.
	WantTombstone bool
}

var breakTests = []breakTest{
	{Name: "dead holder allowing clearing", Plant: plantSpec{PID: 77, Name: "owner"}},
	{
		Name: "dead holder that keeps its record", Plant: plantSpec{PID: 77, Name: "owner", KeepAfterDeath: true},
		WantRefusal: []string{"owner", "newer nitipan"},
	},
	{
		Name: "dead holder with a record of a higher minor", Plant: plantSpec{PID: 77, Name: "owner", Version: "1.5"},
		WantRefusal: []string{"owner", "newer nitipan"},
	},
	{
		Name: "live holder", Plant: plantSpec{PID: 77, Name: "owner"}, Live: true,
		WantRefusal: []string{"owner", "pid 77"}, WantTombstone: true,
	},
}

func TestBreakByLivenessAndForce(t *testing.T) {
	for _, test := range breakTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			if test.Live {
				w.liveHolder()
			}

			test.Plant.Paths = []string{w.dir("repo")}
			id := w.plant(test.Plant)
			breaker := w.terminal(900)

			if len(test.WantRefusal) > 0 {
				res := w.run(breaker, "break", id)
				wantStatus(t, res, exitcode.Refused)
				wantContains(t, "stderr", res.Stderr, test.WantRefusal...)
				wantAbsent(t, "stderr", res.Stderr, "--force")

				if w.leaseByID(id) == nil || len(w.tombIDs()) != 0 {
					t.Fatal("a refused break changed the state")
				}

				res = w.run(breaker, "break", "--force", id)
				wantStatus(t, res, exitcode.OK)
			} else {
				wantStatus(t, w.run(breaker, "break", id), exitcode.OK)
			}

			if w.leaseByID(id) != nil {
				t.Error("the lease survived the break")
			}

			if got := len(w.tombIDs()) == 1; got != test.WantTombstone {
				t.Errorf("tombstones = %v, want one: %v", w.tombIDs(), test.WantTombstone)
			}
		})
	}
}

func TestBreakForceLeavesATombstoneKeyedByTheBrokenHolderAndTheBreakerCanAcquire(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	holder := w.claude(200, "s1")
	breaker := w.claude(300, "s2")

	id := w.acquireOK(holder, "--checkout", repo, "--name", "alpha")

	res := w.run(breaker, "break", "--force", id)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "broken lease "+id)

	tombs := w.tombIDs()
	if len(tombs) != 1 {
		t.Fatalf("tombstones = %v, want one", tombs)
	}

	doc := w.tombDoc(tombs[0])
	by, _ := doc["broken_by"].(map[string]any)
	who, _ := doc["holder"].(map[string]any)

	if doc["lease"] != id || doc["broken_at"] != "2026-10-20T12:00:00Z" || doc["anonymous"] == true {
		t.Errorf("tombstone = %v", doc)
	}

	if who["pid"] != float64(200) || by["pid"] != float64(300) || by["handle"] == "" {
		t.Errorf("tombstone holder %v, breaker %v", who, by)
	}

	w.acquireOK(breaker, "--checkout", repo)
}

func TestBreakByACallerWithNoAdapterRecordsAnAnonymousProcess(t *testing.T) {
	w := newWorld(t)
	holder := w.claude(200, "s1")

	id := w.acquireOK(holder, "--checkout", w.dir("repo"))

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	tombs := w.tombIDs()
	if len(tombs) != 1 {
		t.Fatalf("tombstones = %v, want one", tombs)
	}

	if doc := w.tombDoc(tombs[0]); doc["anonymous"] != true || doc["broken_by"] == nil {
		t.Errorf("tombstone = %v", doc)
	}

	res := w.run(holder, "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "lease "+id+" on ", "was broken by an anonymous process")
}

type deliveryTest struct {
	Name       string
	Args       func(w *world, id string) []string
	WantStatus int
	WantNotice string
}

var deliveryTests = []deliveryTest{
	{
		Name: "acquire", WantStatus: exitcode.OK, WantNotice: "nitipan: notice: lease ",
		Args: func(w *world, _ string) []string { return []string{"acquire", "--checkout", w.dir("elsewhere")} },
	},
	{
		Name: "release", WantStatus: exitcode.Broken, WantNotice: "nitipan: notice: lease ",
		Args: func(_ *world, id string) []string { return []string{"release", id} },
	},
	{
		Name: "list", WantStatus: exitcode.OK, WantNotice: "nitipan: tombstone delivered: lease ",
		Args: func(*world, string) []string { return []string{"list"} },
	},
	{
		Name: "advertise", WantStatus: exitcode.NoSuchLease, WantNotice: "nitipan: notice: lease ",
		Args: func(*world, string) []string { return []string{"advertise", "--name", "renamed"} },
	},
	{
		Name: "break", WantStatus: exitcode.NoSuchLease, WantNotice: "nitipan: notice: lease ",
		Args: func(*world, string) []string { return []string{"break", absentID} },
	},
}

func TestEveryCommandDeliversTheTombstonesAddressedToItsCaller(t *testing.T) {
	for _, test := range deliveryTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			holder := w.claude(200, "s1")
			breaker := w.claude(300, "s2")

			id := w.acquireOK(holder, "--checkout", w.dir("repo"))
			wantStatus(t, w.run(breaker, "break", "--force", id), exitcode.OK)

			res := w.run(holder, test.Args(w, id)...)
			if res.Status != test.WantStatus {
				t.Fatalf("status = %d, want %d\nstderr: %s", res.Status, test.WantStatus, res.Stderr)
			}

			wantContains(t, "stderr", res.Stderr, test.WantNotice, id, "was broken by", "pid 300")

			if tombs := w.tombIDs(); len(tombs) != 0 {
				t.Errorf("tombstones after delivery = %v, want none", tombs)
			}

			again := w.run(holder, "list")
			wantAbsent(t, "stderr", again.Stderr, "was broken by")
		})
	}
}

type failedValidationTest struct {
	Name string
	Args []string
	Want string
}

var failedValidationTests = []failedValidationTest{
	{Name: "bad permission mode", Args: []string{"--permission-mode", "yolo"}, Want: `"yolo"`},
	{Name: "bad scope", Args: []string{"--scope", "../escape"}, Want: ".."},
}

func TestAnAcquireThatFailsValidationStillDeliversTheTombstones(t *testing.T) {
	for _, test := range failedValidationTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			holder := w.claude(200, "s1")
			breaker := w.claude(300, "s2")

			id := w.acquireOK(holder, "--checkout", w.dir("repo"))
			wantStatus(t, w.run(breaker, "break", "--force", id), exitcode.OK)

			args := append([]string{"acquire", "--checkout", w.dir("elsewhere")}, test.Args...)

			res := w.run(holder, args...)
			wantStatus(t, res, exitcode.Usage)
			wantContains(t, "stderr", res.Stderr, test.Want, "nitipan: notice: lease "+id, "was broken by", "pid 300")

			if tombs := w.tombIDs(); len(tombs) != 0 {
				t.Errorf("tombstones after delivery = %v, want none", tombs)
			}
		})
	}
}

func TestATombstoneForAnotherHolderIsLeftAlone(t *testing.T) {
	w := newWorld(t)
	w.liveHolder()

	id := w.plantTombstone(tombSpec{PID: 77})

	wantStatus(t, w.run(w.claude(200, "s1"), "list"), exitcode.OK)
	wantStatus(t, w.run(w.terminal(900), "list"), exitcode.OK)

	if tombs := w.tombIDs(); len(tombs) != 1 || tombs[0] != id {
		t.Errorf("tombstones = %v, want only %s", tombs, id)
	}
}

func tombstonesOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()

	raw, ok := doc["tombstones"].([]any)
	if !ok {
		t.Fatalf("tombstones = %v", doc["tombstones"])
	}

	out := make([]map[string]any, len(raw))

	for i, r := range raw {
		out[i], _ = r.(map[string]any)
	}

	return out
}

func TestListReportsATombstoneOfADeadHolderOnceThenRemovesIt(t *testing.T) {
	w := newWorld(t)
	id := w.plantTombstone(tombSpec{PID: 77})

	res := w.run(w.terminal(900), "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "nitipan: tombstone reaped: lease "+absentID)

	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatal(err)
	}

	tombs := tombstonesOf(t, doc)
	if len(tombs) != 1 || tombs[0]["id"] != id || tombs[0]["action"] != "reaped" || tombs[0]["lease"] != absentID {
		t.Errorf("tombstones = %v", tombs)
	}

	if left := w.tombIDs(); len(left) != 0 {
		t.Errorf("tombstones after the list = %v, want none", left)
	}

	if again := tombstonesOf(t, w.listJSON(w.terminal(900))); len(again) != 0 {
		t.Errorf("second list reported %v", again)
	}
}

func TestListKeepsATombstoneOfADeadHolderWhoseRecordAsksToStay(t *testing.T) {
	w := newWorld(t)
	w.plantTombstone(tombSpec{PID: 77, KeepAfterDeath: true})

	if got := tombstonesOf(t, w.listJSON(w.terminal(900))); len(got) != 0 {
		t.Errorf("reported %v", got)
	}

	if len(w.tombIDs()) != 1 {
		t.Error("list removed a tombstone that asks to stay")
	}
}

func TestListJSONIncludesTheTombstoneItDeliveredToItsCaller(t *testing.T) {
	w := newWorld(t)
	w.liveHolder()

	holder := w.claude(200, "s1")
	id := w.acquireOK(holder, "--checkout", w.dir("repo"))
	wantStatus(t, w.run(w.claude(300, "s2"), "break", "--force", id), exitcode.OK)

	tombs := tombstonesOf(t, w.listJSON(holder))
	if len(tombs) != 1 || tombs[0]["action"] != "delivered" || tombs[0]["lease"] != id || tombs[0]["broken_by"] == nil ||
		tombs[0]["broken_at"] == nil {
		t.Errorf("tombstones = %v", tombs)
	}

	if len(w.tombIDs()) != 0 {
		t.Error("the delivered tombstone survived")
	}
}

func TestAFailedTombstoneRemovalDoesNotCostListItsOutput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	w := newWorld(t)
	id := w.plantTombstone(tombSpec{PID: 77})

	if err := os.Chmod(w.tombDir(), 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(w.tombDir(), 0o700) })

	res := w.run(w.terminal(900), "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "warning", "removing the tombstone")

	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, res.Stdout)
	}

	if tombs := tombstonesOf(t, doc); len(tombs) != 1 || tombs[0]["id"] != id {
		t.Errorf("tombstones = %v", tombs)
	}
}

func TestBreakForceRemovesAnUnusableRecordWithNoTombstoneAndSaysSo(t *testing.T) {
	w := newWorld(t)
	w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

	res := w.run(w.terminal(900), "break", "01JAAAAAAAAAAAAAAAAAAAAAAA")
	wantStatus(t, res, exitcode.Unusable)
	wantAbsent(t, "stderr", res.Stderr, "--force")

	res = w.run(w.terminal(900), "break", "--force", "01JAAAAAAAAAAAAAAAAAAAAAAA")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "removed unusable lease", "no tombstone")

	if len(w.leases()) != 0 || len(w.tombIDs()) != 0 {
		t.Errorf("leases %v, tombstones %v", w.leaseIDs(), w.tombIDs())
	}
}

func TestReleaseOfAnUnusableRecordReportsIt(t *testing.T) {
	w := newWorld(t)
	w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

	res := w.run(w.claude(200, "s1"), "release", "01JAAAAAAAAAAAAAAAAAAAAAAA")
	wantStatus(t, res, exitcode.Unusable)
	wantContains(t, "stderr", res.Stderr, "unusable record")
}

func TestAnUnusableTombstoneNeverBlocksAClaimShowsAsUnknownAndBreaksWithForce(t *testing.T) {
	w := newWorld(t)
	badID := "01JBBBBBBBBBBBBBBBBBBBBBBB"
	w.writeTombFile(badID+".json", []byte(`{"version":"2.0","id":"`+badID+`"}`))

	holder := w.claude(200, "s1")
	w.acquireOK(holder, "--checkout", w.dir("repo"))

	res := w.run(holder, "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "nitipan: tombstone unknown:")

	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatal(err)
	}

	if tombs := tombstonesOf(t, doc); len(tombs) != 1 || tombs[0]["id"] != badID || tombs[0]["action"] != "unknown" {
		t.Errorf("tombstones = %v", tombs)
	}

	if len(w.tombIDs()) != 1 {
		t.Fatal("list removed a tombstone it cannot use")
	}

	res = w.run(w.terminal(900), "break", badID)
	wantStatus(t, res, exitcode.Unusable)
	wantAbsent(t, "stderr", res.Stderr, "--force")

	res = w.run(w.terminal(900), "break", "--force", badID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "tombstone "+badID)

	if len(w.tombIDs()) != 0 {
		t.Error("the unusable tombstone survived break --force")
	}
}

type unlockedListTest struct {
	Name  string
	Setup func(t *testing.T, w *world)
}

var unlockedListTests = []unlockedListTest{
	{Name: "lock busy", Setup: func(t *testing.T, w *world) {
		w.table.Add(4242, proctest.Process{Start: startOf(4242)})

		dir, err := state.Open(w.state, state.Options{
			Checker: &liveness.Checker{Procs: w.table, Local: testMachine}, Procs: w.table, Machine: testMachine,
			SelfPID: 4242, SelfStart: startOf(4242), Command: "nitipan acquire",
		})
		if err != nil {
			t.Fatal(err)
		}

		held, err := dir.Lock()
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = held.Unlock() })
	}},
	{Name: "sandbox blocked lock file", Setup: func(t *testing.T, w *world) {
		lock := filepath.Join(w.state, "lock")
		if err := os.Chmod(lock, 0); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })
	}},
	{Name: "format marker forbids writes", Setup: func(t *testing.T, w *world) {
		if err := os.WriteFile(filepath.Join(w.state, "format"), []byte("7"), 0o600); err != nil {
			t.Fatal(err)
		}
	}},
}

func TestListWithoutWriteAccessStillShowsUnusableTombstonesAndDeliversNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}

	for _, test := range unlockedListTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.liveHolder()

			holder := w.claude(200, "s1")
			wantStatus(t, w.run(holder, "list"), exitcode.OK)

			badID := "01JBBBBBBBBBBBBBBBBBBBBBBB"
			w.writeTombFile(badID+".json", []byte(`{"version":"2.0","id":"`+badID+`"}`))

			addressed := w.plantTombstone(tombSpec{PID: 200})
			reapable := w.plantTombstone(tombSpec{PID: 99})

			test.Setup(t, w)

			res := w.run(holder, "list", "--json")
			wantStatus(t, res, exitcode.OK)
			wantContains(t, "stderr", res.Stderr, "tombstones and notices are not delivered", "nitipan: tombstone unknown:")
			wantAbsent(t, "stderr", res.Stderr, "was broken by")

			var doc map[string]any
			if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
				t.Fatalf("stdout is not json: %v\n%s", err, res.Stdout)
			}

			if tombs := tombstonesOf(t, doc); len(tombs) != 1 || tombs[0]["id"] != badID || tombs[0]["action"] != "unknown" {
				t.Errorf("tombstones = %v, want only the unknown one", tombs)
			}

			if left := w.tombIDs(); len(left) != 3 {
				t.Errorf("tombstones left = %v, want %s, %s and %s", left, badID, addressed, reapable)
			}
		})
	}
}

func TestBreakWithARepeatedIDReportsItsFailureOnce(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "break", absentID, absentID, absentID)
	wantStatus(t, res, exitcode.NoSuchLease)

	if n := strings.Count(res.Stderr, "no such lease "+absentID); n != 1 {
		t.Errorf("the failure appears %d times:\n%s", n, res.Stderr)
	}

	live := newWorld(t)
	live.liveHolder()

	id := live.plant(plantSpec{PID: plantedPID, Paths: []string{live.dir("repo")}})

	res = live.run(live.terminal(900), "break", "--force", id, id)
	wantStatus(t, res, exitcode.OK)

	if n := strings.Count(res.Stdout, id); n != 1 {
		t.Errorf("the lease is reported %d times:\n%s", n, res.Stdout)
	}
}

func TestBreakOfAUsableTombstoneIsRefused(t *testing.T) {
	w := newWorld(t)
	w.liveHolder()

	id := w.plantTombstone(tombSpec{PID: 77})

	res := w.run(w.terminal(900), "break", "--force", id)
	wantStatus(t, res, exitcode.NoSuchLease)

	if len(w.tombIDs()) != 1 {
		t.Error("break removed a tombstone it could read")
	}
}

func TestBreakTakesLeaseIDsOnly(t *testing.T) {
	w := newWorld(t)
	w.writeLeaseFile("stray.json", []byte(`{}`))

	for _, arg := range []string{"stray.json", "stray", "../leases/x", w.dir("repo")} {
		res := w.run(w.terminal(900), "break", "--force", arg)
		wantStatus(t, res, exitcode.Usage)
	}

	if _, err := os.Stat(filepath.Join(w.leasesDir(), "stray.json")); err != nil {
		t.Errorf("the stray file was touched: %v", err)
	}

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Unusable)
	wantContains(t, "stderr", res.Stderr, "stray.json", "not an ID", "by hand")
}

func TestReleaseOfAnotherHoldersRecordOfANewerMinorIsNotYours(t *testing.T) {
	w := newWorld(t)
	w.liveHolder()

	id := w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}, Version: "1.5", Name: "owner"})

	res := w.run(w.claude(200, "s1"), "release", id)
	wantStatus(t, res, exitcode.NotYours)
	wantContains(t, "stderr", res.Stderr, "owner")

	if w.leaseByID(id) == nil {
		t.Error("another holder's record was released")
	}
}

type errorPathTest struct {
	Name string
	Run  func(w *world) result
}

var errorPathTests = []errorPathTest{
	{Name: "break with no ID", Run: func(w *world) result { return w.run(w.terminal(900), "break") }},
	{Name: "break with a path", Run: func(w *world) result { return w.run(w.terminal(900), "break", "/tmp/x") }},
	{Name: "break of an unknown ID", Run: func(w *world) result { return w.run(w.terminal(900), "break", absentID) }},
	{Name: "break of a live holder", Run: func(w *world) result {
		w.liveHolder()

		return w.run(w.terminal(900), "break", w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}}))
	}},
	{Name: "break of a record that must be kept", Run: func(w *world) result {
		id := w.plant(plantSpec{PID: 77, KeepAfterDeath: true, Paths: []string{w.dir("repo")}})

		return w.run(w.terminal(900), "break", id)
	}},
	{Name: "break of a record of a higher minor", Run: func(w *world) result {
		id := w.plant(plantSpec{PID: 77, Version: "1.5", Paths: []string{w.dir("repo")}})

		return w.run(w.terminal(900), "break", id)
	}},
	{Name: "break of an unusable record", Run: func(w *world) result {
		w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

		return w.run(w.terminal(900), "break", "01JAAAAAAAAAAAAAAAAAAAAAAA")
	}},
	{Name: "break of an unusable tombstone", Run: func(w *world) result {
		w.writeTombFile("01JBBBBBBBBBBBBBBBBBBBBBBB.json", []byte(`{"version":"2.0"}`))

		return w.run(w.terminal(900), "break", "01JBBBBBBBBBBBBBBBBBBBBBBB")
	}},
	{Name: "break of a readable tombstone", Run: func(w *world) result {
		w.liveHolder()

		return w.run(w.terminal(900), "break", w.plantTombstone(tombSpec{PID: 77}))
	}},
	{Name: "break of your own lease", Run: func(w *world) result {
		c := w.claude(200, "s1")

		return w.run(c, "break", w.acquireOK(c, "--checkout", w.dir("repo")))
	}},
	{Name: "acquire over a live lease", Run: func(w *world) result {
		w.liveHolder()
		w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}})

		return w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	}},
	{Name: "acquire over a record that must be kept", Run: func(w *world) result {
		w.plant(plantSpec{PID: 77, KeepAfterDeath: true, Paths: []string{w.dir("repo")}})

		return w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	}},
	{Name: "acquire beside an unusable record", Run: func(w *world) result {
		w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

		return w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	}},
	{Name: "acquire beside a stray file", Run: func(w *world) result {
		w.writeLeaseFile("stray.json", []byte(`{}`))

		return w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	}},
	{Name: "release of another holder's lease", Run: func(w *world) result {
		w.liveHolder()

		return w.run(w.claude(200, "s1"), "release", w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}}))
	}},
	{Name: "release with no adapter", Run: func(w *world) result {
		return w.run(w.terminal(900), "release", w.plant(plantSpec{PID: 77, Paths: []string{w.dir("repo")}}))
	}},
	{Name: "release of an unknown ID", Run: func(w *world) result { return w.run(w.claude(200, "s1"), "release", absentID) }},
	{Name: "release with no target", Run: func(w *world) result { return w.run(w.claude(200, "s1"), "release") }},
	{Name: "release of an unusable record", Run: func(w *world) result {
		w.writeLeaseFile("01JAAAAAAAAAAAAAAAAAAAAAAA.json", []byte(unusableRecord))

		return w.run(w.claude(200, "s1"), "release", "01JAAAAAAAAAAAAAAAAAAAAAAA")
	}},
	{Name: "advertise with no lease", Run: func(w *world) result {
		return w.run(w.claude(200, "s1"), "advertise", "--name", "x")
	}},
	{Name: "an unknown flag", Run: func(w *world) result { return w.run(w.terminal(900), "break", "--nope") }},
}

func TestNoErrorNamesForceAsTheFix(t *testing.T) {
	for _, test := range errorPathTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			res := test.Run(w)
			if res.Status == exitcode.OK {
				t.Fatalf("expected a failure, got status 0\nstdout: %s", res.Stdout)
			}

			wantAbsent(t, "stderr", res.Stderr, "--force", "force")
		})
	}
}
