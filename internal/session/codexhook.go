package session

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// RegisterInput is what a Codex SessionStart hook reports.
type RegisterInput struct {
	// Host is the Codex process the hook runs under, found by the process walk.
	Host adapter.Holder

	// HostArgs is the host's full argument list, which decides its mode.
	HostArgs []string

	Session   string
	CodexHome string

	// Executable is the absolute path of this nitipan. A SessionEnd entry counts only when it runs it.
	Executable string

	// BeforeClear runs when the registration is about to clear an end, and before it removes anything.
	// The caller publishes the session's notice there. An error stops the registration.
	BeforeClear func(*RegisterResult) error
}

// RegisterResult says what a registration cleared. A session that comes back after an end gets a
// notice, which BeforeClear publishes.
type RegisterResult struct {
	// Cleared is true when the registration removed an end mark or an ended registration.
	Cleared bool

	// EndedSince is the earliest time an end was recorded among the cleared items.
	EndedSince time.Time
}

// RegisterCodex records that a session is open under a host.
//
// A new registration removes every ended registration of the session, under any host, and clears
// its end marks under any host. The first registration under a host records two checks of the
// config in force, and later ones copy them.
func (s *Service) RegisterCodex(l *state.Locked, in RegisterInput) (*RegisterResult, error) {
	if in.Host.Adapter.Name != AgentCodex {
		return nil, fmt.Errorf("the host is %q, not Codex", in.Host.Adapter.Name)
	}

	if !ValidSession(in.Session) {
		return nil, fmt.Errorf("%w: session %q", ErrBadName, in.Session)
	}

	reg := &Registration{
		Version:      record.Current,
		Agent:        AgentCodex,
		Session:      in.Session,
		Host:         Host{PID: in.Host.PID, Start: in.Host.Start},
		Mode:         adapter.CodexHostMode(in.HostArgs),
		CodexHome:    in.CodexHome,
		RegisteredAt: s.now(),
	}

	if err := reg.Key().Validate(); err != nil {
		return nil, err
	}

	if err := s.fillChecks(reg, in); err != nil {
		return nil, err
	}

	if err := s.checkRewritable(state.RegisteredDir, reg.Key()); err != nil {
		return nil, err
	}

	plan, err := s.planClear(in.Session)
	if err != nil {
		return nil, err
	}

	// The notice goes out before anything is removed, so a failure leaves the end in place and the
	// next registration tries again. A removal done first could lose the only signal the session gets.
	if plan.Cleared && in.BeforeClear != nil {
		if err := in.BeforeClear(&plan.RegisterResult); err != nil {
			return nil, err
		}
	}

	if err := s.applyClear(l, plan); err != nil {
		return nil, err
	}

	res := &plan.RegisterResult

	data, err := encode(reg)
	if err != nil {
		return nil, err
	}

	if err := l.ReplaceNamed(state.RegisteredDir, reg.Key().FileName(), data); err != nil {
		return nil, fmt.Errorf("registering session %s: %w", in.Session, err)
	}

	return res, nil
}

// fillChecks copies the host's checks from a sibling registration, or reads the config.
func (s *Service) fillChecks(reg *Registration, in RegisterInput) error {
	all, err := s.registrations("")
	if err != nil {
		return err
	}

	for _, rf := range all {
		if rf.Reg == nil || rf.Key.PID != reg.Host.PID || rf.Key.Start != reg.Host.Start.String() {
			continue
		}

		reg.UnloadDelaySecs = rf.Reg.UnloadDelaySecs
		reg.SessionEnd = rf.Reg.SessionEnd

		return nil
	}

	start, ok := StartTime(reg.Host.Start)
	checks := ScanCodexConfig(in.CodexHome, in.Executable, start, ok)
	reg.UnloadDelaySecs = checks.UnloadDelaySecs
	reg.SessionEnd = checks.SessionEnd

	return nil
}

// clearPlan lists what a registration will remove: the session's ended registrations and end marks,
// whatever host they name.
type clearPlan struct {
	RegisterResult

	registrations []Key
	marks         []Key
}

// planClear finds what a registration clears, without removing anything.
func (s *Service) planClear(session string) (*clearPlan, error) {
	plan := &clearPlan{}

	note := func(t time.Time) {
		if plan.EndedSince.IsZero() || t.Before(plan.EndedSince) {
			plan.EndedSince = t
		}
	}

	regs, err := s.registrations(session)
	if err != nil {
		return nil, err
	}

	for _, rf := range regs {
		// An old binary never removes a registration of a higher minor.
		if rf.Reg == nil || !rf.Reg.Ended || writable(rf.Reg.Version) != nil {
			continue
		}

		if rf.Reg.EndedAt != nil {
			note(*rf.Reg.EndedAt)
		} else {
			note(rf.Reg.RegisteredAt)
		}

		plan.registrations = append(plan.registrations, rf.Key)
	}

	marks, err := s.keys(state.EndedDir, AgentCodex, session)
	if err != nil {
		return nil, err
	}

	for _, k := range marks {
		if at, err := statMark(s.Root, k); err == nil {
			note(at)
		}

		plan.marks = append(plan.marks, k)
	}

	plan.Cleared = len(plan.registrations)+len(plan.marks) > 0
	if plan.Cleared && plan.EndedSince.IsZero() {
		plan.EndedSince = s.now()
	}

	return plan, nil
}

func (s *Service) applyClear(l *state.Locked, plan *clearPlan) error {
	for _, k := range plan.registrations {
		if err := removeIfPresent(l, state.RegisteredDir, k.FileName()); err != nil {
			return err
		}
	}

	for _, k := range plan.marks {
		if err := removeIfPresent(l, state.EndedDir, k.Name()); err != nil {
			return err
		}
	}

	return nil
}

func removeIfPresent(l *state.Locked, sub, name string) error {
	err := l.RemoveNamed(sub, name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// EndCodex records that a session ended under its host. It takes no lock: the SessionEnd hook has
// at most 3 seconds, and the lock can be busy longer. The mark is an empty file created with
// O_EXCL, and one that already exists counts as success.
func (s *Service) EndCodex(host adapter.Holder, session string) error {
	if host.Adapter.Name != AgentCodex {
		return fmt.Errorf("the host is %q, not Codex", host.Adapter.Name)
	}

	k := Key{Agent: AgentCodex, Session: session, PID: host.PID, Start: host.Start.String()}
	if err := k.Validate(); err != nil {
		return err
	}

	if err := state.FormatErrAt(s.Root); err != nil {
		return err
	}

	if _, err := state.CreateMark(s.Root, state.EndedDir, k.Name()); err != nil {
		return err
	}

	return nil
}
