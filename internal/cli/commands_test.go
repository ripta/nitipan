package cli

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/state"
)

type strayTest struct {
	Name string
	Args []string
}

var strayTests = []strayTest{
	{Name: "acquire", Args: []string{"acquire"}},
	{Name: "advertise", Args: []string{"advertise", "--name", "x"}},
	{Name: "release", Args: []string{"release", "--all"}},
	{Name: "request", Args: []string{"request", "01JZZZZZZZZZZZZZZZZZZZZZZZ"}},
	{Name: "grant", Args: []string{"grant", "01JZZZZZZZZZZZZZZZZZZZZZZZ"}},
	{Name: "decline", Args: []string{"decline", "01JZZZZZZZZZZZZZZZZZZZZZZZ"}},
	{Name: "revoke", Args: []string{"revoke", "01JZZZZZZZZZZZZZZZZZZZZZZZ"}},
	{Name: "recall", Args: []string{"recall", "01JZZZZZZZZZZZZZZZZZZZZZZZ", "--deadline", "30m"}},
}

func TestStrayAgentVariablesFailHolderCommandsClosed(t *testing.T) {
	for _, test := range strayTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			res := w.run(w.stray(900), test.Args...)
			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, "CLAUDE_CODE_SESSION_ID", "no ancestor")

			if len(w.leases()) != 0 {
				t.Error("a fail-closed command wrote a lease")
			}
		})
	}
}

func TestLockBusyGivesItsOwnStatusAndNamesTheHolder(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		w := newWorld(t)
		w.table.Add(4242, proctest.Process{Start: startOf(4242), Stopped: stopped})

		dir, err := state.Open(w.state, state.Options{
			Checker:   &liveness.Checker{Procs: w.table, Local: testMachine},
			Procs:     w.table,
			Machine:   testMachine,
			SelfPID:   4242,
			SelfStart: startOf(4242),
			Command:   "nitipan acquire --elsewhere",
		})
		if err != nil {
			t.Fatal(err)
		}

		held, err := dir.Lock()
		if err != nil {
			t.Fatal(err)
		}

		res := w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
		wantStatus(t, res, exitcode.LockBusy)

		want := "which is running"
		if stopped {
			want = "which is stopped"
		}

		wantContains(t, "stderr", res.Stderr, "process 4242", "nitipan acquire --elsewhere", want)

		_ = held.Unlock()

		if len(w.leases()) != 0 {
			t.Error("a call that timed out on the lock wrote a lease")
		}
	}
}

func TestStateDirectoryFailsClosedWhenTheUserLookupFails(t *testing.T) {
	w := newWorld(t)
	decoy := w.dir("decoy")

	t.Setenv("HOME", decoy)
	t.Setenv("XDG_STATE_HOME", decoy)

	w.home = func() (string, error) {
		return state.ResolveDirWith(func(string) (*user.User, error) { return nil, errors.New("directory service down") })
	}

	res := w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "user database")

	if entries, _ := os.ReadDir(decoy); len(entries) != 0 {
		t.Errorf("nitipan fell back to $HOME: %v", entries)
	}
}

func TestStateDirectoryComesFromTheUserDatabaseNotFromHomeOrXDG(t *testing.T) {
	w := newWorld(t)
	decoy := w.dir("decoy")
	home := w.dir("realhome")

	t.Setenv("HOME", decoy)
	t.Setenv("XDG_STATE_HOME", decoy)

	w.home = func() (string, error) {
		return state.ResolveDirWith(func(string) (*user.User, error) { return &user.User{HomeDir: home}, nil })
	}

	wantStatus(t, w.run(w.claude(200, "s1"), "acquire", "--checkout", w.dir("repo")), exitcode.OK)

	info, err := os.Stat(filepath.Join(home, ".local", "state", "nitipan"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("state directory under the database's home: %v, %v", info, err)
	}

	if entries, _ := os.ReadDir(decoy); len(entries) != 0 {
		t.Errorf("state went to the decoy: %v", entries)
	}
}

func TestABlockedWritePrintsTheDetectedAgentsEntries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}

	w := newWorld(t)
	blocked := w.dir("blocked")

	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	target := filepath.Join(blocked, "nitipan")
	w.home = func() (string, error) { return target, nil }

	c := w.claude(200, "s1")

	res := w.run(c, "acquire", "--checkout", w.dir("repo"))
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "likely", "Detected agent: claude",
		`"Edit(`+target+`/**)"`, `"`+target+`"`, "mkdir -p -m 700 "+target)

	setupText := w.run(c, "setup")
	wantStatus(t, setupText, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, setupText.Stdout[:len(setupText.Stdout)-1])
}

func TestSetupPrintsAbsoluteEntriesForTheNamedOrDetectedAgent(t *testing.T) {
	w := newWorld(t)

	for _, res := range []result{w.run(w.terminal(900), "setup", "claude"), w.run(w.claude(200, "s1"), "setup")} {
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stdout", res.Stdout, `"Edit(`+w.state+`/**)"`, `"`+w.state+`"`, "mkdir -p -m 700 "+w.state)
		wantAbsent(t, "stdout", res.Stdout, "~")
	}

	if _, err := os.Stat(w.state); !os.IsNotExist(err) {
		t.Error("setup created the state directory")
	}
}

func TestSetupNeedsAnAgentWhenNoneRunsIt(t *testing.T) {
	w := newWorld(t)

	wantStatus(t, w.run(w.terminal(900), "setup"), exitcode.Usage)
	wantStatus(t, w.run(w.terminal(900), "setup", "bogus"), exitcode.Usage)
	wantStatus(t, w.run(w.terminal(900), "setup", "claude", "codex"), exitcode.Usage)
}

type unsupportedTest struct {
	Name      string
	Agent     string
	Proc      proctest.Process
	Env       map[string]string
	WantErr   string
	WantSetup string
}

var unsupportedTests = []unsupportedTest{
	{
		Name:      "codex",
		Agent:     "codex",
		Proc:      proctest.Process{Name: "codex", Args: []string{"/usr/local/bin/codex", "app-server", "--managed-daemon"}},
		Env:       map[string]string{"CODEX_SESSION_ID": "thread-1", "CODEX_THREAD_ID": "thread-1"},
		WantErr:   "has no registration under the Codex daemon (pid 400)",
		WantSetup: "Codex needs three things",
	},
	{
		Name:      "opencode",
		Agent:     "opencode",
		Proc:      proctest.Process{Name: "opencode", Args: []string{"/usr/local/bin/opencode", "serve", "--service"}},
		Env:       map[string]string{"OPENCODE_SESSION_ID": "ses_1"},
		WantErr:   "needs a TUI with nitipan's plugin loaded",
		WantSetup: "opencode needs nitipan's two plugins",
	},
}

func TestAcquireFromCodexAndOpencodeFailsClosedWithoutTheirReports(t *testing.T) {
	for _, test := range unsupportedTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			agent := test.Proc
			agent.Start, agent.PPID = startOf(400), 1
			w.table.Add(400, agent)
			w.table.Add(401, proctest.Process{Start: startOf(401), PPID: 400, Name: "zsh"})

			res := w.run(caller{shellPID: 401, env: test.Env}, "acquire", "--checkout", w.dir("repo"))
			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, test.WantErr, test.WantSetup)
			wantAbsent(t, "stderr", res.Stderr, "--force")

			if len(w.leases()) != 0 {
				t.Error("an agent without a report wrote a lease")
			}
		})
	}
}
