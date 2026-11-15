// Package state owns the state directory: where it lives, how it is locked, and how records are
// published into it.
//
// The directory layout and the lock are part of the on-disk format. Every write happens under the
// lock, and every record is published through a synced temp file so a reader without the lock never
// sees a partly written one.
package state

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
)

// Errors returned by the state directory.
var (
	// ErrNotOwned means the state directory belongs to another user.
	ErrNotOwned = errors.New("state directory is not owned by the calling user")

	// ErrNotDirectory means the state path exists and is not a directory.
	ErrNotDirectory = errors.New("state path is not a directory")

	// ErrBadID means an ID is not a lease ID. It guards file names against path tricks.
	ErrBadID = errors.New("not a lease id")

	// ErrExists means a record with that ID is already published.
	ErrExists = errors.New("record already exists")

	// ErrBadName means a file name could escape its directory or hide as a temp file.
	ErrBadName = errors.New("not a usable file name")
)

// Defaults for Options.
const (
	DefaultLockBudget = 5 * time.Second
	DefaultLockPoll   = 25 * time.Millisecond
)

// Directory names inside the state directory.
const (
	LeasesDir     = "leases"
	TombstonesDir = "tombstones"

	// The session directories hold what Codex hooks and opencode plugins report. They sit under
	// sessions/, as paths relative to the state directory.
	RegisteredDir = "sessions/registered"
	PresenceDir   = "sessions/presence"
	ExecutingDir  = "sessions/executing"
	EndedDir      = "sessions/ended"
	ConfigDir     = "sessions/config"

	lockName   = "lock"
	formatName = "format"
	tempPrefix = ".tmp-"
	fileSuffix = ".json"
)

// Options configure a Dir. The command layer builds them, so tests inject every dependency.
type Options struct {
	// UID is the user that must own the directory. Zero means the calling user.
	UID int

	// Checker judges the holder named in the lock file when the lock stays busy.
	Checker *liveness.Checker

	// Procs reads the state of the lock holder.
	Procs proc.Table

	// Machine, SelfPID, SelfStart, and Command describe this process. They go into the lock file
	// right after the lock is taken.
	Machine   record.Machine
	SelfPID   int
	SelfStart record.ProcessStart
	Command   string

	// LockBudget is how long Lock retries before giving up. Zero means DefaultLockBudget.
	LockBudget time.Duration

	// LockPoll is the pause between lock attempts. Zero means DefaultLockPoll.
	LockPoll time.Duration

	// Sleep pauses between lock attempts. Nil means time.Sleep.
	Sleep func(time.Duration)

	// AfterLock runs under the lock, after the temp-file sweep, whenever the format marker allows
	// writes. It does the upkeep that every lock holder owes, such as turning Codex end marks into
	// ended registrations and sweeping dead hosts' session files. A failure goes to Warn and never
	// fails the call.
	AfterLock func(*Locked) error

	// Warn receives failures that must not fail a call. Nil drops them.
	Warn func(error)
}

// Dir is an opened state directory.
type Dir struct {
	path string
	opts Options
}

// ResolveDir returns the state directory path, ~/.local/state/nitipan, with the home directory taken
// from the user database.
//
// HOME and XDG_STATE_HOME are ignored. Two sessions that resolve the directory differently would
// scan two directories, and both claims would succeed. A failed lookup is an error, never a
// fallback to $HOME.
func ResolveDir() (string, error) {
	return ResolveDirWith(user.LookupId)
}

// ResolveDirWith is ResolveDir with the user lookup injected.
func ResolveDirWith(lookup func(uid string) (*user.User, error)) (string, error) {
	uid := strconv.Itoa(os.Getuid())

	u, err := lookup(uid)
	if err != nil {
		return "", fmt.Errorf("looking up user %s in the user database: %w", uid, err)
	}

	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return "", fmt.Errorf("user database gives %q as the home directory of user %s, want an absolute path",
			u.HomeDir, uid)
	}

	return filepath.Join(u.HomeDir, ".local", "state", "nitipan"), nil
}

// Open creates the state directory with mode 0700 if it is missing, and refuses one the caller does
// not own.
func Open(path string, opts Options) (*Dir, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("state directory %q: want an absolute path", path)
	}

	if opts.UID == 0 {
		opts.UID = os.Getuid()
	}

	if opts.LockBudget == 0 {
		opts.LockBudget = DefaultLockBudget
	}

	if opts.LockPoll == 0 {
		opts.LockPoll = DefaultLockPoll
	}

	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating the parent of the state directory: %w", err)
	}

	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("creating the state directory %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("checking the state directory %s: %w", path, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%s: %w", path, ErrNotDirectory)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("checking the owner of %s: no ownership information on this platform", path)
	}

	if int(st.Uid) != opts.UID {
		return nil, fmt.Errorf("%s is owned by user %d, not %d: %w", path, st.Uid, opts.UID, ErrNotOwned)
	}

	return &Dir{path: path, opts: opts}, nil
}

// Path returns the absolute state directory path.
func (d *Dir) Path() string {
	return d.path
}
