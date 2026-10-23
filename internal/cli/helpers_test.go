package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

const (
	selfPID = 100
	tester  = "tester"
)

var testMachine = record.Machine{Host: "laptop", ID: "machine-1"}

func startOf(pid int) record.ProcessStart {
	return record.ProcessStart{Platform: record.PlatformDarwin, Sec: int64(pid)}
}

// fakeFinder reports the listed directories as checkout boundaries.
type fakeFinder map[string]bool

func (f fakeFinder) IsBoundary(dir string) bool { return f[dir] }

// caller is one shell command's place in the fake process tree.
type caller struct {
	shellPID int
	env      map[string]string
}

func (c caller) withEnv(key, value string) caller {
	env := map[string]string{}
	for k, v := range c.env {
		env[k] = v
	}

	env[key] = value
	c.env = env

	return c
}

type result struct {
	Status int
	Stdout string
	Stderr string
}

// world is a state directory, a fake process table, and the commands run against them.
type world struct {
	t      *testing.T
	root   string
	state  string
	table  *proctest.Table
	finder fakeFinder
	hooks  lease.Hooks
	budget time.Duration
	home   func() (string, error)
	now    time.Time

	// stdin is the input the next command reads, as a hook would get it.
	stdin string

	// exe is the nitipan path the hook entries name.
	exe string

	// db answers opencode's time_suspended. Nil makes every read fail.
	db session.OpencodeDB
}

func newWorld(t *testing.T) *world {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	table := proctest.New(record.PlatformDarwin, selfPID)
	table.Add(selfPID, proctest.Process{Start: startOf(selfPID)})

	w := &world{
		t:      t,
		root:   root,
		state:  filepath.Join(root, "state", "nitipan"),
		table:  table,
		finder: fakeFinder{},
		budget: 60 * time.Millisecond,
		now:    time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		exe:    "/usr/local/bin/nitipan",
	}

	return w
}

// claude adds a Claude Code process and a shell under it, and returns the shell's environment.
func (w *world) claude(pid int, session string) caller {
	w.table.Add(pid, proctest.Process{Start: startOf(pid), PPID: 1, Name: "claude"})
	w.table.Add(pid+1, proctest.Process{Start: startOf(pid + 1), PPID: pid, Name: "zsh"})

	return caller{shellPID: pid + 1, env: map[string]string{
		"CLAUDE_PID":             strconv.Itoa(pid),
		"CLAUDE_CODE_SESSION_ID": session,
	}}
}

// terminal adds a shell with no agent above it and no agent variables.
func (w *world) terminal(pid int) caller {
	w.table.Add(pid, proctest.Process{Start: startOf(pid), PPID: 1, Name: "zsh"})

	return caller{shellPID: pid, env: map[string]string{}}
}

// stray adds a shell that inherited an agent's variables but has no agent above it.
func (w *world) stray(pid int) caller {
	c := w.terminal(pid)
	c.env = map[string]string{"CLAUDE_CODE_SESSION_ID": "leftover"}

	return c
}

// kill removes a process, so any holder with that PID reads dead.
func (w *world) kill(pid int) {
	delete(w.table.Procs, pid)
}

func (w *world) deps(c caller, stdout, stderr *bytes.Buffer) Deps {
	w.table.Procs[selfPID].PPID = c.shellPID

	stateDir := func() (string, error) { return w.state, nil }
	if w.home != nil {
		stateDir = w.home
	}

	return Deps{
		Stdout:       stdout,
		Stderr:       stderr,
		Stdin:        strings.NewReader(w.stdin),
		Executable:   func() (string, error) { return w.exe, nil },
		UserHome:     func() (string, error) { return filepath.Join(w.root, "userhome"), nil },
		OpencodeDB:   w.db,
		Getenv:       func(k string) string { return c.env[k] },
		Procs:        w.table,
		StateDir:     stateDir,
		Machine:      func() (record.Machine, error) { return testMachine, nil },
		User:         func() (string, error) { return tester, nil },
		Cwd:          func() (string, error) { return w.root, nil },
		Now:          func() time.Time { return w.now },
		Finder:       w.finder,
		StateOptions: state.Options{LockBudget: w.budget, LockPoll: 10 * time.Millisecond},
		Hooks:        w.hooks,
	}
}

func (w *world) run(c caller, args ...string) result {
	w.t.Helper()

	var stdout, stderr bytes.Buffer

	status := Execute(w.deps(c, &stdout, &stderr), args)

	return result{Status: status, Stdout: stdout.String(), Stderr: stderr.String()}
}

// dir makes a directory under the world's root and returns its path.
func (w *world) dir(parts ...string) string {
	w.t.Helper()

	p := filepath.Join(append([]string{w.root}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		w.t.Fatal(err)
	}

	return p
}

func (w *world) leasesDir() string {
	return filepath.Join(w.state, state.LeasesDir)
}

// leases decodes every published record, in lease ID order.
func (w *world) leases() []*record.Record {
	w.t.Helper()

	entries, err := os.ReadDir(w.leasesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		w.t.Fatal(err)
	}

	var out []*record.Record

	for _, e := range entries {
		if e.Name()[0] == '.' {
			continue
		}

		data, err := os.ReadFile(filepath.Join(w.leasesDir(), e.Name()))
		if err != nil {
			w.t.Fatal(err)
		}

		rec, err := record.Decode(data)
		if err != nil {
			w.t.Fatalf("decoding %s: %v", e.Name(), err)
		}

		out = append(out, rec)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func (w *world) leaseByID(id string) *record.Record {
	w.t.Helper()

	for _, r := range w.leases() {
		if r.ID == id {
			return r
		}
	}

	return nil
}

// plantSpec describes a record written straight into the state directory.
type plantSpec struct {
	ID      string
	PID     int
	Paths   []string
	Version string

	// Resource plants a record of kind resource on this name, with a footprint and body that agree.
	// Paths stays empty.
	Resource string

	// KeepAfterDeath is true to write clear_when_dead false.
	KeepAfterDeath bool

	Machine record.Machine
	Name    string
	Handle  string
	Agent   string
	Kind    string

	// Liveness is the liveness kind. Empty means process.
	Liveness string

	// SessionID is the holder's session.
	SessionID string
}

// plant writes a record for another holder and returns its ID.
func (w *world) plant(spec plantSpec) string {
	w.t.Helper()

	if spec.ID == "" {
		spec.ID = state.NewID()
	}

	if spec.Version == "" {
		spec.Version = "1.0"

		if spec.Resource != "" {
			spec.Version = "1.1"
		}
	}

	if spec.Machine == (record.Machine{}) {
		spec.Machine = testMachine
	}

	if spec.Agent == "" {
		spec.Agent = "claude"
	}

	if spec.Kind == "" {
		spec.Kind = record.KindPath

		if spec.Resource != "" {
			spec.Kind = record.KindResource
		}
	}

	if spec.Liveness == "" {
		spec.Liveness = record.LivenessProcess
	}

	footprint := []map[string]any{}
	domainKeys := []map[string]any{}

	for _, p := range spec.Paths {
		footprint = append(footprint, map[string]any{"type": "path", "path": p})
		domainKeys = append(domainKeys, map[string]any{"path": p})
	}

	domain := map[string]any{"kind": spec.Kind}

	switch {
	case spec.Resource != "":
		footprint = append(footprint, map[string]any{"type": "name", "name": spec.Resource})
		domain["name"] = spec.Resource
		domain["slots"] = 1
	default:
		domain["checkout"] = spec.Paths[0]
		domain["keys"] = domainKeys
	}

	doc := map[string]any{
		"version":         spec.Version,
		"id":              spec.ID,
		"footprint":       footprint,
		"clear_when_dead": !spec.KeepAfterDeath,
		"liveness":        spec.Liveness,
		"machine":         map[string]any{"host": spec.Machine.Host, "id": spec.Machine.ID},
		"holder": map[string]any{
			"agent": spec.Agent, "pid": spec.PID, "handle": spec.Handle, "name": spec.Name, "session_id": spec.SessionID,
			"start": map[string]any{"platform": "darwin", "sec": spec.PID},
		},
		"reach":       "machine",
		"domain":      domain,
		"acting_for":  "someone",
		"acquired_at": "2026-10-19T12:00:00Z",
	}

	data, err := json.Marshal(doc)
	if err != nil {
		w.t.Fatal(err)
	}

	w.writeLeaseFile(spec.ID+".json", data)

	return spec.ID
}

func (w *world) writeLeaseFile(name string, data []byte) {
	w.t.Helper()

	if err := os.MkdirAll(w.leasesDir(), 0o700); err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(w.leasesDir(), name), data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// listJSON runs `list --json` and decodes the document.
func (w *world) listJSON(c caller, args ...string) map[string]any {
	w.t.Helper()

	res := w.run(c, append([]string{"list", "--json"}, args...)...)
	if res.Status != 0 {
		w.t.Fatalf("list --json: status %d, stderr %q", res.Status, res.Stderr)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		w.t.Fatalf("list --json is not json: %v\n%s", err, res.Stdout)
	}

	return doc
}
