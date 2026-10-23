package session

import (
	"fmt"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

// maxUnloadDelaySecs is the longest thread_unload_delay_secs that acquire accepts. Codex fires
// SessionEnd only when it unloads a thread, which happens this many seconds after the last client
// leaves. The default is 1800.
const maxUnloadDelaySecs = 60

// codexRule judges a Codex session from its registrations, end marks, and writer-lock file.
type codexRule struct {
	s *Service
}

// Judge implements liveness.SessionRule.
func (r codexRule) Judge(env record.Envelope) liveness.Result {
	res, _ := r.JudgeHint(env)

	return res
}

// JudgeHint implements liveness.HintingRule.
//
// A session is judged by its newest registration whose host is alive. It is dead when an end mark
// names it under that host and Codex's writer-lock file for it is absent. While the file exists the
// session is alive, which guards against a late SessionEnd that lands after a resume.
func (r codexRule) JudgeHint(env record.Envelope) (verdict liveness.Result, hint string) {
	session := env.Holder.SessionID
	if !ValidSession(session) {
		return unknown("the lease has no usable session ID"), ""
	}

	regs, err := r.s.registrations(session)
	if err != nil {
		return unknown("cannot read the Codex registrations: %v", err), ""
	}

	for _, rf := range regs {
		if rf.Err != nil {
			return unknown("session file %s cannot be used: %v", rf.Key.FileName(), rf.Err), ""
		}
	}

	if len(regs) == 0 {
		return unknown("no registration for session %s", session), ""
	}

	n, ok := pickRegistration(regs)
	if !ok {
		return unknown("cannot tell whether the host of session %s is alive", session), ""
	}

	switch n.Host.Status {
	case liveness.Alive:
		return r.alive(n, session)
	case liveness.Dead:
		return r.hostDead(n), ""
	default:
		return unknown("cannot tell whether host %d is alive: %s", n.Reg.Host.PID, n.Host.Reason), ""
	}
}

// pickRegistration chooses the newest registration whose host is alive. With none, it chooses the
// newest overall, unless some host could not be read, because that host may be alive.
func pickRegistration(regs []regFile) (regFile, bool) {
	var live []regFile

	for _, rf := range regs {
		if rf.Host.Status == liveness.Alive {
			live = append(live, rf)
		}
	}

	if len(live) > 0 {
		return newest(live), true
	}

	for _, rf := range regs {
		if rf.Host.Status == liveness.Unknown {
			return regFile{}, false
		}
	}

	return newest(regs), true
}

func (r codexRule) alive(n regFile, session string) (verdict liveness.Result, hint string) {
	lock, lockErr := WriterLock(n.Reg.CodexHome, session)

	mark, err := r.s.endMark(n.Key)
	if err != nil {
		return unknown("%v", err), ""
	}

	if !n.Reg.Ended && !mark {
		res := liveness.Result{Status: liveness.Alive, Reason: fmt.Sprintf("registered under host %d", n.Reg.Host.PID)}

		if lock == FileAbsent {
			return res, "likely ended: Codex's writer-lock file for this session is gone, though its host still runs. " +
				"Nothing was cleared"
		}

		return res, ""
	}

	switch lock {
	case FilePresent:
		return liveness.Result{Status: liveness.Alive, Reason: "an end was recorded, but Codex's writer-lock file " +
			"still exists, so the session was resumed"}, ""
	case FileAbsent:
		return liveness.Result{Status: liveness.Dead, Reason: "the session ended and Codex's writer-lock file is gone"}, ""
	default:
		return unknown("an end was recorded, and the writer-lock file cannot be checked: %v", lockErr), ""
	}
}

// hostDead judges a session whose newest registration has a dead host. An embedded host is its own
// TUI, so nothing can reconnect to it. A daemon restarts, and a resumed session registers under the
// new host, so a daemon's death reads unknown unless the session already ended.
func (r codexRule) hostDead(n regFile) liveness.Result {
	switch {
	case n.Reg.Ended:
		return liveness.Result{Status: liveness.Dead, Reason: "the session ended before its host exited"}
	case n.Reg.Mode == ModeTUI:
		return liveness.Result{Status: liveness.Dead, Reason: fmt.Sprintf("the embedded Codex TUI %d exited",
			n.Reg.Host.PID)}
	default:
		return liveness.Result{Status: liveness.Unknown, Reason: fmt.Sprintf("Codex host %d exited, and the session "+
			"may resume under a new host", n.Reg.Host.PID)}
	}
}

func unknown(format string, args ...any) liveness.Result {
	return liveness.Result{Status: liveness.Unknown, Reason: fmt.Sprintf(format, args...)}
}

// GateError is a session precondition that acquire refused. The command layer appends the setup
// entries for the agent.
type GateError struct {
	Agent string
	Msg   string
}

// Error implements error.
func (e *GateError) Error() string { return e.Msg }

// Admit decides whether a session may acquire. It runs mid-turn, when the writer is certainly
// live, so it can demand things a later liveness read cannot.
func (s *Service) Admit(h adapter.Holder) error {
	switch h.Adapter.Name {
	case AgentCodex:
		return s.admitCodex(h)
	case AgentOpencode:
		return s.admitOpencode(h)
	default:
		return nil
	}
}

// MayMove reports whether a session's leases may follow it to the host it runs under now. A Codex
// session needs its own writer-lock file, the same as acquire. Without it the leases stay on the old
// host and read unknown, and the second result says why.
func (s *Service) MayMove(h adapter.Holder) (ok bool, why string) {
	if h.Adapter.Name != AgentCodex {
		return true, ""
	}

	regs, err := s.registrations(h.SessionID)
	if err != nil {
		return false, fmt.Sprintf("the Codex registrations cannot be read: %v", err)
	}

	for _, rf := range regs {
		if rf.Reg == nil || rf.Reg.Host.PID != h.PID || !rf.Reg.Host.Start.Equal(h.Start) {
			continue
		}

		lock, err := WriterLock(rf.Reg.CodexHome, h.SessionID)

		switch lock {
		case FilePresent:
			return true, ""
		case FileAbsent:
			return false, fmt.Sprintf("Codex has no writer-lock file for this session at %s, so it cannot be shown to "+
				"run here", WriterLockPath(rf.Reg.CodexHome, h.SessionID))
		default:
			return false, fmt.Sprintf("Codex's writer-lock file cannot be checked: %v", err)
		}
	}

	return false, fmt.Sprintf("session %s has no registration under this host yet", h.SessionID)
}

func hostText(mode string, pid int) string {
	if mode == ModeTUI {
		return fmt.Sprintf("the embedded Codex TUI (pid %d)", pid)
	}

	return fmt.Sprintf("the Codex daemon (pid %d)", pid)
}

const restartText = "Codex reads these settings only when its host starts, so a change applies after that host restarts."

// admitCodex needs a registration under the host this command runs under, a recorded unload delay
// of 60 seconds or less, a recorded SessionEnd entry, and the session's own writer-lock file. The
// lock file proves that the veto works for this Codex version.
func (s *Service) admitCodex(h adapter.Holder) error {
	regs, err := s.registrations(h.SessionID)
	if err != nil {
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("cannot read the Codex registrations: %v", err)}
	}

	hostMode := adapter.CodexDaemon

	var here *regFile

	for i := range regs {
		rf := regs[i]

		if rf.Err != nil {
			return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("session file %s cannot be used (%v); upgrade nitipan "+
				"or ask the user", rf.Key.FileName(), rf.Err)}
		}

		if rf.Reg.Host.PID == h.PID && rf.Reg.Host.Start.Equal(h.Start) {
			here = &regs[i]
			hostMode = rf.Reg.Mode
		}
	}

	if here == nil {
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("Codex session %s has no registration under %s. "+
			"The SessionStart hook may have timed out, or it is not installed or not trusted.", h.SessionID,
			hostText(hostMode, h.PID))}
	}

	reg := here.Reg
	host := hostText(reg.Mode, reg.Host.PID)

	switch {
	case reg.UnloadDelaySecs == nil:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("the unload delay of %s is unknown, so nitipan cannot tell "+
			"how soon Codex reports an ended session. %s", host, restartText)}
	case *reg.UnloadDelaySecs > maxUnloadDelaySecs:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("thread_unload_delay_secs is %d for %s and must be %d or "+
			"less. %s", *reg.UnloadDelaySecs, host, maxUnloadDelaySecs, restartText)}
	case reg.SessionEnd == CheckAbsent:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("the recorded Codex config for %s has no SessionEnd entry "+
			"that runs nitipan. %s", host, restartText)}
	case reg.SessionEnd != CheckPresent:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("nitipan could not confirm a SessionEnd entry in the Codex "+
			"config for %s. %s", host, restartText)}
	}

	lock, err := WriterLock(reg.CodexHome, h.SessionID)

	switch lock {
	case FilePresent:
		return nil
	case FileAbsent:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("Codex has no writer-lock file for session %s at %s, so this "+
			"Codex version is not supported", h.SessionID, WriterLockPath(reg.CodexHome, h.SessionID))}
	default:
		return &GateError{Agent: AgentCodex, Msg: fmt.Sprintf("cannot check Codex's writer-lock file: %v", err)}
	}
}
