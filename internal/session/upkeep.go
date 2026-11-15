package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/state"
)

// Maintain is the upkeep every lock holder owes. It first turns Codex end marks under a live host
// into ended registrations, then sweeps the session files whose tied process is dead.
//
// It runs under the lock. Removing a file is a write, so an old binary acts on nothing it cannot
// fully read, and it never rewrites or removes a registration or mark of a higher minor version.
// A failure to handle one file never stops the rest, and all of them are returned.
func (s *Service) Maintain(l *state.Locked) error {
	named := s.leaseSessions()

	var errs []error

	errs = append(errs, s.convertEndMarks(l)...)
	errs = append(errs, s.sweep(l, named)...)

	return errors.Join(errs...)
}

// leaseSessions lists the sessions that some lease names, as "agent\x00session". A lease file that
// does not decode names nobody.
func (s *Service) leaseSessions() map[string]bool {
	out := map[string]bool{}

	files, err := state.ListNames(s.Root, state.LeasesDir)
	if err != nil {
		return out
	}

	for _, name := range files {
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		data, err := state.ReadName(s.Root, state.LeasesDir, name)
		if err != nil {
			continue
		}

		var rec struct {
			Holder struct {
				Agent     string `json:"agent"`
				SessionID string `json:"session_id"`
			} `json:"holder"`
		}

		if json.Unmarshal(data, &rec) == nil && rec.Holder.SessionID != "" {
			out[leaseKey(rec.Holder.Agent, rec.Holder.SessionID)] = true
		}
	}

	return out
}

func leaseKey(agent, session string) string {
	return agent + "\x00" + session
}

// HoldsLease reports whether some lease names the session.
func (s *Service) HoldsLease(agent, session string) bool {
	return s.leaseSessions()[leaseKey(agent, session)]
}

// convertEndMarks makes end marks durable. A mark under a live host counts only while the session's
// writer-lock file is absent, and then the session's registration becomes ended. The mark is removed
// once the registration carries it. A mark whose lock file exists is left, since the session may
// have resumed and a later check can still see the file go.
func (s *Service) convertEndMarks(l *state.Locked) []error {
	keys, err := s.keys(state.EndedDir, AgentCodex, "")
	if err != nil {
		return []error{err}
	}

	var errs []error

	for _, k := range keys {
		if s.hostOf(k).Status != liveness.Alive {
			continue
		}

		if err := s.convertOne(l, k); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func (s *Service) convertOne(l *state.Locked, k Key) error {
	data, err := state.ReadName(s.Root, state.RegisteredDir, k.FileName())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	reg, err := decodeRegistration(data, k)
	if err != nil {
		return nil
	}

	if !reg.Ended {
		lock, _ := WriterLock(reg.CodexHome, k.Session)
		if lock != FileAbsent {
			return nil
		}

		if err := writable(reg.Version); err != nil {
			return nil
		}

		at := s.now()
		reg.Ended, reg.EndedAt = true, &at

		out, err := encode(reg)
		if err != nil {
			return err
		}

		if err := l.ReplaceNamed(state.RegisteredDir, k.FileName(), out); err != nil {
			return fmt.Errorf("marking session %s ended: %w", k.Session, err)
		}
	}

	return removeIfPresent(l, state.EndedDir, k.Name())
}

// sweep removes session files whose tied process is dead, with three exceptions. An execution mark
// stays until opencode's time_suspended for its session reads clear. An ended registration stays
// while any lease names its session, and so does the registration of a dead embedded host, which
// counts as ended. A file that cannot be fully read stays until an upgrade or `break --force`.
func (s *Service) sweep(l *state.Locked, named map[string]bool) []error {
	var errs []error

	regs, err := s.registrations("")
	if err != nil {
		errs = append(errs, err)
	}

	for _, rf := range regs {
		if rf.Reg == nil || rf.Host.Status != liveness.Dead || writable(rf.Reg.Version) != nil {
			continue
		}

		endedLike := rf.Reg.Ended || rf.Reg.Mode == ModeTUI
		if endedLike && named[leaseKey(AgentCodex, rf.Reg.Session)] {
			continue
		}

		if err := removeIfPresent(l, state.RegisteredDir, rf.Key.FileName()); err != nil {
			errs = append(errs, err)
		}
	}

	for _, dir := range []string{state.PresenceDir, state.ExecutingDir} {
		marks, err := s.marks(dir, "")
		if err != nil {
			errs = append(errs, err)

			continue
		}

		var dead []markFile

		for _, mf := range marks {
			if mf.Mark != nil && mf.Proc.Status == liveness.Dead && writable(mf.Mark.Version) == nil {
				dead = append(dead, mf)
			}
		}

		var keep map[string]bool
		if dir == state.ExecutingDir {
			keep = s.mayResume(dead)
		}

		for _, mf := range dead {
			if keep[mf.Key.Session] {
				continue
			}

			if err := removeIfPresent(l, dir, mf.Key.FileName()); err != nil {
				errs = append(errs, err)
			}
		}
	}

	errs = append(errs, s.sweepEndMarks(l)...)

	return errs
}

// resumeBudget bounds the one database read a sweep makes. The sweep runs under the lock in a Codex
// hook that has 3 seconds in all, so the read must leave most of that alone.
const resumeBudget = 800 * time.Millisecond

// mayResume names the sessions whose dead-host execution marks must stay: those whose
// time_suspended is set. It reads the database once for all of them. A read that fails or runs out
// of time keeps every mark, since each one reads unknown.
func (s *Service) mayResume(dead []markFile) map[string]bool {
	if len(dead) == 0 {
		return nil
	}

	sessions := make([]string, 0, len(dead))
	keep := make(map[string]bool, len(dead))

	for _, mf := range dead {
		sessions = append(sessions, mf.Key.Session)
		keep[mf.Key.Session] = true
	}

	if s.DB == nil {
		return keep
	}

	suspended, err := s.DB.SuspendedAmong(sessions, resumeBudget)
	if err != nil {
		return keep
	}

	for _, id := range sessions {
		keep[id] = suspended[id]
	}

	return keep
}

// sweepEndMarks removes end marks that name a dead host. They never count, because a host restart
// fires SessionEnd for every thread.
func (s *Service) sweepEndMarks(l *state.Locked) []error {
	keys, err := s.keys(state.EndedDir, AgentCodex, "")
	if err != nil {
		return []error{err}
	}

	var errs []error

	for _, k := range keys {
		if s.hostOf(k).Status != liveness.Dead {
			continue
		}

		if err := removeIfPresent(l, state.EndedDir, k.Name()); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// hostOf judges the process a file name is tied to. The name carries the start as text, so a name
// whose start does not parse as this machine's platform reads unknown.
func (s *Service) hostOf(k Key) liveness.Result {
	start, ok := ParseStart(s.Procs.Platform(), k.Start)
	if !ok {
		return unknown("the start %q in %s does not parse on this machine", k.Start, k.Name())
	}

	return s.processes().CheckProcess(k.PID, start)
}
