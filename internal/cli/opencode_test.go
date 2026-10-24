package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
)

// presenceName is the presence file of session ses_1 under a TUI.
func presenceName(tui int) string {
	return "opencode.ses_1." + strconv.Itoa(tui) + "." + startOf(tui).String() + ".json"
}

// opencodeSetup starts the shared service at PID 500 and a TUI at PID 600, and returns the shell of
// session ses_1 under the service with the subprocesses a TUI plugin and a server plugin would spawn.
func opencodeSetup(t *testing.T, w *world) (shell, tuiPlugin, serverPlugin caller) {
	t.Helper()

	w.opencodeHost(500, 1, opencodeServiceArgs)
	w.opencodeHost(600, 1, opencodeTUIArgs)

	return w.opencodeShell(500, 501, "ses_1"), w.below(600, 601, nil), w.below(500, 502, nil)
}

func present(on bool) map[string]any {
	return map[string]any{"present": on}
}

func executing(on bool) map[string]any {
	return map[string]any{"executing": on}
}

func TestOpencodeAcquireNeedsAPresenceMarkFromALiveTUI(t *testing.T) {
	w := newWorld(t)
	shell, tui, server := opencodeSetup(t, w)

	res := w.run(shell, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "no presence mark from a live TUI", "needs a TUI with nitipan's plugin loaded",
		"opencode needs nitipan's two plugins")

	// An execution mark alone never admits a claim.
	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)
	wantStatus(t, w.run(shell, "acquire", "--checkout", w.dir("repo")), exitcode.Error)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	if got := w.sessionFiles("presence"); len(got) != 1 || got[0] != presenceName(600) {
		t.Fatalf("presence = %v", got)
	}

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	rec := w.leaseByID(id)
	if rec.Liveness != "session" || rec.Holder.Agent != "opencode" || rec.Holder.SessionID != "ses_1" ||
		rec.Holder.PID != 500 {
		t.Errorf("holder = %+v, liveness %q", rec.Holder, rec.Liveness)
	}

	if status, _ := w.livenessOf(shell, id); status != "alive" {
		t.Errorf("liveness = %s", status)
	}
}

func TestOpencodeSessionDiesWhenItsPresenceMarkGoes(t *testing.T) {
	w := newWorld(t)
	shell, tui, _ := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(false)), exitcode.OK)

	if got := w.sessionFiles("presence"); len(got) != 0 {
		t.Fatalf("presence = %v", got)
	}

	other := w.claude(700, "other")
	if status, reason := w.livenessOf(other, id); status != "dead" {
		t.Errorf("liveness = %s (%s)", status, reason)
	}
}

func TestOpencodeSessionReadsDeadWhenItsTUIExits(t *testing.T) {
	w := newWorld(t)
	shell, tui, _ := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.kill(600)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Errorf("liveness = %s", status)
	}

	if got := w.sessionFiles("presence"); len(got) != 0 {
		t.Errorf("a dead TUI's presence mark survived the sweep: %v", got)
	}
}

func TestOpencodeTurnKeepsTheSessionAliveAfterItsTUIDies(t *testing.T) {
	w := newWorld(t)
	shell, tui, server := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)

	if got := w.sessionFiles("executing"); len(got) != 1 {
		t.Fatalf("executing = %v", got)
	}

	w.kill(600)

	other := w.claude(700, "other")
	if status, reason := w.livenessOf(other, id); status != "alive" {
		t.Fatalf("liveness mid-turn = %s (%s)", status, reason)
	}

	wantStatus(t, w.hook(server, "execution", "ses_1", executing(false)), exitcode.OK)

	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Errorf("liveness after the turn = %s", status)
	}
}

type suspendedTest struct {
	Name       string
	DB         *fakeDB
	Want       string
	WantHint   string
	WantMarkOn bool
}

var suspendedTests = []suspendedTest{
	{
		Name:       "suspended is set",
		DB:         &fakeDB{suspended: map[string]bool{"ses_1": true}},
		Want:       "unknown",
		WantMarkOn: true,
	},
	{
		Name:     "database unreadable",
		DB:       &fakeDB{err: errors.New("no such table: session_v2")},
		Want:     "unknown",
		WantHint: "cannot read the opencode database: no such table: session_v2",

		WantMarkOn: true,
	},
	{
		Name: "suspended is clear",
		DB:   &fakeDB{suspended: map[string]bool{}},
		Want: "dead",
	},
}

func TestOpencodeExecutionMarkUnderADeadHostReadsTimeSuspended(t *testing.T) {
	for _, test := range suspendedTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.db = test.DB

			shell, tui, server := opencodeSetup(t, w)

			wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

			id := w.acquireOK(shell, "--checkout", w.dir("repo"))

			wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)

			// The service and the TUI both die mid-turn.
			w.kill(500)
			w.kill(600)

			other := w.claude(700, "other")

			doc := w.listJSON(other)
			lease := firstLease(t, doc)

			if status, reason := statusOf(t, lease); status != test.Want {
				t.Errorf("liveness = %v (%v)", status, reason)
			}

			hint, _ := lease["hint"].(string)
			if test.WantHint != "" && !strings.Contains(hint, test.WantHint) {
				t.Errorf("hint = %q, want %q", hint, test.WantHint)
			}

			if got := len(w.sessionFiles("executing")) == 1; got != test.WantMarkOn {
				t.Errorf("execution mark kept = %v, want %v", got, test.WantMarkOn)
			}

			if len(w.sessionFiles("presence")) != 0 {
				t.Error("a dead TUI's presence mark survived")
			}

			if id == "" {
				t.Fatal("no lease")
			}
		})
	}
}

func TestOpencodeStaleExecutionMarkOfADeadHostIsSweptAndReadsDead(t *testing.T) {
	// The private server of `opencode run --standalone` dies before its mark-off lands. The session is
	// absent from the database, or its time_suspended is clear. Either way no turn can resume.
	for _, db := range []*fakeDB{{suspended: map[string]bool{}}, {suspended: map[string]bool{"other": true}}} {
		w := newWorld(t)
		w.db = db

		w.opencodeHost(700, 1, []string{"/opt/homebrew/bin/opencode", "run", "--standalone", "hi"})
		w.opencodeHost(701, 700, []string{"/opt/homebrew/bin/opencode", "serve", "--stdio", "--port", "0"})

		wantStatus(t, w.hook(w.below(701, 702, nil), "execution", "ses_1", executing(true)), exitcode.OK)
		wantStatus(t, w.run(w.claude(800, "c"), "list"), exitcode.OK)

		if got := w.sessionFiles("executing"); len(got) != 1 {
			t.Fatalf("executing = %v", got)
		}

		w.kill(701)

		other := w.claude(900, "other")
		wantStatus(t, w.run(other, "list"), exitcode.OK)

		if got := w.sessionFiles("executing"); len(got) != 0 {
			t.Errorf("the sweep kept a stale execution mark: %v", got)
		}
	}

	// The rule reads such a session dead, with a lease that names it.
	w := newWorld(t)
	w.db = &fakeDB{}
	shell, tui, server := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)
	w.kill(600)
	w.kill(500)

	if status, reason := w.livenessOf(w.claude(900, "other"), id); status != "dead" {
		t.Errorf("liveness = %s (%s)", status, reason)
	}
}

func TestOpencodeUnreadableDatabaseIsNamedByList(t *testing.T) {
	w := newWorld(t)
	w.db = &fakeDB{err: errors.New("unable to open database file")}

	shell, tui, server := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	w.acquireOK(shell, "--checkout", w.dir("repo"))
	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)

	w.kill(500)
	w.kill(600)

	res := w.run(w.claude(700, "other"), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "hint: cannot read the opencode database: unable to open database file")
}

func TestOpencodePresenceComesOnlyFromTheTUIAndExecutionOnlyFromTheService(t *testing.T) {
	w := newWorld(t)
	_, tui, server := opencodeSetup(t, w)

	for _, test := range []struct {
		Name   string
		Caller caller
		Sub    string
		Extra  map[string]any
	}{
		{"presence from the service", server, "presence", present(true)},
		{"execution from the TUI", tui, "execution", executing(true)},
	} {
		res := w.hook(test.Caller, test.Sub, "ses_1", test.Extra)
		if res.Status == exitcode.OK {
			t.Errorf("%s succeeded", test.Name)
		}

		wantAbsent(t, "stderr", res.Stderr, "hook")
	}

	if len(w.sessionFiles("presence"))+len(w.sessionFiles("executing")) != 0 {
		t.Error("a refused mark was written")
	}
}

func TestOpencodeHookFromACodexHostIsRefused(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)

	res := w.hook(w.below(400, 401, nil), "presence", "ses_1", present(true))
	if res.Status == exitcode.OK {
		t.Fatal("a Codex process wrote an opencode mark")
	}

	wantAbsent(t, "stderr", res.Stderr, "hook")
}

func TestOpencodeStandaloneTUIOwnsItsPrivateServeChild(t *testing.T) {
	w := newWorld(t)

	w.opencodeHost(700, 1, append(opencodeTUIArgs, "--standalone"))
	w.opencodeHost(701, 700, []string{"/opt/homebrew/bin/opencode", "serve", "--stdio", "--port", "0"})

	shell := w.opencodeShell(701, 702, "ses_1")
	tui := w.below(700, 703, nil)
	server := w.below(701, 704, nil)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)

	if got := w.sessionFiles("presence"); len(got) != 1 || got[0] != presenceName(700) {
		t.Errorf("presence = %v", got)
	}

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	if rec := w.leaseByID(id); rec.Holder.PID != 701 {
		t.Errorf("the lease names host %d, want the serve child 701", rec.Holder.PID)
	}
}

func TestOpencodeSessionFileOfAHigherMajorMakesTheSessionUnknown(t *testing.T) {
	w := newWorld(t)
	shell, tui, _ := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	path := filepath.Join(w.state, "sessions", "presence", presenceName(600))
	if err := os.WriteFile(path, []byte(`{"version":"2.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// A file nitipan cannot use is not rewritten, even by the TUI it names.
	if res := w.hook(tui, "presence", "ses_1", present(true)); res.Status == exitcode.OK {
		t.Error("a mark of a higher major was rewritten")
	}

	w.kill(600)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "unknown" {
		t.Errorf("liveness = %s", status)
	}

	if data, err := os.ReadFile(path); err != nil || string(data) != `{"version":"2.0"}` {
		t.Errorf("the file changed or went away: %v", err)
	}
}

func TestOpencodeMarksOfAHigherMinorSurviveTheSweep(t *testing.T) {
	w := newWorld(t)
	w.db = &fakeDB{}

	shell, tui, server := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	wantStatus(t, w.hook(server, "execution", "ses_1", executing(true)), exitcode.OK)
	w.acquireOK(shell, "--checkout", w.dir("repo"))

	presence := presenceName(600)
	execution := "opencode.ses_1.500." + startOf(500).String() + ".json"

	w.rewriteSessionFile("presence", presence, map[string]any{"version": "1.3"})
	w.rewriteSessionFile("executing", execution, map[string]any{"version": "1.3"})

	w.kill(500)
	w.kill(600)

	wantStatus(t, w.run(w.claude(700, "other"), "list"), exitcode.OK)

	if got := w.sessionFiles("presence"); !contains(got, presence) {
		t.Errorf("the sweep removed a presence mark of a higher minor: %v", got)
	}

	if got := w.sessionFiles("executing"); !contains(got, execution) {
		t.Errorf("the sweep removed an execution mark of a higher minor: %v", got)
	}
}

func TestOpencodeSweepReadsTheDatabaseOnceForAllDeadExecutionMarks(t *testing.T) {
	w := newWorld(t)
	db := &fakeDB{suspended: map[string]bool{"ses_b": true}}
	w.db = db

	w.opencodeHost(500, 1, opencodeServiceArgs)

	server := w.below(500, 502, nil)
	for _, id := range []string{"ses_a", "ses_b", "ses_c"} {
		wantStatus(t, w.hook(server, "execution", id, executing(true)), exitcode.OK)
	}

	w.kill(500)

	wantStatus(t, w.run(w.claude(700, "other"), "list"), exitcode.OK)

	if db.reads != 1 {
		t.Errorf("the sweep read the database %d times, want 1", db.reads)
	}

	got := w.sessionFiles("executing")
	if len(got) != 1 || !strings.Contains(got[0], "ses_b") {
		t.Errorf("executing = %v, want only the suspended session's mark", got)
	}
}

func TestOpencodeSweepKeepsEveryDeadExecutionMarkWhenTheReadFails(t *testing.T) {
	w := newWorld(t)
	w.db = &fakeDB{err: errors.New("no answer within 800ms")}

	w.opencodeHost(500, 1, opencodeServiceArgs)

	server := w.below(500, 502, nil)
	for _, id := range []string{"ses_a", "ses_b"} {
		wantStatus(t, w.hook(server, "execution", id, executing(true)), exitcode.OK)
	}

	w.kill(500)

	wantStatus(t, w.run(w.claude(700, "other"), "list"), exitcode.OK)

	if got := w.sessionFiles("executing"); len(got) != 2 {
		t.Errorf("executing = %v, want both marks kept", got)
	}
}

func TestOpencodeAcceptsItsPermissionModes(t *testing.T) {
	w := newWorld(t)
	shell, tui, _ := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	wantStatus(t, w.run(shell, "acquire", "--checkout", w.dir("repo"), "--permission-mode", "plan"), exitcode.OK)

	res := w.run(shell, "advertise", "--permission-mode", "yolo")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "build", "plan", "auto")
}

func TestSessionFilesWithBadNamesAreIgnoredAndNeverVouchForASession(t *testing.T) {
	w := newWorld(t)
	shell, tui, _ := opencodeSetup(t, w)

	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	dir := filepath.Join(w.state, "sessions", "presence")
	for _, name := range []string{"notes.txt", "opencode.ses_1.json", "opencode.ses_1.600.zz.json", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	w.kill(600)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Errorf("a file with a bad name changed the verdict: %s", status)
	}
}
