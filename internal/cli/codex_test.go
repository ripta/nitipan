package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/session"
)

// codexSession starts a daemon at PID 400, writes the config, makes the lock file, and registers
// session t1 through the hook. It returns the session's shell and the hook's caller.
func codexSession(t *testing.T, w *world) (shell, hook caller) {
	t.Helper()

	const (
		thread = "t1"
		host   = 400
	)

	w.codexHost(host)
	w.codexConfig(goodCodexConfig)
	w.lockFile(true)

	shell = w.codexShell(host, host+1)
	hook = w.hookFor(host, host+2)

	wantStatus(t, w.hook(hook, "session-start", thread, nil), exitcode.OK)

	return shell, hook
}

func TestCodexSessionStartRegistersTheSessionAndAcquireSucceeds(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)

	if got := w.sessionFiles("registered"); len(got) != 1 || got[0] != registrationName("t1", 400) {
		t.Fatalf("registered = %v", got)
	}

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &reg); err != nil {
		t.Fatal(err)
	}

	if reg.Mode != session.ModeDaemon || reg.CodexHome != w.codexHome() || reg.SessionEnd != session.CheckPresent ||
		reg.UnloadDelaySecs == nil || *reg.UnloadDelaySecs != 60 {
		t.Errorf("registration = %+v", reg)
	}

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	rec := w.leaseByID(id)
	if rec.Liveness != "session" || rec.Holder.Agent != "codex" || rec.Holder.SessionID != "t1" || rec.Holder.PID != 400 {
		t.Errorf("holder = %+v, liveness %q", rec.Holder, rec.Liveness)
	}

	if status, _ := w.livenessOf(shell, id); status != "alive" {
		t.Errorf("liveness = %s", status)
	}
}

func TestCodexHookTakesTheSessionOnlyFromItsInput(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)
	w.codexConfig(goodCodexConfig)

	hk := w.hookFor(400, 402)
	hk.env["CODEX_SESSION_ID"] = "from-the-environment"

	wantStatus(t, w.hook(hk, "session-start", "from-the-input", nil), exitcode.OK)

	if got := w.sessionFiles("registered"); len(got) != 1 || got[0] != registrationName("from-the-input", 400) {
		t.Errorf("registered = %v", got)
	}
}

func TestCodexHookRefusesASessionOutsideTheAllowedSet(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)

	for _, bad := range []string{"../escape", "a.b", "has space", "", strings.Repeat("a", 129)} {
		res := w.hook(w.hookFor(400, 402), "session-start", bad, nil)
		if res.Status == exitcode.OK {
			t.Errorf("session %q was accepted", bad)
		}

		wantAbsent(t, "stderr", res.Stderr, "hook")
	}

	if got := w.sessionFiles("registered"); len(got) != 0 {
		t.Errorf("registered = %v", got)
	}
}

func TestCodexHookFailsClosedWhenNoAgentIsAbove(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)

	res := w.hook(w.terminal(900), "session-start", "t1", nil)
	if res.Status == exitcode.OK {
		t.Fatal("a hook with no agent above it succeeded")
	}

	wantAbsent(t, "stderr", res.Stderr, "hook")

	if got := w.sessionFiles("registered"); len(got) != 0 {
		t.Errorf("registered = %v", got)
	}

	if res := w.hook(w.terminal(901), "session-end", "t1", nil); res.Status == exitcode.OK {
		t.Error("an end with no agent above it succeeded")
	}

	if got := w.sessionFiles("ended"); len(got) != 0 {
		t.Errorf("ended = %v", got)
	}
}

type gateTest struct {
	Name   string
	Config string

	// Before runs after the config and the lock file exist and before the hook registers the session.
	Before func(w *world)

	// Break runs after the registration and before acquire.
	Break func(w *world)

	Want []string
}

var gateTests = []gateTest{
	{
		Name:   "default delay",
		Config: "",
		Want:   []string{"thread_unload_delay_secs is 1800", "the Codex daemon (pid 400)", "after that host restarts"},
	},
	{
		Name:   "long delay",
		Config: "thread_unload_delay_secs = 120\n",
		Want:   []string{"thread_unload_delay_secs is 120", "must be 60 or less"},
	},
	{
		Name:   "delay in an unreadable form",
		Config: "thread_unload_delay_secs = \"60\"\n",
		Want:   []string{"unload delay of the Codex daemon (pid 400) is unknown", "after that host restarts"},
	},
	{
		Name:   "no session end entry",
		Config: "thread_unload_delay_secs = 60\n",
		Want:   []string{"no SessionEnd entry that runs nitipan", "the Codex daemon (pid 400)"},
	},
	{
		Name: "a session end entry for something else",
		Config: "thread_unload_delay_secs = 60\n[[hooks.SessionEnd]]\n[[hooks.SessionEnd.hooks]]\ntype = \"command\"\n" +
			"command = \"/bin/other hook session-end\"\n",
		Want: []string{"could not confirm a SessionEnd entry"},
	},
	{
		Name:   "config written after the host started",
		Config: goodCodexConfig,
		Before: func(w *world) {
			path := filepath.Join(w.codexHome(), "config.toml")
			later := time.Unix(codexEpoch+10_000, 0)

			if err := os.Chtimes(path, later, later); err != nil {
				w.t.Fatal(err)
			}
		},
		Want: []string{"unload delay of the Codex daemon (pid 400) is unknown", "after that host restarts"},
	},
	{
		Name:   "no writer lock file",
		Config: goodCodexConfig,
		Break:  func(w *world) { w.lockFile(false) },
		Want:   []string{"no writer-lock file for session t1", "this Codex version is not supported"},
	},
	{
		Name:   "writer lock unreadable",
		Config: goodCodexConfig,
		Break: func(w *world) {
			dir := filepath.Join(w.codexHome(), "thread-writer-locks")
			if err := os.RemoveAll(dir); err != nil {
				w.t.Fatal(err)
			}

			w.writeFile(dir, "not a directory", time.Unix(codexEpoch, 0))
		},
		Want: []string{"cannot check Codex's writer-lock file"},
	},
}

func TestCodexAcquireFailsClosedWhenARecordedCheckFails(t *testing.T) {
	for _, test := range gateTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.codexHost(400)
			w.codexConfig(test.Config)
			w.lockFile(true)

			shell, hook := w.codexShell(400, 401), w.hookFor(400, 402)

			if test.Before != nil {
				test.Before(w)
			}

			wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

			if test.Break != nil {
				test.Break(w)
			}

			res := w.run(shell, "acquire", "--checkout", w.dir("repo"))
			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, test.Want...)
			wantContains(t, "stderr", res.Stderr, "Codex needs three things", "thread_unload_delay_secs = 60")

			if len(w.leases()) != 0 {
				t.Error("a failed gate wrote a lease")
			}
		})
	}
}

func TestCodexAcquireWithoutARegistrationSaysTheHookMayHaveTimedOut(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)

	res := w.run(w.codexShell(400, 401), "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "no registration under the Codex daemon (pid 400)",
		"may have timed out", "/usr/local/bin/nitipan hook session-start", "/usr/local/bin/nitipan hook session-end",
		"timeout = 3", "thread_unload_delay_secs = 60", "writable_roots", w.state)
}

func TestCodexAcquireFailsClosedWithoutTheRootSessionID(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)

	shell := w.codexShell(400, 401)
	delete(shell.env, "CODEX_SESSION_ID")

	res := w.run(shell, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "exported no session ID")
}

func TestCodexSessionReadsDeadAfterItsEndAndItsLockFileGoes(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)

	if got := w.sessionFiles("ended"); len(got) != 1 || got[0] != endMarkName("t1", 400) {
		t.Fatalf("ended = %v", got)
	}

	// A late SessionEnd after a resume leaves the lock file in place, so the session stays alive.
	if status, _ := w.livenessOf(shell, id); status != "alive" {
		t.Fatalf("liveness with the lock file present = %s", status)
	}

	if got := w.sessionFiles("ended"); len(got) != 1 {
		t.Errorf("the end mark of a live session was dropped: %v", got)
	}

	w.lockFile(false)

	other := w.claude(700, "other")
	if status, reason := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness = %s (%s)", status, reason)
	}

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &reg); err != nil {
		t.Fatal(err)
	}

	if !reg.Ended {
		t.Error("the registration was not marked ended")
	}

	if got := w.sessionFiles("ended"); len(got) != 0 {
		t.Errorf("the end mark outlived its conversion: %v", got)
	}

	res := w.run(other, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "cleared", id)
}

func TestCodexSecondSessionEndForTheSameSessionAndHostSucceeds(t *testing.T) {
	w := newWorld(t)
	_, hook := codexSession(t, w)

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)

	if got := w.sessionFiles("ended"); len(got) != 1 {
		t.Errorf("ended = %v", got)
	}
}

func TestCodexSessionEndTakesNoLock(t *testing.T) {
	w := newWorld(t)
	_, hook := codexSession(t, w)

	l, err := w.engineFor(hook).Dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Unlock() }()

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
}

func TestCodexResumeClearsTheEndAndTheNextCallReportsIt(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness = %s", status)
	}

	w.tick()
	w.lockFile(true)

	// The session resumes: Codex fires SessionStart again under the same host.
	wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

	if got := w.sessionFiles("registered"); len(got) != 1 {
		t.Fatalf("registered = %v", got)
	}

	if w.tombstoneCount() != 1 {
		t.Fatalf("the resume left %d notices, want 1", w.tombstoneCount())
	}

	// The hook delivered nothing. The session's next call does.
	res := w.run(shell, "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "recorded the end of session t1", "registered again", "your leases stand")

	if w.tombstoneCount() != 0 {
		t.Error("the notice was not removed once delivered")
	}

	if status, _ := w.livenessOf(shell, id); status != "alive" {
		t.Errorf("liveness after the resume = %s", status)
	}
}

func TestCodexResumeKeepsTheEndWhenItsNoticeCannotBePublished(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)
	wantStatus(t, w.run(w.claude(700, "other"), "list"), exitcode.OK)

	// A file where the tombstone directory belongs makes the notice impossible to publish.
	w.writeFile(filepath.Join(w.state, "tombstones"), "in the way", time.Unix(codexEpoch, 0))
	w.tick()
	w.lockFile(true)

	if res := w.hook(hook, "session-start", "t1", nil); res.Status == exitcode.OK {
		t.Fatal("the registration succeeded without its notice")
	}

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &reg); err != nil || !reg.Ended {
		t.Errorf("the end was cleared before the notice went out: %+v, %v", reg, err)
	}

	// Once the notice can go out, the next registration clears the end and sends it.
	if err := os.Remove(filepath.Join(w.state, "tombstones")); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

	if w.tombstoneCount() != 1 {
		t.Errorf("tombstones = %d, want the one notice", w.tombstoneCount())
	}
}

func TestCodexResumeNoticeNamesALeaseAnotherSessionClaimedMeanwhile(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)

	other := w.claude(700, "other")
	w.tick()

	// list converts the end mark. The other session then clears the dead lease and claims the domain.
	wantStatus(t, w.run(other, "list"), exitcode.OK)
	w.tick()

	claimed := w.acquireOK(other, "--checkout", w.dir("repo"))

	// The session resumes and takes another lease, which keeps its registration wanted.
	w.tick()
	w.lockFile(true)
	w.acquireOK(shell, "--checkout", w.dir("elsewhere"))

	w.tick()
	wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

	res := w.run(shell, "list")
	wantContains(t, "stderr", res.Stderr, "recorded the end of session t1", "other sessions claimed this lease", claimed)
}

func TestCodexHostRestartLeavesLiveSessionsUnknownAndAResumedCallMovesTheLeases(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.kill(400)

	other := w.claude(700, "other")

	if status, reason := w.livenessOf(other, id); status != "unknown" {
		t.Fatalf("liveness after the restart = %s (%s)", status, reason)
	}

	w.tick()
	w.codexHost(500)

	shell2, hook2 := w.codexShell(500, 501), w.hookFor(500, 502)
	wantStatus(t, w.hook(hook2, "session-start", "t1", nil), exitcode.OK)

	res := w.run(shell2, "advertise", "--name", "renamed")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "runs under a new codex host, pid 500 instead of pid 400", "moved with it", id)

	if rec := w.leaseByID(id); rec.Holder.PID != 500 || !rec.Holder.Start.Equal(codexStart(500)) {
		t.Errorf("holder after the move = %+v", rec.Holder)
	}

	if status, _ := w.livenessOf(other, id); status != "alive" {
		t.Errorf("liveness after the move = %s", status)
	}
}

func TestCodexLeasesStayOnTheOldHostWhenTheLockFileIsMissing(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.kill(400)
	w.tick()
	w.codexHost(500)

	shell2, hook2 := w.codexShell(500, 501), w.hookFor(500, 502)
	wantStatus(t, w.hook(hook2, "session-start", "t1", nil), exitcode.OK)

	w.lockFile(false)

	res := w.run(shell2, "advertise", "--name", "renamed")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "stay on the old host and read unknown", "no writer-lock file")

	if rec := w.leaseByID(id); rec.Holder.PID != 400 {
		t.Errorf("the lease moved to %d without a lock file", rec.Holder.PID)
	}
}

func TestCodexLeaseOnTheOldHostReadsUnknownWhileTheNewHostHasNoLockFile(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.kill(400)
	w.tick()
	w.codexHost(500)

	shell2, hook2 := w.codexShell(500, 501), w.hookFor(500, 502)
	wantStatus(t, w.hook(hook2, "session-start", "t1", nil), exitcode.OK)

	w.lockFile(false)

	res := w.run(shell2, "advertise", "--name", "renamed")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "stay on the old host and read unknown")

	other := w.claude(700, "other")

	status, reason := w.livenessOf(other, id)
	if status != "unknown" {
		t.Fatalf("liveness = %s (%s), want unknown", status, reason)
	}

	wantContains(t, "reason", reason, "host 400", "host 500")
}

func TestCodexSessionAlreadyEndedStaysDeadAcrossAHostRestart(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness = %s", status)
	}

	w.kill(400)

	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Errorf("an ended session read %s after its host died", status)
	}

	if got := w.sessionFiles("registered"); len(got) != 1 {
		t.Errorf("the ended registration did not outlive its host while a lease names it: %v", got)
	}
}

func TestCodexSessionFoundDeadThenResumedReadsUnknownAfterALaterRestart(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness = %s", status)
	}

	// The daemon restarts. The session resumes under the new host, which clears the ended
	// registration that sat under the old one.
	w.kill(400)
	w.tick()
	w.codexHost(500)
	w.lockFile(true)

	hook2 := w.hookFor(500, 502)
	wantStatus(t, w.hook(hook2, "session-start", "t1", nil), exitcode.OK)

	if got := w.sessionFiles("registered"); len(got) != 1 || got[0] != registrationName("t1", 500) {
		t.Fatalf("registered = %v", got)
	}

	w.kill(500)

	if status, _ := w.livenessOf(other, id); status != "unknown" {
		t.Errorf("liveness after a second restart = %s", status)
	}
}

func TestCodexSessionWhoseNewestRegistrationIsEndedReadsAliveWhileItsLockFileExists(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)
	w.lockFile(false)

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness = %s", status)
	}

	// The lock file comes back under the same live host, as after a resume the hook missed.
	w.lockFile(true)

	if status, _ := w.livenessOf(other, id); status != "alive" {
		t.Errorf("liveness = %s", status)
	}
}

func TestCodexWriterLockThatFailsWithAnythingButENOENTReadsUnknown(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.hook(hook, "session-end", "t1", nil), exitcode.OK)

	dir := filepath.Join(w.codexHome(), "thread-writer-locks")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	w.writeFile(dir, "not a directory", time.Unix(codexEpoch, 0))

	other := w.claude(700, "other")
	if status, _ := w.livenessOf(other, id); status != "unknown" {
		t.Errorf("liveness = %s", status)
	}

	res := w.run(other, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Refused)
}

func TestCodexEmbeddedHostIsRecordedAsTheTUIAndItsDeathEndsTheSession(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400, codexTUIArgs...)
	w.codexConfig(goodCodexConfig)
	w.lockFile(true)

	shell, hook := w.codexShell(400, 401), w.hookFor(400, 402)
	wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &reg); err != nil {
		t.Fatal(err)
	}

	if reg.Mode != session.ModeTUI {
		t.Fatalf("mode = %q", reg.Mode)
	}

	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.kill(400)

	other := w.claude(700, "other")
	if status, reason := w.livenessOf(other, id); status != "dead" {
		t.Fatalf("liveness after the TUI exited = %s (%s)", status, reason)
	}

	// The registration survives the sweep while a lease names the session.
	if got := w.sessionFiles("registered"); len(got) != 1 {
		t.Fatalf("registered = %v", got)
	}

	wantStatus(t, w.run(other, "break", id), exitcode.OK)
	wantStatus(t, w.run(other, "list"), exitcode.OK)

	if got := w.sessionFiles("registered"); len(got) != 0 {
		t.Errorf("a dead embedded host's registration outlived its last lease: %v", got)
	}
}

func TestCodexHostThatIsNotTheTUIIsRecordedAsADaemon(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400, "/usr/local/bin/codex", "app-server", "--stdio")
	w.codexConfig(goodCodexConfig)

	wantStatus(t, w.hook(w.hookFor(400, 402), "session-start", "t1", nil), exitcode.OK)

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &reg); err != nil {
		t.Fatal(err)
	}

	if reg.Mode != session.ModeDaemon {
		t.Errorf("mode = %q", reg.Mode)
	}
}

func TestCodexConfigChecksAreCopiedFromTheFirstRegistrationUnderAHost(t *testing.T) {
	w := newWorld(t)
	w.codexHost(400)
	w.codexConfig(goodCodexConfig)

	hook := w.hookFor(400, 402)
	wantStatus(t, w.hook(hook, "session-start", "t1", nil), exitcode.OK)

	// The config changes under the running host. A change needs a restart, so the host keeps its checks.
	w.codexConfig("thread_unload_delay_secs = 1800\n")
	wantStatus(t, w.hook(hook, "session-start", "t2", nil), exitcode.OK)

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t2", 400)), &reg); err != nil {
		t.Fatal(err)
	}

	if reg.UnloadDelaySecs == nil || *reg.UnloadDelaySecs != 60 || reg.SessionEnd != session.CheckPresent {
		t.Errorf("the second registration read the config again: %+v", reg)
	}
}

func TestCodexHookNeverDeliversOrRemovesATombstone(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	wantStatus(t, w.run(w.terminal(900), "break", "--force", id), exitcode.OK)

	if w.tombstoneCount() != 1 {
		t.Fatalf("tombstones = %d", w.tombstoneCount())
	}

	w.tick()

	res := w.hook(hook, "session-start", "t1", nil)
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "broken")

	if w.tombstoneCount() != 1 {
		t.Error("the hook removed a tombstone")
	}

	res = w.run(shell, "list")
	wantContains(t, "stderr", res.Stderr, "was broken by")
}

func TestCodexSessionFileOfAHigherMajorMakesTheSessionUnknown(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	path := filepath.Join(w.state, "sessions", "registered", registrationName("t1", 400))

	var body map[string]any
	if err := json.Unmarshal(w.registration(registrationName("t1", 400)), &body); err != nil {
		t.Fatal(err)
	}

	body["version"] = "2.0"

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	other := w.claude(700, "other")

	status, reason := w.livenessOf(other, id)
	if status != "unknown" || !strings.Contains(reason, "cannot be used") {
		t.Errorf("liveness = %s (%s)", status, reason)
	}

	// An old binary never rewrites a session file it cannot use, so a new registration is refused.
	if res := w.hook(hook, "session-start", "t1", nil); res.Status == exitcode.OK {
		t.Error("a registration rewrote a session file of a higher major")
	}

	// The file survives the sweep even after its host dies.
	w.kill(400)

	if status, _ := w.livenessOf(other, id); status != "unknown" {
		t.Errorf("liveness after the host died = %s", status)
	}

	if after, err := os.ReadFile(path); err != nil || !strings.Contains(string(after), `"2.0"`) {
		t.Errorf("the session file changed or went away: %v", err)
	}
}

func TestCodexRegistrationOfAHigherMinorIsNeitherSweptNorClearedByAnOldBinary(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)
	w.acquireOK(shell, "--checkout", w.dir("repo"))

	// A newer nitipan wrote the registration, and the session ended.
	name := registrationName("t1", 400)
	w.rewriteSessionFile("registered", name, map[string]any{"version": "1.3", "ended": true})

	// Another registration of a higher minor sits under a host that is gone, with no lease naming it.
	w.codexHost(450)
	wantStatus(t, w.hook(w.hookFor(450, 452), "session-start", "t9", nil), exitcode.OK)

	orphan := registrationName("t9", 450)
	w.rewriteSessionFile("registered", orphan, map[string]any{"version": "1.3"})

	w.kill(450)
	w.kill(400)
	w.tick()
	w.codexHost(500)

	// The sweep keeps both files.
	other := w.claude(700, "other")
	wantStatus(t, w.run(other, "list"), exitcode.OK)

	for _, keep := range []string{name, orphan} {
		if got := w.sessionFiles("registered"); !contains(got, keep) {
			t.Errorf("the sweep removed %s of a higher minor: %v", keep, got)
		}
	}

	// A new registration for the session clears ended registrations, but not one it must not remove.
	wantStatus(t, w.hook(w.hookFor(500, 502), "session-start", "t1", nil), exitcode.OK)

	if got := w.sessionFiles("registered"); !contains(got, name) {
		t.Errorf("a registration cleared an ended registration of a higher minor: %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}

	return false
}

func TestCodexListFlagsALikelyEndedSessionAndNeverClearsIt(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)
	id := w.acquireOK(shell, "--checkout", w.dir("repo"))

	w.lockFile(false)

	doc := w.listJSON(shell)
	lease := firstLease(t, doc)

	hint, _ := lease["hint"].(string)
	if !strings.Contains(hint, "likely ended") {
		t.Errorf("hint = %q", hint)
	}

	if status, _ := w.livenessOf(shell, id); status != "alive" {
		t.Errorf("the hint changed the verdict: %s", status)
	}

	res := w.run(shell, "list")
	wantContains(t, "stdout", res.Stdout, "hint: likely ended")

	if len(w.leases()) != 1 {
		t.Error("list cleared the lease on a hint")
	}
}

func TestCodexAcceptsItsPermissionModes(t *testing.T) {
	w := newWorld(t)
	shell, _ := codexSession(t, w)

	wantStatus(t, w.run(shell, "acquire", "--checkout", w.dir("repo"), "--permission-mode", "workspace-write"),
		exitcode.OK)

	res := w.run(shell, "advertise", "--permission-mode", "yolo")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "read-only", "workspace-write", "danger-full-access", "on-request")
}

const codexTUIRewrite = `
[tui]
screen_reader_detection_done = true

[tui.model_availability_nux]
"gpt-5.5" = 3
`

// registeredChecks reads the two config checks of the registration for thread t1 under a host.
func registeredChecks(t *testing.T, w *world, host int) session.Registration {
	t.Helper()

	var reg session.Registration
	if err := json.Unmarshal(w.registration(registrationName("t1", host)), &reg); err != nil {
		t.Fatal(err)
	}

	return reg
}

// rewrittenByCodex rewrites the config after the first host started, as a TUI start does, and
// starts a second host that is older than the rewritten file.
func rewrittenByCodex(t *testing.T, w *world, content string) session.Registration {
	t.Helper()

	w.writeFile(filepath.Join(w.codexHome(), "config.toml"), content, time.Unix(codexEpoch+10_000, 0))
	w.codexHost(500, codexTUIArgs...)

	wantStatus(t, w.hook(w.hookFor(500, 502), "session-start", "t1", nil), exitcode.OK)

	return registeredChecks(t, w, 500)
}

func TestCodexConfigOlderThanItsHostIsRememberedAndRecorded(t *testing.T) {
	w := newWorld(t)
	codexSession(t, w)

	reg := registeredChecks(t, w, 400)
	if reg.UnloadDelaySecs == nil || *reg.UnloadDelaySecs != 60 || reg.SessionEnd != session.CheckPresent {
		t.Errorf("checks = %v, %s", reg.UnloadDelaySecs, reg.SessionEnd)
	}

	if got := w.sessionFiles("config"); len(got) != 1 {
		t.Errorf("config memory = %v, want one file", got)
	}
}

func TestCodexNewerConfigWithTheRememberedValuesIsTrusted(t *testing.T) {
	w := newWorld(t)
	codexSession(t, w)

	reg := rewrittenByCodex(t, w, goodCodexConfig+"# touched\n")
	if reg.UnloadDelaySecs == nil || *reg.UnloadDelaySecs != 60 || reg.SessionEnd != session.CheckPresent {
		t.Errorf("checks = %v, %s", reg.UnloadDelaySecs, reg.SessionEnd)
	}
}

func TestCodexTUIRewriteOfTheConfigDoesNotMatter(t *testing.T) {
	w := newWorld(t)
	codexSession(t, w)

	reg := rewrittenByCodex(t, w, goodCodexConfig+codexTUIRewrite)
	if reg.UnloadDelaySecs == nil || *reg.UnloadDelaySecs != 60 || reg.SessionEnd != session.CheckPresent {
		t.Errorf("checks = %v, %s", reg.UnloadDelaySecs, reg.SessionEnd)
	}
}

func TestCodexNewerConfigWithAChangedDelayReadsUnknown(t *testing.T) {
	w := newWorld(t)
	codexSession(t, w)

	changed := strings.Replace(goodCodexConfig, "thread_unload_delay_secs = 60", "thread_unload_delay_secs = 600", 1)

	reg := rewrittenByCodex(t, w, changed+codexTUIRewrite)
	if reg.UnloadDelaySecs != nil {
		t.Errorf("a changed delay was trusted: %d", *reg.UnloadDelaySecs)
	}

	if reg.SessionEnd != session.CheckPresent {
		t.Errorf("the unchanged SessionEnd check = %s", reg.SessionEnd)
	}
}

func TestCodexNewerConfigWithNothingRememberedReadsUnknown(t *testing.T) {
	w := newWorld(t)
	w.codexHost(500, codexTUIArgs...)
	w.writeFile(filepath.Join(w.codexHome(), "config.toml"), goodCodexConfig, time.Unix(codexEpoch+10_000, 0))

	wantStatus(t, w.hook(w.hookFor(500, 502), "session-start", "t1", nil), exitcode.OK)

	reg := registeredChecks(t, w, 500)
	if reg.UnloadDelaySecs != nil || reg.SessionEnd != session.CheckUnknown {
		t.Errorf("checks = %v, %s", reg.UnloadDelaySecs, reg.SessionEnd)
	}
}

type memoryGuardTest struct {
	Name string
	Body string
}

var memoryGuardTests = []memoryGuardTest{
	{Name: "higher major", Body: `{"version":"2.0","codex_home":"%s","session_end":"present"}`},
	{Name: "higher minor", Body: `{"version":"1.9","codex_home":"%s","session_end":"present","extra":1}`},
	{Name: "undecodable", Body: `not json at all`},
}

func TestCodexConfigMemoryThatCannotBeWrittenIsLeftUntouched(t *testing.T) {
	for _, test := range memoryGuardTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			codexSession(t, w)

			files := w.sessionFiles("config")
			if len(files) != 1 {
				t.Fatalf("config memory = %v", files)
			}

			path := filepath.Join(w.state, "sessions", "config", files[0])
			body := strings.Replace(test.Body, "%s", w.codexHome(), 1)

			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			changed := strings.Replace(goodCodexConfig, "thread_unload_delay_secs = 60", "thread_unload_delay_secs = 30", 1)
			w.codexConfig(changed)
			w.codexHost(500, codexTUIArgs...)

			wantStatus(t, w.hook(w.hookFor(500, 502), "session-start", "t1", nil), exitcode.OK)

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != body {
				t.Errorf("the memory file was rewritten: %s", got)
			}
		})
	}
}
