package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

// ErrLockBusy matches a LockBusyError. A busy lock has its own exit status.
var ErrLockBusy = errors.New("state is locked by another nitipan call")

// HolderNote is the advisory line a lock holder writes into the lock file. The flock is
// authoritative. The note only helps name who holds it.
type HolderNote struct {
	PID     int                 `json:"pid"`
	Start   record.ProcessStart `json:"start"`
	Machine record.Machine      `json:"machine"`
	Command string              `json:"command"`
}

// LockBusyError reports that the lock stayed busy for the whole retry budget.
type LockBusyError struct {
	Waited time.Duration

	// Holder is set only when the noted process passed the liveness check. Otherwise the holder is
	// unknown, because the note may belong to a holder that has since died.
	Holder *HolderNote

	// Stopped is true when the verified holder is stopped, as by SIGSTOP. StoppedKnown is false
	// when that could not be read.
	Stopped      bool
	StoppedKnown bool
}

// Error implements error.
func (e *LockBusyError) Error() string {
	msg := fmt.Sprintf("the state lock stayed busy for %s", e.Waited.Round(time.Millisecond))

	if e.Holder == nil {
		return msg + "; the process holding it is unknown"
	}

	holder := fmt.Sprintf("process %d", e.Holder.PID)
	if e.Holder.Command != "" {
		holder += fmt.Sprintf(" (%s)", e.Holder.Command)
	}

	switch {
	case !e.StoppedKnown:
		return fmt.Sprintf("%s; held by %s", msg, holder)
	case e.Stopped:
		return fmt.Sprintf("%s; held by %s, which is stopped", msg, holder)
	default:
		return fmt.Sprintf("%s; held by %s, which is running", msg, holder)
	}
}

// Is matches ErrLockBusy.
func (e *LockBusyError) Is(target error) bool {
	return target == ErrLockBusy
}

// Locked is the state directory held under its lock. Every write goes through it.
type Locked struct {
	dir  *Dir
	file *os.File

	// formatErr is set when the format marker forbids writes. Reads still work.
	formatErr error
}

// Lock takes the exclusive lock without blocking, retrying until the budget runs out.
//
// Right after it takes the lock, the holder writes its PID, process start, and command into the
// lock file. The sweep of stale temp files and the format check also happen here, under the lock.
func (d *Dir) Lock() (*Locked, error) {
	f, err := os.OpenFile(filepath.Join(d.path, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the lock file: %w", err)
	}

	if err := d.acquire(f); err != nil {
		// A stale note is read before the descriptor closes.
		note := readNote(f)
		_ = f.Close()

		var busy *LockBusyError
		if errors.As(err, &busy) {
			d.describeHolder(busy, note)
		}

		return nil, err
	}

	l := &Locked{dir: d, file: f}

	if err := l.writeNote(); err != nil {
		_ = f.Close()

		return nil, err
	}

	l.formatErr = d.checkFormat()
	if l.formatErr == nil {
		if err := l.sweepTemp(); err != nil {
			_ = f.Close()

			return nil, err
		}

		if d.opts.AfterLock != nil {
			if err := d.opts.AfterLock(l); err != nil && d.opts.Warn != nil {
				d.opts.Warn(fmt.Errorf("session upkeep: %w", err))
			}
		}
	}

	return l, nil
}

func (d *Dir) acquire(f *os.File) error {
	var waited time.Duration

	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}

		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("locking the state directory: %w", err)
		}

		if waited >= d.opts.LockBudget {
			return &LockBusyError{Waited: waited}
		}

		d.opts.Sleep(d.opts.LockPoll)
		waited += d.opts.LockPoll
	}
}

// describeHolder fills in the holder only when the noted process is verifiably the one running.
func (d *Dir) describeHolder(busy *LockBusyError, note *HolderNote) {
	if note == nil || d.opts.Checker == nil || !liveness.SameMachine(d.opts.Checker.Local, note.Machine) {
		return
	}

	if d.opts.Checker.CheckProcess(note.PID, note.Start).Status != liveness.Alive {
		return
	}

	busy.Holder = note

	if d.opts.Procs == nil {
		return
	}

	stopped, err := d.opts.Procs.Stopped(note.PID)
	if err != nil {
		return
	}

	busy.Stopped = stopped
	busy.StoppedKnown = true
}

func readNote(f *os.File) *HolderNote {
	buf := make([]byte, 4096)

	// ReadAt reports io.EOF for a short read, so only the byte count matters.
	n, _ := f.ReadAt(buf, 0)
	if n == 0 {
		return nil
	}

	var note HolderNote
	if json.Unmarshal(buf[:n], &note) != nil || note.PID <= 0 {
		return nil
	}

	return &note
}

func (l *Locked) writeNote() error {
	note := HolderNote{
		PID:     l.dir.opts.SelfPID,
		Start:   l.dir.opts.SelfStart,
		Machine: l.dir.opts.Machine,
		Command: l.dir.opts.Command,
	}

	data, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("encoding the lock holder note: %w", err)
	}

	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("clearing the lock holder note: %w", err)
	}

	if _, err := l.file.WriteAt(append(data, '\n'), 0); err != nil {
		return fmt.Errorf("writing the lock holder note: %w", err)
	}

	return nil
}

// Unlock releases the lock. The kernel also drops it when the process dies.
func (l *Locked) Unlock() error {
	if l.file == nil {
		return nil
	}

	err := l.file.Close()
	l.file = nil

	if err != nil {
		return fmt.Errorf("releasing the state lock: %w", err)
	}

	return nil
}

// Dir returns the locked directory.
func (l *Locked) Dir() *Dir {
	return l.dir
}
