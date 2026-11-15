package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// codexEpoch puts Codex hosts well after the config files the tests write, which carry an older
// modification time.
const codexEpoch = 1_800_000_000

func codexStart(pid int) record.ProcessStart {
	return record.ProcessStart{Platform: record.PlatformDarwin, Sec: int64(codexEpoch + pid)}
}

var (
	codexDaemonArgs = []string{"/usr/local/bin/codex", "app-server", "--managed-daemon"}
	codexTUIArgs    = []string{"/usr/local/bin/codex", "resume"}
)

// goodCodexConfig sets the unload delay and runs this nitipan's SessionEnd hook.
const goodCodexConfig = `thread_unload_delay_secs = 60

[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = "/usr/local/bin/nitipan hook session-end"
timeout = 3
`

// codexHost adds a Codex process with the given arguments. No arguments means the managed daemon.
func (w *world) codexHost(pid int, args ...string) {
	w.t.Helper()

	if len(args) == 0 {
		args = codexDaemonArgs
	}

	w.table.Add(pid, proctest.Process{Start: codexStart(pid), PPID: 1, Name: "codex", Args: args})
}

// codexShell adds a shell under a host and returns the environment Codex gives a shell command of
// session t1.
func (w *world) codexShell(host, shell int) caller {
	w.table.Add(shell, proctest.Process{Start: startOf(shell), PPID: host, Name: "zsh"})

	return caller{shellPID: shell, env: map[string]string{
		"CODEX_SESSION_ID": "t1",
		"CODEX_THREAD_ID":  "t1",
	}}
}

// below adds a process under a host and returns it as a caller whose environment carries only the
// extra variables. A hook or plugin subprocess looks like this.
func (w *world) below(host, pid int, env map[string]string) caller {
	w.table.Add(pid, proctest.Process{Start: startOf(pid), PPID: host, Name: "sh"})

	if env == nil {
		env = map[string]string{}
	}

	return caller{shellPID: pid, env: env}
}

func (w *world) codexHome() string {
	return w.dir("codexhome")
}

// hookFor is a hook subprocess of a Codex host, which sees CODEX_HOME.
func (w *world) hookFor(host, pid int) caller {
	return w.below(host, pid, map[string]string{"CODEX_HOME": w.codexHome()})
}

// writeFile writes a file and gives it a modification time.
func (w *world) writeFile(path, content string, mtime time.Time) {
	w.t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}

	if err := os.Chtimes(path, mtime, mtime); err != nil {
		w.t.Fatal(err)
	}
}

// codexConfig writes config.toml under the Codex home, older than every host.
func (w *world) codexConfig(content string) {
	w.t.Helper()

	w.writeFile(filepath.Join(w.codexHome(), "config.toml"), content, time.Unix(codexEpoch-1000, 0))
}

// lockFile makes Codex's writer-lock file for thread t1 exist, or removes it.
func (w *world) lockFile(present bool) {
	w.t.Helper()

	path := filepath.Join(w.codexHome(), "thread-writer-locks", "t1.lock")

	if present {
		w.writeFile(path, "", time.Unix(codexEpoch, 0))

		return
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		w.t.Fatal(err)
	}
}

// hook runs a hook subcommand with its input.
func (w *world) hook(c caller, sub, session string, extra map[string]any) result {
	w.t.Helper()

	input := map[string]any{"session_id": session}
	for k, v := range extra {
		input[k] = v
	}

	data, err := json.Marshal(input)
	if err != nil {
		w.t.Fatal(err)
	}

	w.stdin = string(data)

	defer func() { w.stdin = "" }()

	return w.run(c, "hook", sub)
}

// tick moves the clock a minute, so two registrations never tie.
func (w *world) tick() {
	w.now = w.now.Add(time.Minute)
}

// sessionFiles lists the files in a sessions subdirectory.
func (w *world) sessionFiles(sub string) []string {
	w.t.Helper()

	entries, err := os.ReadDir(filepath.Join(w.state, "sessions", sub))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		w.t.Fatal(err)
	}

	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}

	return out
}

// registration reads a Codex registration file.
func (w *world) registration(name string) []byte {
	w.t.Helper()

	data, err := os.ReadFile(filepath.Join(w.state, "sessions", "registered", name))
	if err != nil {
		w.t.Fatal(err)
	}

	return data
}

// rewriteSessionFile changes fields of a session file in place, as a newer nitipan might have
// written it.
func (w *world) rewriteSessionFile(sub, name string, fields map[string]any) {
	w.t.Helper()

	path := filepath.Join(w.state, "sessions", sub, name)

	data, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		w.t.Fatal(err)
	}

	for k, v := range fields {
		body[k] = v
	}

	data, err = json.Marshal(body)
	if err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// registrationName is the file a Codex SessionStart under a host writes.
func registrationName(session string, host int) string {
	return "codex." + session + "." + strconv.Itoa(host) + "." + codexStart(host).String() + ".json"
}

func endMarkName(session string, host int) string {
	return "codex." + session + "." + strconv.Itoa(host) + "." + codexStart(host).String()
}

// livenessOf returns the liveness status of the lease with the ID, from list --json.
func (w *world) livenessOf(c caller, id string) (status, reason string) {
	w.t.Helper()

	leases, _ := w.listJSON(c)["leases"].([]any)

	for _, l := range leases {
		lease, _ := l.(map[string]any)
		if lease["id"] != id {
			continue
		}

		return statusOf(w.t, lease)
	}

	w.t.Fatalf("list has no lease %s", id)

	return "", ""
}

// firstLease returns the first lease of a list --json document.
func firstLease(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()

	leases, ok := doc["leases"].([]any)
	if !ok || len(leases) == 0 {
		t.Fatalf("list has no leases: %v", doc)
	}

	lease, ok := leases[0].(map[string]any)
	if !ok {
		t.Fatalf("lease is %T", leases[0])
	}

	return lease
}

// statusOf returns the liveness status of a lease of a list --json document.
func statusOf(t *testing.T, lease map[string]any) (status, reason string) {
	t.Helper()

	live, ok := lease["liveness"].(map[string]any)
	if !ok {
		t.Fatalf("lease has no liveness: %v", lease)
	}

	status, _ = live["status"].(string)
	reason, _ = live["reason"].(string)

	return status, reason
}

// tombstoneCount counts the files in tombstones/.
func (w *world) tombstoneCount() int {
	w.t.Helper()

	entries, err := os.ReadDir(filepath.Join(w.state, state.TombstonesDir))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}

	if err != nil {
		w.t.Fatal(err)
	}

	return len(entries)
}

// engineFor builds the engine a command run by the caller would use, for tests that hold the lock.
func (w *world) engineFor(c caller) *lease.Engine {
	w.t.Helper()

	var stdout, stderr bytes.Buffer

	a := &app{deps: w.deps(c, &stdout, &stderr).withDefaults()}

	e, err := a.engine()
	if err != nil {
		w.t.Fatal(err)
	}

	return e
}

// SuspendedAmong answers for several sessions in one read, which counts as one read.
func (f *fakeDB) SuspendedAmong(sessions []string, _ time.Duration) (map[string]bool, error) {
	f.reads++

	if f.err != nil {
		return nil, f.err
	}

	out := map[string]bool{}

	for _, s := range sessions {
		if f.suspended[s] {
			out[s] = true
		}
	}

	return out, nil
}

// fakeDB answers opencode's time_suspended from a map.
type fakeDB struct {
	suspended map[string]bool
	err       error
	reads     int
}

func (f *fakeDB) Suspended(session string) (bool, error) {
	f.reads++

	if f.err != nil {
		return false, f.err
	}

	return f.suspended[session], nil
}

var (
	opencodeServiceArgs = []string{"/opt/homebrew/bin/opencode", "serve", "--service"}
	opencodeTUIArgs     = []string{"/opt/homebrew/bin/opencode"}
)

// opencodeHost adds an opencode process.
func (w *world) opencodeHost(pid, parent int, args []string) {
	w.t.Helper()

	w.table.Add(pid, proctest.Process{Start: startOf(pid), PPID: parent, Name: "opencode", Args: args})
}

// opencodeShell adds a shell command under the service and returns the environment opencode gives it.
func (w *world) opencodeShell(host, shell int, session string) caller {
	w.table.Add(shell, proctest.Process{Start: startOf(shell), PPID: host, Name: "zsh"})

	return caller{shellPID: shell, env: map[string]string{
		"OPENCODE_SESSION_ID": session,
		"OPENCODE_TERMINAL":   "1",
	}}
}
