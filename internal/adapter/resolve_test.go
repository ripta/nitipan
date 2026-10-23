package adapter

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

func start(sec int64) record.ProcessStart {
	return record.ProcessStart{Platform: record.PlatformDarwin, Sec: sec}
}

// tree builds a process table: nitipan (100) under a shell (90) under whatever the test adds.
func tree(extra func(table *proctest.Table)) *proctest.Table {
	table := proctest.New(record.PlatformDarwin, 100)
	table.Add(100, proctest.Process{Start: start(100), PPID: 90, Name: "nitipan", Args: []string{"nitipan", "list"}})
	table.Add(90, proctest.Process{Start: start(90), PPID: 80, Name: "zsh", Args: []string{"zsh"}})

	extra(table)

	return table
}

func resolver(table *proctest.Table, env map[string]string) *Resolver {
	return &Resolver{
		Adapters: All(),
		Procs:    table,
		Getenv:   func(k string) string { return env[k] },
		User:     func() (string, error) { return "ripta", nil },
	}
}

func TestResolveClaudeByPIDVariable(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "node", Args: []string{"node", "claude"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	res := resolver(table, map[string]string{"CLAUDE_PID": "80", "CLAUDE_CODE_SESSION_ID": "sess-1"}).Resolve()

	holder, err := res.RequireHolder()
	if err != nil {
		t.Fatalf("RequireHolder: %v", err)
	}

	if holder.Adapter.Name != "claude" || holder.PID != 80 || holder.SessionID != "sess-1" || holder.User != "ripta" {
		t.Errorf("holder = %+v", holder)
	}

	if !holder.Start.Equal(start(80)) {
		t.Errorf("start = %+v", holder.Start)
	}

	if holder.MissingSession() {
		t.Error("a single-session agent never lacks a session")
	}

	rec := holder.Record()
	if rec.Agent != "claude" || rec.PID != 80 || rec.SessionID != "sess-1" {
		t.Errorf("record holder = %+v", rec)
	}
}

func TestResolveNearestAgentWinsOverInheritedOuterAgent(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{
			Start: start(80), PPID: 70, Name: "codex",
			Args: []string{"/usr/local/bin/codex", "app-server", "--managed-daemon"},
		})
		table.Add(70, proctest.Process{Start: start(70), PPID: 1, Name: "node", Args: []string{"node", "claude"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	env := map[string]string{
		"CLAUDE_PID":             "70",
		"CLAUDE_CODE_SESSION_ID": "outer-claude",
		"CODEX_SESSION_ID":       "inner-codex",
		"CODEX_THREAD_ID":        "inner-codex",
	}

	holder, err := resolver(table, env).Resolve().RequireHolder()
	if err != nil {
		t.Fatalf("RequireHolder: %v", err)
	}

	if holder.Adapter.Name != "codex" || holder.PID != 80 || holder.SessionID != "inner-codex" {
		t.Errorf("holder = %+v, want the nearer codex host and its session", holder)
	}

	if !holder.Adapter.MultiSession {
		t.Error("codex is declared multi-session")
	}
}

func TestResolveOuterAgentIsFoundWhenNoNearerAgentExists(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 70, Name: "bash", Args: []string{"bash"}})
		table.Add(70, proctest.Process{Start: start(70), PPID: 1, Name: "node", Args: []string{"node", "claude"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	holder, err := resolver(table, map[string]string{"CLAUDE_PID": "70", "CLAUDE_CODE_SESSION_ID": "s"}).
		Resolve().RequireHolder()
	if err != nil || holder.PID != 70 {
		t.Errorf("holder = %+v, %v, want PID 70", holder, err)
	}
}

type walkFailureTest struct {
	Name      string
	Env       map[string]string
	Setup     func(table *proctest.Table)
	WantAs    func(err error) bool
	WantInMsg string
}

var walkFailureTests = []walkFailureTest{
	{
		Name: "pid variable names no ancestor",
		Env:  map[string]string{"CLAUDE_PID": "55", "CLAUDE_CODE_SESSION_ID": "s"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "launchd", Args: []string{"x"}})
			table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
		},
		WantAs: func(err error) bool {
			var target *PIDNotAncestorError

			return errors.As(err, &target) && target.Var == "CLAUDE_PID"
		},
		WantInMsg: "CLAUDE_PID=55",
	},
	{
		Name: "pid variable is not a number",
		Env:  map[string]string{"CLAUDE_PID": "soon"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "x", Args: []string{"x"}})
			table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
		},
		WantAs: func(err error) bool {
			var target *PIDNotAncestorError

			return errors.As(err, &target)
		},
		WantInMsg: "CLAUDE_PID",
	},
	{
		Name: "stray codex variables with no codex ancestor",
		Env:  map[string]string{"CODEX_SESSION_ID": "s", "CODEX_THREAD_ID": "s"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "tmux", Args: []string{"tmux"}})
			table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
		},
		WantAs: func(err error) bool {
			var target *StrayVariablesError

			return errors.As(err, &target) && reflect.DeepEqual(target.Vars, []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID"})
		},
		WantInMsg: "CODEX_SESSION_ID, CODEX_THREAD_ID",
	},
	{
		Name: "an ancestor that cannot be read",
		Env:  map[string]string{"CLAUDE_PID": "70"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), PPID: 70, ArgsErr: errors.New("permission denied")})
			table.Add(70, proctest.Process{Start: start(70), PPID: 1, Name: "node"})
			table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
		},
		WantAs: func(err error) bool {
			var target *UnreadableAncestorError

			return errors.As(err, &target) && target.PID == 80 && reflect.DeepEqual(target.Vars, []string{"CLAUDE_PID"})
		},
		WantInMsg: "agent variables CLAUDE_PID could not be matched",
	},
	{
		Name: "an ancestor whose parent cannot be read",
		Env:  map[string]string{"CLAUDE_PID": "70"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), ParentErr: errors.New("not permitted"), Args: []string{"x"}})
		},
		WantAs: func(err error) bool {
			var target *UnreadableAncestorError

			return errors.As(err, &target) && target.PID == 80
		},
		WantInMsg: "ancestor process 80: not permitted; agent variables CLAUDE_PID could not be matched",
	},
	{
		Name: "stray variables and an ancestor that cannot be read",
		Env:  map[string]string{"CODEX_SESSION_ID": "s", "CODEX_THREAD_ID": "s"},
		Setup: func(table *proctest.Table) {
			table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "login", ArgsErr: errors.New("operation not permitted")})
		},
		WantAs: func(err error) bool {
			var target *UnreadableAncestorError

			return errors.As(err, &target) && target.PID == 80
		},
		WantInMsg: "CODEX_SESSION_ID, CODEX_THREAD_ID could not be matched",
	},
}

func TestResolveFailsClosed(t *testing.T) {
	for _, test := range walkFailureTests {
		t.Run(test.Name, func(t *testing.T) {
			res := resolver(tree(test.Setup), test.Env).Resolve()

			if res.Holder != nil {
				t.Fatalf("holder = %+v, want none", res.Holder)
			}

			if !errors.Is(res.Failure, ErrFailClosed) || !test.WantAs(res.Failure) {
				t.Fatalf("failure = %v", res.Failure)
			}

			if !strings.Contains(res.Failure.Error(), test.WantInMsg) {
				t.Errorf("message %q does not name %q", res.Failure, test.WantInMsg)
			}

			if _, err := res.RequireHolder(); !errors.Is(err, ErrFailClosed) {
				t.Errorf("RequireHolder = %v, want a fail-closed error", err)
			}
		})
	}
}

func TestStrayVariablesStillLetListAndBreakRunAnonymously(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "tmux", Args: []string{"tmux"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	res := resolver(table, map[string]string{"CODEX_SESSION_ID": "s"}).Resolve()

	caller, err := res.Caller()
	if err != nil {
		t.Fatalf("Caller: %v", err)
	}

	if caller.Holder != nil {
		t.Errorf("caller holder = %+v, want anonymous", caller.Holder)
	}

	got := caller.Anonymous
	if got.User != "ripta" || got.PID != 90 || got.Name != "zsh" || !got.Start.Equal(start(90)) {
		t.Errorf("anonymous = %+v, want the shell as ripta", got)
	}

	if res.Failure == nil {
		t.Error("holder commands should still fail closed")
	}
}

func TestResolveWithNoAgentVariablesIsAnonymousAndNeverWalks(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		// An unreadable ancestor would fail a walk. It must not matter here.
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, ArgsErr: errors.New("denied"), ParentErr: errors.New("denied")})
	})

	res := resolver(table, map[string]string{"AGENT": "1", "AI_AGENT": "someone"}).Resolve()

	if res.Holder != nil || res.Failure != nil {
		t.Fatalf("resolution = %+v, want a plain anonymous caller", res)
	}

	_, err := res.RequireHolder()
	if !errors.Is(err, ErrNoAdapter) {
		t.Fatalf("RequireHolder = %v, want ErrNoAdapter", err)
	}

	for _, name := range []string{"claude", "codex", "opencode"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}

	caller, err := res.Caller()
	if err != nil || caller.Anonymous.PID != 90 || caller.Anonymous.Name != "zsh" {
		t.Errorf("caller = %+v, %v", caller, err)
	}

	rec := caller.Anonymous.Record()
	if rec.Agent != "" || rec.User != "ripta" || rec.PID != 90 {
		t.Errorf("anonymous record = %+v", rec)
	}
}

func TestResolveSkipsUnreadableInitProcess(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "bash", Args: []string{"bash"}})
		table.Add(1, proctest.Process{Start: start(1), ArgsErr: errors.New("operation not permitted")})
	})

	res := resolver(table, map[string]string{"CODEX_SESSION_ID": "s"}).Resolve()

	var stray *StrayVariablesError
	if !errors.As(res.Failure, &stray) {
		t.Errorf("failure = %v, want the stray-variables error, not an unreadable-init error", res.Failure)
	}
}

func TestResolveStopsWhenAnAncestorVanishes(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 70, Name: "bash", Args: []string{"bash"}})
	})

	res := resolver(table, map[string]string{"CLAUDE_PID": "70"}).Resolve()

	var notAncestor *PIDNotAncestorError
	if !errors.As(res.Failure, &notAncestor) {
		t.Errorf("failure = %v, want the PID-variable error", res.Failure)
	}
}

func TestResolveStopsOnAParentCycle(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 80, Name: "bash", Args: []string{"bash"}})
	})

	res := resolver(table, map[string]string{"CLAUDE_PID": "70"}).Resolve()
	if res.Failure == nil || res.Holder != nil {
		t.Errorf("resolution = %+v, want a fail-closed result", res)
	}
}

func TestResolveOpencodeBySignature(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{
			Start: start(80), PPID: 1, Name: "opencode",
			Args: []string{"opencode", "serve", "--service"},
		})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	holder, err := resolver(table, map[string]string{"OPENCODE_SESSION_ID": "ses_1"}).Resolve().RequireHolder()
	if err != nil {
		t.Fatalf("RequireHolder: %v", err)
	}

	if holder.Adapter.Name != "opencode" || holder.PID != 80 || holder.SessionID != "ses_1" {
		t.Errorf("holder = %+v", holder)
	}
}

func TestMultiSessionAgentWithoutSessionIDIsFlagged(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "codex", Args: []string{"codex"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	// CODEX_THREAD_ID survives an environment policy that strips CODEX_SESSION_ID.
	holder, err := resolver(table, map[string]string{"CODEX_THREAD_ID": "thr"}).Resolve().RequireHolder()
	if err != nil {
		t.Fatalf("RequireHolder: %v", err)
	}

	if !holder.MissingSession() {
		t.Errorf("holder = %+v, want a missing session", holder)
	}
}

func TestResolveReportsUnreadableAnonymousIdentity(t *testing.T) {
	table := proctest.New(record.PlatformDarwin, 100)
	table.Add(100, proctest.Process{PPID: 90})

	res := resolver(table, nil).Resolve()

	if _, err := res.Caller(); !errors.Is(err, proc.ErrNoProcess) {
		t.Errorf("Caller = %v, want the unreadable parent", err)
	}
}

type signatureTest struct {
	Name string
	Args []string
	Mode string
	Serv bool
}

var codexModeTests = []signatureTest{
	{Name: "tui", Args: []string{"/opt/homebrew/bin/codex"}, Mode: CodexTUI},
	{Name: "tui with flags", Args: []string{"codex", "--no-daemon", "resume"}, Mode: CodexTUI},
	{Name: "daemon", Args: []string{"codex", "app-server", "--managed-daemon"}, Mode: CodexDaemon},
	{Name: "ide extension app-server", Args: []string{"codex", "app-server", "--stdio"}, Mode: CodexDaemon},
	{Name: "unrecognized host", Args: []string{"node", "codex.js"}, Mode: CodexDaemon},
	{Name: "empty", Args: nil, Mode: CodexDaemon},
}

func TestCodexHostMode(t *testing.T) {
	for _, test := range codexModeTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := CodexHostMode(test.Args); got != test.Mode {
				t.Errorf("CodexHostMode(%q) = %q, want %q", test.Args, got, test.Mode)
			}
		})
	}
}

var opencodeTests = []signatureTest{
	{Name: "service", Args: []string{"opencode", "serve", "--service"}, Serv: true},
	{Name: "standalone stdio", Args: []string{"opencode", "serve", "--stdio", "--port", "0"}, Serv: true},
	{Name: "tui", Args: []string{"/usr/local/bin/opencode"}, Serv: false},
	{Name: "run", Args: []string{"opencode", "run", "hi"}, Serv: false},
	{Name: "not opencode", Args: []string{"serve", "serve"}, Serv: false},
}

func TestOpencodeIsServe(t *testing.T) {
	for _, test := range opencodeTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := OpencodeIsServe(test.Args); got != test.Serv {
				t.Errorf("OpencodeIsServe(%q) = %v, want %v", test.Args, got, test.Serv)
			}
		})
	}
}

func TestHostWalksEvenWithNoAgentVariableAndNeverReadsTheSessionFromTheEnvironment(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{
			Start: start(80), PPID: 1, Name: "codex",
			Args: []string{"/usr/local/bin/codex", "app-server", "--managed-daemon"},
		})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	// The environment carries a session ID for the hook to ignore, and no marker at all otherwise.
	host, err := resolver(table, map[string]string{"CODEX_SESSION_ID": "from-the-environment"}).Host()
	if err != nil {
		t.Fatalf("Host: %v", err)
	}

	if host.Adapter.Name != "codex" || host.PID != 80 || host.SessionID != "" || host.User != "ripta" {
		t.Errorf("host = %+v", host)
	}

	// Resolve never walks without a variable. Host does.
	bare, err := resolver(table, nil).Host()
	if err != nil || bare.PID != 80 {
		t.Errorf("host with no variables = %+v, %v", bare, err)
	}
}

func TestHostFailsClosedWhenNoAncestorIsAnAgent(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "bash", Args: []string{"bash"}})
		table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
	})

	_, err := resolver(table, nil).Host()
	if !errors.Is(err, ErrFailClosed) {
		t.Errorf("err = %v, want ErrFailClosed", err)
	}
}

func TestHostFailsClosedOnAnUnreadableAncestor(t *testing.T) {
	table := tree(func(table *proctest.Table) {
		table.Add(80, proctest.Process{
			Start: start(80), PPID: 1, Name: "bash", ArgsErr: errors.New("permission denied"),
		})
	})

	_, err := resolver(table, nil).Host()

	var unreadable *UnreadableAncestorError
	if !errors.As(err, &unreadable) {
		t.Errorf("err = %v, want an unreadable ancestor", err)
	}
}

func TestHostFindsTheOpencodeTUIAndTheServiceBySignature(t *testing.T) {
	for _, test := range []struct {
		Name string
		Args []string
		Serv bool
	}{
		{"service", []string{"/opt/homebrew/bin/opencode", "serve", "--service"}, true},
		{"standalone child", []string{"/opt/homebrew/bin/opencode", "serve", "--stdio", "--port", "0"}, true},
		{"tui", []string{"/opt/homebrew/bin/opencode"}, false},
		{"run is also a TUI signature", []string{"/opt/homebrew/bin/opencode", "run", "hi"}, false},
		{"acp is also a TUI signature", []string{"/opt/homebrew/bin/opencode", "acp"}, false},
	} {
		t.Run(test.Name, func(t *testing.T) {
			table := tree(func(table *proctest.Table) {
				table.Add(80, proctest.Process{Start: start(80), PPID: 1, Name: "opencode", Args: test.Args})
				table.Add(1, proctest.Process{Start: start(1), Name: "launchd"})
			})

			host, err := resolver(table, nil).Host()
			if err != nil || host.Adapter.Name != "opencode" || host.PID != 80 {
				t.Fatalf("host = %+v, %v", host, err)
			}

			if OpencodeIsServe(test.Args) != test.Serv {
				t.Errorf("OpencodeIsServe = %v", !test.Serv)
			}
		})
	}
}

func TestAdaptersListTheirPermissionModes(t *testing.T) {
	want := map[string][]string{
		"claude":   {"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"},
		"codex":    {"read-only", "workspace-write", "danger-full-access", "on-request", "never"},
		"opencode": {"build", "plan", "auto"},
	}

	for _, a := range All() {
		if !reflect.DeepEqual(a.PermissionModes, want[a.Name]) {
			t.Errorf("%s permission modes = %v, want %v", a.Name, a.PermissionModes, want[a.Name])
		}
	}
}

func TestAdaptersDeclareSessionModel(t *testing.T) {
	want := map[string]bool{"claude": false, "codex": true, "opencode": true}

	for _, a := range All() {
		if a.MultiSession != want[a.Name] {
			t.Errorf("%s multi-session = %v, want %v", a.Name, a.MultiSession, want[a.Name])
		}
	}
}
