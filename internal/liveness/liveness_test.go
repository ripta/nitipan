package liveness

import (
	"errors"
	"testing"

	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

var (
	localMachine = record.Machine{Host: "laptop", ID: "machine-1"}

	darwinStart = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1000, Usec: 5}

	linuxStart = record.ProcessStart{Platform: record.PlatformLinux, BootID: "boot-a", Ticks: 500, PIDNS: 4026531836}
)

func envelope(kind string, start record.ProcessStart) record.Envelope {
	return record.Envelope{
		Version:       record.Current,
		ID:            "01JAAAAAAAAAAAAAAAAAAAAAAA",
		ClearWhenDead: true,
		Liveness:      kind,
		Machine:       localMachine,
		Holder:        record.Holder{Agent: "claude", PID: 77, Start: start},
	}
}

type processTest struct {
	Name     string
	Platform string
	Procs    map[int]proctest.Process
	Env      record.Envelope
	Want     Status
}

var processTests = []processTest{
	{
		Name:     "darwin holder alive",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {Start: darwinStart}},
		Env:      envelope(record.LivenessProcess, darwinStart),
		Want:     Alive,
	},
	{
		Name:     "darwin holder gone",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{},
		Env:      envelope(record.LivenessProcess, darwinStart),
		Want:     Dead,
	},
	{
		Name:     "darwin pid reused",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {Start: record.ProcessStart{Platform: record.PlatformDarwin, Sec: 2000}}},
		Env:      envelope(record.LivenessProcess, darwinStart),
		Want:     Dead,
	},
	{
		Name:     "darwin pid reused within the same second",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {Start: record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1000, Usec: 6}}},
		Env:      envelope(record.LivenessProcess, darwinStart),
		Want:     Dead,
	},
	{
		Name:     "darwin process unreadable",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {StartErr: errors.New("operation not permitted")}},
		Env:      envelope(record.LivenessProcess, darwinStart),
		Want:     Unknown,
	},
	{
		Name:     "linux holder alive",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{77: {Start: linuxStart}},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Alive,
	},
	{
		Name:     "linux holder gone",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Dead,
	},
	{
		Name:     "linux pid reused",
		Platform: record.PlatformLinux,
		Procs: map[int]proctest.Process{77: {Start: record.ProcessStart{
			Platform: record.PlatformLinux, BootID: "boot-a", Ticks: 999, PIDNS: 4026531836,
		}}},
		Env:  envelope(record.LivenessProcess, linuxStart),
		Want: Dead,
	},
	{
		Name:     "linux holder from an earlier boot",
		Platform: record.PlatformLinux,
		Procs: map[int]proctest.Process{77: {Start: record.ProcessStart{
			Platform: record.PlatformLinux, BootID: "boot-b", Ticks: 500, PIDNS: 4026531836,
		}}},
		Env:  envelope(record.LivenessProcess, linuxStart),
		Want: Dead,
	},
	{
		Name:     "linux pid namespace mismatch",
		Platform: record.PlatformLinux,
		Procs: map[int]proctest.Process{77: {Start: record.ProcessStart{
			Platform: record.PlatformLinux, BootID: "boot-a", Ticks: 500, PIDNS: 4026532000,
		}}},
		Env:  envelope(record.LivenessProcess, linuxStart),
		Want: Unknown,
	},
	{
		Name:     "linux boot id or stat unreadable",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{77: {StartErr: errors.New("permission denied")}},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Unknown,
	},
	{
		Name:     "unknown process start platform",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {Start: darwinStart}},
		Env:      envelope(record.LivenessProcess, record.ProcessStart{Platform: "plan9"}),
		Want:     Unknown,
	},
	{
		Name:     "record written on another operating system",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{77: {Start: darwinStart}},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Unknown,
	},
	{
		Name:     "linux record from another PID namespace with the pid absent here",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{},
		Env: envelope(record.LivenessProcess, record.ProcessStart{
			Platform: record.PlatformLinux, BootID: "boot-a", Ticks: 500, PIDNS: 4026532000,
		}),
		Want: Unknown,
	},
	{
		Name:     "linux record with the pid absent and the reader's namespace unreadable",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{1: {StartErr: errors.New("permission denied")}},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Unknown,
	},
	{
		Name:     "unknown platform tag with the pid absent",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{},
		Env:      envelope(record.LivenessProcess, record.ProcessStart{Platform: "plan9"}),
		Want:     Unknown,
	},
	{
		Name:     "empty platform tag with the pid absent",
		Platform: record.PlatformLinux,
		Procs:    map[int]proctest.Process{},
		Env:      envelope(record.LivenessProcess, record.ProcessStart{}),
		Want:     Unknown,
	},
	{
		Name:     "record from another operating system with the pid absent",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{},
		Env:      envelope(record.LivenessProcess, linuxStart),
		Want:     Unknown,
	},
	{
		Name:     "no pid",
		Platform: record.PlatformDarwin,
		Procs:    map[int]proctest.Process{},
		Env: func() record.Envelope {
			e := envelope(record.LivenessProcess, darwinStart)
			e.Holder.PID = 0

			return e
		}(),
		Want: Unknown,
	},
}

func newTable(platform string, procs map[int]proctest.Process) *proctest.Table {
	table := proctest.New(platform, 1)

	// The reader itself exists, and on Linux its start carries the reader's PID namespace.
	table.Add(1, proctest.Process{Start: record.ProcessStart{
		Platform: platform, BootID: "boot-a", Ticks: 1, PIDNS: 4026531836, Sec: 1,
	}})

	for pid, p := range procs {
		table.Add(pid, p)
	}

	return table
}

func TestCheckProcessKind(t *testing.T) {
	for _, test := range processTests {
		t.Run(test.Name, func(t *testing.T) {
			checker := &Checker{Procs: newTable(test.Platform, test.Procs), Local: localMachine}

			got := checker.Check(test.Env)
			if got.Status != test.Want {
				t.Errorf("status = %v (%s), want %v", got.Status, got.Reason, test.Want)
			}

			if got.Reason == "" {
				t.Error("a verdict should carry a reason")
			}
		})
	}
}

type machineTest struct {
	Name   string
	Remote record.Machine
	Want   Status
}

var machineTests = []machineTest{
	{
		Name:   "same machine id and host",
		Remote: localMachine,
		Want:   Alive,
	},
	{
		Name:   "same machine id after a host name change",
		Remote: record.Machine{Host: "laptop-on-hotel-wifi", ID: "machine-1"},
		Want:   Alive,
	},
	{
		Name:   "different machine id with the same host name",
		Remote: record.Machine{Host: "laptop", ID: "machine-2"},
		Want:   Unknown,
	},
	{
		Name:   "different host with no remote machine id",
		Remote: record.Machine{Host: "other"},
		Want:   Unknown,
	},
	{
		Name:   "same host with no remote machine id",
		Remote: record.Machine{Host: "laptop"},
		Want:   Alive,
	},
}

func TestRecordFromAnotherMachineIsUnknown(t *testing.T) {
	for _, test := range machineTests {
		t.Run(test.Name, func(t *testing.T) {
			env := envelope(record.LivenessProcess, darwinStart)
			env.Machine = test.Remote

			checker := &Checker{
				Procs: newTable(record.PlatformDarwin, map[int]proctest.Process{77: {Start: darwinStart}}),
				Local: localMachine,
			}

			if got := checker.Check(env); got.Status != test.Want {
				t.Errorf("status = %v (%s), want %v", got.Status, got.Reason, test.Want)
			}
		})
	}
}

func TestRemoteRecordIsNeverReadDeadFromALocalPID(t *testing.T) {
	env := envelope(record.LivenessProcess, darwinStart)
	env.Machine = record.Machine{Host: "other", ID: "machine-9"}

	table := newTable(record.PlatformDarwin, nil)
	checker := &Checker{Procs: table, Local: localMachine}

	if got := checker.Check(env); got.Status != Unknown {
		t.Errorf("status = %v, want unknown", got.Status)
	}

	if len(table.StartCalls) != 0 {
		t.Errorf("a remote record caused local process reads: %v", table.StartCalls)
	}

	if checker.MayClear(env) {
		t.Error("a remote record must not be clearable")
	}
}

type fakeRule struct {
	result Result
	seen   int
}

func (f *fakeRule) Judge(record.Envelope) Result {
	f.seen++

	return f.result
}

type kindTest struct {
	Name     string
	Kind     string
	Agent    string
	Sessions map[string]SessionRule
	Want     Status
}

var kindTests = []kindTest{
	{
		Name: "absent kind",
		Kind: "",
		Want: Unknown,
	},
	{
		Name: "unknown kind",
		Kind: "lease-of-the-future",
		Want: Unknown,
	},
	{
		Name:  "session kind for an agent with no rule",
		Kind:  record.LivenessSession,
		Agent: "codex",
		Want:  Unknown,
	},
	{
		Name:     "session kind with a nil rule",
		Kind:     record.LivenessSession,
		Agent:    "codex",
		Sessions: map[string]SessionRule{"codex": nil},
		Want:     Unknown,
	},
	{
		Name:     "session kind with a rule asks the rule",
		Kind:     record.LivenessSession,
		Agent:    "codex",
		Sessions: map[string]SessionRule{"codex": &fakeRule{result: Result{Dead, "session ended"}}},
		Want:     Dead,
	},
}

func TestKindsThatCannotBeJudgedAreUnknown(t *testing.T) {
	for _, test := range kindTests {
		t.Run(test.Name, func(t *testing.T) {
			env := envelope(test.Kind, darwinStart)
			env.Holder.Agent = test.Agent

			// The PID does not exist, so judging by process would say dead.
			checker := &Checker{
				Procs:    newTable(record.PlatformDarwin, nil),
				Local:    localMachine,
				Sessions: test.Sessions,
			}

			if got := checker.Check(env); got.Status != test.Want {
				t.Errorf("status = %v (%s), want %v", got.Status, got.Reason, test.Want)
			}
		})
	}
}

func TestUnjudgeableRecordIsNeverCleared(t *testing.T) {
	for _, test := range kindTests {
		if test.Want != Unknown {
			continue
		}

		t.Run(test.Name, func(t *testing.T) {
			env := envelope(test.Kind, darwinStart)
			env.Holder.Agent = test.Agent

			checker := &Checker{Procs: newTable(record.PlatformDarwin, nil), Local: localMachine}
			if checker.MayClear(env) {
				t.Error("a record with unknown liveness was clearable")
			}
		})
	}
}

type clearTest struct {
	Name          string
	Alive         bool
	ClearWhenDead bool
	Want          bool
}

var clearTests = []clearTest{
	{Name: "dead and clearable", Alive: false, ClearWhenDead: true, Want: true},
	{Name: "dead but not clearable", Alive: false, ClearWhenDead: false, Want: false},
	{Name: "alive and clearable", Alive: true, ClearWhenDead: true, Want: false},
}

func TestMayClear(t *testing.T) {
	for _, test := range clearTests {
		t.Run(test.Name, func(t *testing.T) {
			procs := map[int]proctest.Process{}
			if test.Alive {
				procs[77] = proctest.Process{Start: darwinStart}
			}

			env := envelope(record.LivenessProcess, darwinStart)
			env.ClearWhenDead = test.ClearWhenDead

			checker := &Checker{Procs: newTable(record.PlatformDarwin, procs), Local: localMachine}
			if got := checker.MayClear(env); got != test.Want {
				t.Errorf("MayClear = %v, want %v", got, test.Want)
			}
		})
	}
}

func TestTreatAsAlive(t *testing.T) {
	for status, want := range map[Status]bool{Alive: true, Unknown: true, Dead: false} {
		if got := (Result{Status: status}).TreatAsAlive(); got != want {
			t.Errorf("%v.TreatAsAlive = %v, want %v", status, got, want)
		}
	}
}

func TestStatusStrings(t *testing.T) {
	for status, want := range map[Status]string{Alive: "alive", Dead: "dead", Unknown: "unknown"} {
		if got := status.String(); got != want {
			t.Errorf("String = %q, want %q", got, want)
		}
	}
}
