package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

const (
	measureEnv     = "NITIPAN_MEASURE_REBASE"
	measureCommits = 60
	measureRounds  = 5
)

// TestMeasureTheHookOnALongRebase times a rebase of a long branch with and without the hook. It
// only runs when asked, since a timing is no assertion. Run it with
//
//	NITIPAN_MEASURE_REBASE=1 go test ./internal/cli -run MeasureTheHook -v
func TestMeasureTheHookOnALongRebase(t *testing.T) {
	if os.Getenv(measureEnv) == "" {
		t.Skip("set " + measureEnv + " to time a rebase")
	}

	r, _, beta, _, _ := newPairedRig(t)

	r.must(r.user(`cd "$REPO"
git checkout -q -b long main
for i in $(seq 1 ` + strconv.Itoa(measureCommits) + `); do
	echo $i > docs/f$i.txt
	git add docs/f$i.txt
	git commit -qm "f$i"
done
git checkout -q -b onto main
echo base >> README
git commit -qam base
git checkout -q long`))

	original := r.refs("long")[0]

	rewind := func() {
		r.must(r.user(`cd "$REPO"; git checkout -q long; git reset -q --hard ` + original))
	}

	rebase := func() time.Duration {
		rewind()

		start := time.Now()
		res := beta.run(`git rebase -q onto`)
		took := time.Since(start)

		if res.Status != 0 {
			t.Fatalf("the rebase failed\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
		}

		return took
	}

	measure := func(label string) {
		var took []time.Duration
		for i := 0; i < measureRounds; i++ {
			took = append(took, rebase())
		}

		slices.Sort(took)
		t.Logf("%-40s median %v, fastest %v, slowest %v, over %d commits", label, took[len(took)/2], took[0],
			took[len(took)-1], measureCommits)
	}

	r.must(r.user(`nit setup git --uninstall "$REPO" > /dev/null`))
	measure("without the hook")

	r.must(r.user(`nit setup git --install "$REPO" > /dev/null`))
	measure("with the hook, a claude lease")

	r.plantOpencodeSession(t)
	t.Logf("the planted session lease reads:\n%s", r.must(r.user(`nit list`)).Stdout)
	measure("with the hook, plus an opencode session lease")
}

// plantOpencodeSession gives the state directory a lease of liveness kind session whose judgement
// ends at opencode's database, the path that runs sqlite3. The session is suspended in the database,
// so the sweep that every locked call runs keeps its execution mark under the dead host.
func (r *rig) plantOpencodeSession(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is not installed")
	}

	dbDir := filepath.Join(r.home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	create := exec.Command("sqlite3", filepath.Join(dbDir, "opencode.db"),
		"create table session_v2 (id text, time_suspended integer); insert into session_v2 values ('ses_cost', 1);")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("creating the database: %v\n%s", err, out)
	}

	const (
		session = "ses_cost"
		deadPID = 99999
		start   = "1700000000-0"
	)

	mark := `{"version":"1.0","agent":"opencode","session":"` + session + `","pid":99999,` +
		`"start":{"platform":"darwin","sec":1700000000},"written_at":"2026-10-25T00:00:00Z"}`

	dir := filepath.Join(r.state, "sessions", "executing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "opencode."+session+".99999."+start+".json"), []byte(mark), 0o600); err != nil {
		t.Fatal(err)
	}

	lib := filepath.Join(r.repo, "lib")
	id := "01JAAAAAAAAAAAAAAAAAAAAAAB"
	lease := `{"version":"1.0","id":"` + id + `","footprint":[{"type":"path","path":"` + lib + `"}],"clear_when_dead":true,` +
		`"liveness":"session","machine":` + machineJSON(t) + `,"holder":{"agent":"opencode","session_id":"` + session + `",` +
		`"pid":99999,"start":{"platform":"darwin","sec":1700000000}},"reach":"machine","domain":{"kind":"path",` +
		`"checkout":"` + lib + `","keys":[{"path":"` + lib + `"}]},"acting_for":"someone","acquired_at":"2026-10-25T00:00:00Z"}`

	leases := filepath.Join(r.state, "leases")
	if err := os.MkdirAll(leases, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(leases, id+".json"), []byte(lease), 0o600); err != nil {
		t.Fatal(err)
	}
}

// machineJSON is the machine the test binary would record for itself.
func machineJSON(t *testing.T) string {
	t.Helper()

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	return `{"host":"` + host + `"}`
}
