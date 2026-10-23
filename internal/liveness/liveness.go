// Package liveness decides whether the holder of a record is alive, dead, or unknown.
//
// Unknown counts as alive wherever it matters, so a claim is refused instead of stolen. A holder is
// dead only when this package can positively show its process is gone.
package liveness

import (
	"errors"
	"fmt"

	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
)

// Status is a liveness verdict.
type Status int

// Liveness verdicts. The zero value is Unknown.
const (
	Unknown Status = iota
	Alive
	Dead
)

// String names the status as `list --json` spells it.
func (s Status) String() string {
	switch s {
	case Alive:
		return "alive"
	case Dead:
		return "dead"
	default:
		return "unknown"
	}
}

// Result is a verdict with the reason behind it.
type Result struct {
	Status Status
	Reason string
}

// TreatAsAlive reports whether a caller must behave as if the holder were running.
func (r Result) TreatAsAlive() bool {
	return r.Status != Dead
}

// SessionRule judges a record whose liveness kind is "session" for one agent.
type SessionRule interface {
	Judge(env record.Envelope) Result
}

// HintingRule is a session rule that can also advise a person. The hint never changes the verdict.
// `list` shows it, as for a Codex session that probably ended.
type HintingRule interface {
	SessionRule

	JudgeHint(env record.Envelope) (Result, string)
}

// Checker judges holders.
type Checker struct {
	// Procs reads the local process table.
	Procs proc.Table

	// Local is the machine the checker runs on.
	Local record.Machine

	// Sessions holds the session rule of each agent that has one, keyed by agent name. An agent with no
	// rule reads unknown.
	Sessions map[string]SessionRule
}

// Check judges the holder named by an envelope.
func (c *Checker) Check(env record.Envelope) Result {
	if !SameMachine(c.Local, env.Machine) {
		return Result{Unknown, fmt.Sprintf("record is from machine %q", env.Machine.Host)}
	}

	switch env.Liveness {
	case record.LivenessProcess:
		return c.CheckProcess(env.Holder.PID, env.Holder.Start)
	case record.LivenessSession:
		rule, ok := c.Sessions[env.Holder.Agent]
		if !ok || rule == nil {
			return Result{Unknown, fmt.Sprintf("no session rule for agent %q", env.Holder.Agent)}
		}

		return rule.Judge(env)
	case "":
		return Result{Unknown, "record has no liveness kind"}
	default:
		return Result{Unknown, fmt.Sprintf("unknown liveness kind %q", env.Liveness)}
	}
}

// CheckHint is Check with the advice a hinting session rule gives. The hint is empty for every other
// record.
func (c *Checker) CheckHint(env record.Envelope) (verdict Result, hint string) {
	if env.Liveness == record.LivenessSession && SameMachine(c.Local, env.Machine) {
		if rule, ok := c.Sessions[env.Holder.Agent].(HintingRule); ok {
			return rule.JudgeHint(env)
		}
	}

	return c.Check(env), ""
}

// CheckProcess judges a local process by its PID and recorded start.
func (c *Checker) CheckProcess(pid int, start record.ProcessStart) Result {
	if pid <= 0 {
		return Result{Unknown, fmt.Sprintf("record has no usable pid %d", pid)}
	}

	cur, err := c.Procs.Start(pid)
	if errors.Is(err, proc.ErrNoProcess) {
		return c.absent(pid, start)
	}

	if err != nil {
		return Result{Unknown, fmt.Sprintf("cannot read process %d: %v", pid, err)}
	}

	return Compare(start, cur)
}

// absent judges a PID that does not exist locally. Absence proves death only when this reader could
// have seen the holder: the record's platform is known and is the reader's own, and on Linux the
// record was written in the reader's PID namespace. Otherwise the holder may be running where this
// reader cannot see it.
func (c *Checker) absent(pid int, start record.ProcessStart) Result {
	switch {
	case !start.Known():
		return Result{Unknown, fmt.Sprintf("unknown process start platform %q", start.Platform)}
	case start.Platform != c.Procs.Platform():
		return Result{Unknown, fmt.Sprintf("record start is %s, this machine reads %s", start.Platform, c.Procs.Platform())}
	case start.Platform == record.PlatformLinux:
		self, err := c.Procs.Start(c.Procs.Self())
		if err != nil {
			return Result{Unknown, fmt.Sprintf("cannot read this process's PID namespace: %v", err)}
		}

		if self.PIDNS != start.PIDNS {
			return Result{Unknown, "holder is in another PID namespace"}
		}
	}

	return Result{Dead, fmt.Sprintf("no process %d", pid)}
}

// MayClear reports whether a reader may clear a record: its holder is dead and the record allows it.
func (c *Checker) MayClear(env record.Envelope) bool {
	return env.ClearWhenDead && c.Check(env).Status == Dead
}

// Compare compares the start recorded for a PID with the start of the process holding that PID now.
//
// The reader's view on Linux carries the boot ID and PID namespace of the reader. A different
// namespace cannot see the holder, so the answer is unknown. A different boot means the holder's
// process is long gone. The same boot and namespace with other ticks is a reused PID.
func Compare(recorded, current record.ProcessStart) Result {
	switch {
	case !recorded.Known():
		return Result{Unknown, fmt.Sprintf("unknown process start platform %q", recorded.Platform)}
	case recorded.Platform != current.Platform:
		return Result{Unknown, fmt.Sprintf("record start is %s, this machine reads %s", recorded.Platform, current.Platform)}
	case recorded.Platform == record.PlatformDarwin:
		return compareDarwin(recorded, current)
	default:
		return compareLinux(recorded, current)
	}
}

func compareDarwin(recorded, current record.ProcessStart) Result {
	if recorded.Sec == current.Sec && recorded.Usec == current.Usec {
		return Result{Alive, "process start matches"}
	}

	return Result{Dead, "pid was reused by a process with another start time"}
}

func compareLinux(recorded, current record.ProcessStart) Result {
	if recorded.PIDNS != current.PIDNS {
		return Result{Unknown, "holder is in another PID namespace"}
	}

	if recorded.BootID != current.BootID {
		return Result{Dead, "holder started in an earlier boot"}
	}

	if recorded.Ticks == current.Ticks {
		return Result{Alive, "process start matches"}
	}

	return Result{Dead, "pid was reused by a process with another start time"}
}

// SameMachine reports whether a record was written on the local machine. The machine ID decides
// when both sides have one, and the host name is the fallback. A laptop whose host name follows
// the network still recognizes its own records.
func SameMachine(local, remote record.Machine) bool {
	if local.ID != "" && remote.ID != "" {
		return local.ID == remote.ID
	}

	return local.Host != "" && local.Host == remote.Host
}
