package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// opencodeRule judges an opencode session from the marks its plugins write.
type opencodeRule struct {
	s *Service
}

// Judge implements liveness.SessionRule.
func (r opencodeRule) Judge(env record.Envelope) liveness.Result {
	res, _ := r.JudgeHint(env)

	return res
}

// JudgeHint implements liveness.HintingRule.
//
// A session is alive while a presence mark names it from a live TUI, or an execution mark names it
// under a live host. An execution mark under a dead host reads time_suspended from opencode's
// database. Set means the next service boot resumes the turn, so the session is unknown. Clear
// means the turn ended. Otherwise the session is dead.
func (r opencodeRule) JudgeHint(env record.Envelope) (verdict liveness.Result, advice string) {
	session := env.Holder.SessionID
	if !ValidSession(session) {
		return unknown("the lease has no usable session ID"), ""
	}

	presence, err := r.s.marks(state.PresenceDir, session)
	if err != nil {
		return unknown("cannot read the opencode presence marks: %v", err), ""
	}

	executing, err := r.s.marks(state.ExecutingDir, session)
	if err != nil {
		return unknown("cannot read the opencode execution marks: %v", err), ""
	}

	var (
		doubt string
		hint  string
	)

	for _, mf := range append(append([]markFile(nil), presence...), executing...) {
		if mf.Err != nil {
			return unknown("session file %s cannot be used: %v", mf.Key.FileName(), mf.Err), ""
		}
	}

	for _, mf := range presence {
		switch mf.Proc.Status {
		case liveness.Alive:
			return liveness.Result{Status: liveness.Alive, Reason: fmt.Sprintf("presence mark from TUI %d", mf.Mark.PID)}, ""
		case liveness.Unknown:
			doubt = fmt.Sprintf("cannot tell whether TUI %d is alive: %s", mf.Mark.PID, mf.Proc.Reason)
		}
	}

	for _, mf := range executing {
		switch mf.Proc.Status {
		case liveness.Alive:
			return liveness.Result{Status: liveness.Alive, Reason: fmt.Sprintf("execution mark under host %d", mf.Mark.PID)}, ""
		case liveness.Unknown:
			doubt = fmt.Sprintf("cannot tell whether host %d is alive: %s", mf.Mark.PID, mf.Proc.Reason)
		default:
			suspended, err := r.s.suspended(session)

			switch {
			case err != nil:
				doubt = fmt.Sprintf("host %d died mid-turn, and opencode's database cannot be read", mf.Mark.PID)
				hint = fmt.Sprintf("cannot read the opencode database: %v", err)
			case suspended:
				doubt = fmt.Sprintf("host %d died mid-turn, and the next service boot resumes the turn", mf.Mark.PID)
			}
		}
	}

	if doubt != "" {
		return liveness.Result{Status: liveness.Unknown, Reason: doubt}, hint
	}

	return liveness.Result{Status: liveness.Dead, Reason: "no presence mark from a live TUI and no execution mark"}, ""
}

// suspended reads time_suspended for a session. A session the database does not know has no turn
// to resume, so it reads clear.
func (s *Service) suspended(session string) (bool, error) {
	if s.DB == nil {
		return false, errors.New("no opencode database is configured")
	}

	return s.DB.Suspended(session)
}

// admitOpencode needs a presence mark from a live TUI. An execution mark alone never admits a
// claim, because it only extends a lease.
func (s *Service) admitOpencode(h adapter.Holder) error {
	presence, err := s.marks(state.PresenceDir, h.SessionID)
	if err != nil {
		return &GateError{Agent: AgentOpencode, Msg: fmt.Sprintf("cannot read the opencode presence marks: %v", err)}
	}

	for _, mf := range presence {
		if mf.Err != nil {
			return &GateError{Agent: AgentOpencode, Msg: fmt.Sprintf("session file %s cannot be used (%v); upgrade nitipan "+
				"or ask the user", mf.Key.FileName(), mf.Err)}
		}

		if mf.Proc.Status == liveness.Alive {
			return nil
		}
	}

	return &GateError{Agent: AgentOpencode, Msg: fmt.Sprintf("opencode session %s has no presence mark from a live TUI. "+
		"It needs a TUI with nitipan's plugin loaded. A session from opencode run, the desktop app, or the API cannot "+
		"acquire.", h.SessionID)}
}

// SetMark writes or removes an opencode mark for a session. A presence mark ties to the TUI and an
// execution mark to the service, as the caller's process walk found them.
func (s *Service) SetMark(l *state.Locked, dir string, host adapter.Holder, session string, on bool) error {
	if host.Adapter.Name != AgentOpencode {
		return fmt.Errorf("the host is %q, not opencode", host.Adapter.Name)
	}

	m := &Mark{
		Version:   record.SessionVersion,
		Agent:     AgentOpencode,
		Session:   session,
		PID:       host.PID,
		Start:     host.Start,
		WrittenAt: s.now(),
	}

	if err := m.Key().Validate(); err != nil {
		return err
	}

	name := m.Key().FileName()

	if err := s.checkRewritable(dir, m.Key()); err != nil {
		return err
	}

	if !on {
		return removeIfPresent(l, dir, name)
	}

	data, err := encode(m)
	if err != nil {
		return err
	}

	if err := l.ReplaceNamed(dir, name, data); err != nil {
		return fmt.Errorf("marking session %s: %w", session, err)
	}

	return nil
}

// checkRewritable refuses to overwrite a session file this binary must not rewrite. A file that
// does not exist is fine.
func (s *Service) checkRewritable(dir string, k Key) error {
	data, err := state.ReadName(s.Root, dir, k.FileName())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	var head struct {
		Version record.Version `json:"version"`
	}

	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s cannot be read, so it is not rewritten: %w", k.FileName(), err)
	}

	if err := writable(head.Version); err != nil {
		return fmt.Errorf("%s is not rewritten: %w", k.FileName(), err)
	}

	return nil
}
