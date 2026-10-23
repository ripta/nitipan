package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Service reads and writes the session files of one state directory.
//
// Reads need no lock, because every file is published atomically. Writes take a *state.Locked
// from the caller, except the Codex end mark, which takes no lock at all.
//
// A Service holds the state path and not a *state.Dir. Opening the directory needs a liveness
// checker, and the checker needs this service's rules, so the rules must exist before the directory.
type Service struct {
	// Root is the absolute state directory path.
	Root string

	// Procs reads the local process table.
	Procs proc.Table

	// Local is the machine the service runs on.
	Local record.Machine

	// Now defaults to time.Now.
	Now func() time.Time

	// DB reads opencode's database. Nil means every read fails, so every answer that needs it reads
	// unknown.
	DB OpencodeDB
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}

	return time.Now().UTC()
}

// processes judges hosts and TUIs by PID and start, the way a record of kind process is judged.
func (s *Service) processes() *liveness.Checker {
	return &liveness.Checker{Procs: s.Procs, Local: s.Local}
}

// Rules returns the session rule of each agent that has one, keyed by agent name.
func (s *Service) Rules() map[string]liveness.SessionRule {
	return map[string]liveness.SessionRule{
		AgentCodex:    codexRule{s: s},
		AgentOpencode: opencodeRule{s: s},
	}
}

// keys lists the session files in a directory that belong to an agent and, when session is not
// empty, to that session. A name that does not follow the grammar vouches for no session, so it is
// skipped.
func (s *Service) keys(sub, agent, session string) ([]Key, error) {
	names, err := state.ListNames(s.Root, sub)
	if err != nil {
		return nil, err
	}

	var out []Key

	for _, name := range names {
		k, err := ParseKey(name)
		if err != nil || k.Agent != agent {
			continue
		}

		if session != "" && k.Session != session {
			continue
		}

		out = append(out, k)
	}

	return out, nil
}

// regFile is a registration with the verdict on its host.
type regFile struct {
	Key  Key
	Reg  *Registration
	Err  error
	Host liveness.Result
}

// registrations reads every Codex registration of a session, or of every session when session is
// empty. A file that cannot be used comes back with Err set, so the caller reads its session as
// unknown.
func (s *Service) registrations(session string) ([]regFile, error) {
	keys, err := s.keys(state.RegisteredDir, AgentCodex, session)
	if err != nil {
		return nil, err
	}

	out := make([]regFile, 0, len(keys))

	for _, k := range keys {
		rf := regFile{Key: k}

		data, err := state.ReadName(s.Root, state.RegisteredDir, k.FileName())

		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			rf.Err = fmt.Errorf("%w: %w", errUnusable, err)
		default:
			rf.Reg, rf.Err = decodeRegistration(data, k)
		}

		if rf.Reg != nil {
			rf.Host = s.processes().CheckProcess(rf.Reg.Host.PID, rf.Reg.Host.Start)
		}

		out = append(out, rf)
	}

	return out, nil
}

// markFile is a presence or execution mark with the verdict on its process.
type markFile struct {
	Key  Key
	Mark *Mark
	Err  error
	Proc liveness.Result
}

// marks reads the opencode marks of a session in one directory.
func (s *Service) marks(sub, session string) ([]markFile, error) {
	keys, err := s.keys(sub, AgentOpencode, session)
	if err != nil {
		return nil, err
	}

	out := make([]markFile, 0, len(keys))

	for _, k := range keys {
		mf := markFile{Key: k}

		data, err := state.ReadName(s.Root, sub, k.FileName())

		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			mf.Err = fmt.Errorf("%w: %w", errUnusable, err)
		default:
			mf.Mark, mf.Err = decodeMark(data, k)
		}

		if mf.Mark != nil {
			mf.Proc = s.processes().CheckProcess(mf.Mark.PID, mf.Mark.Start)
		}

		out = append(out, mf)
	}

	return out, nil
}

// endMark reports whether the Codex end mark for a host exists. Any error other than a missing
// file is returned, so the caller reads unknown.
func (s *Service) endMark(k Key) (bool, error) {
	_, err := os.Lstat(filepath.Join(s.Root, state.EndedDir, k.Name()))

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ENOENT):
		return false, nil
	default:
		return false, fmt.Errorf("reading the end mark %s: %w", k.Name(), err)
	}
}

// FileState is what a check of Codex's writer-lock file found.
type FileState int

// Writer-lock file states. Only ENOENT counts as absent. Any other error reads unknown.
const (
	FileUnknown FileState = iota
	FilePresent
	FileAbsent
)

// WriterLockPath is where Codex keeps a thread's writer-lock file.
func WriterLockPath(codexHome, session string) string {
	return filepath.Join(codexHome, "thread-writer-locks", session+".lock")
}

// WriterLock checks Codex's writer-lock file for a session. The file exists while a Codex runtime
// holds the thread for writing, and it vetoes a late SessionEnd after a resume.
func WriterLock(codexHome, session string) (FileState, error) {
	if !filepath.IsAbs(codexHome) {
		return FileUnknown, fmt.Errorf("the Codex home %q is not an absolute path", codexHome)
	}

	if !ValidSession(session) {
		return FileUnknown, fmt.Errorf("session %q cannot name a lock file", session)
	}

	_, err := os.Stat(WriterLockPath(codexHome, session))

	switch {
	case err == nil:
		return FilePresent, nil
	case errors.Is(err, unix.ENOENT):
		return FileAbsent, nil
	default:
		return FileUnknown, fmt.Errorf("reading the writer-lock file for %s: %w", session, err)
	}
}

// newest picks the most recently registered of several registrations.
func newest(regs []regFile) regFile {
	sorted := append([]regFile(nil), regs...)

	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].Reg.RegisteredAt, sorted[j].Reg.RegisteredAt
		if !a.Equal(b) {
			return a.After(b)
		}

		return sorted[i].Key.Name() > sorted[j].Key.Name()
	})

	return sorted[0]
}
