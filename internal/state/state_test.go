package state

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

const (
	idA = "01JAAAAAAAAAAAAAAAAAAAAAAA"
	idB = "01JBBBBBBBBBBBBBBBBBBBBBBB"
)

var (
	testMachine = record.Machine{Host: "laptop", ID: "machine-1"}
	selfStart   = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 500}
)

// newDir opens a fresh state directory. The fake process table knows the test process as alive.
func newDir(t *testing.T, mutate func(o *Options)) (*Dir, *proctest.Table) {
	t.Helper()

	table := proctest.New(record.PlatformDarwin, 4242)
	table.Add(4242, proctest.Process{Start: selfStart})

	opts := Options{
		Checker:    &liveness.Checker{Procs: table, Local: testMachine},
		Procs:      table,
		Machine:    testMachine,
		SelfPID:    4242,
		SelfStart:  selfStart,
		Command:    "nitipan acquire",
		LockBudget: 40 * time.Millisecond,
		LockPoll:   10 * time.Millisecond,
		Sleep:      func(time.Duration) {},
	}

	if mutate != nil {
		mutate(&opts)
	}

	dir, err := Open(filepath.Join(t.TempDir(), "state", "nitipan"), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return dir, table
}

func mustLock(t *testing.T, d *Dir) *Locked {
	t.Helper()

	l, err := d.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	t.Cleanup(func() { _ = l.Unlock() })

	return l
}

func TestOpenCreatesDirectoryWithMode0700(t *testing.T) {
	dir, _ := newDir(t, nil)

	info, err := os.Stat(dir.Path())
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestOpenRefusesDirectoryOwnedByAnotherUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	_, err := Open(path, Options{UID: os.Getuid() + 1})
	if !errors.Is(err, ErrNotOwned) {
		t.Errorf("err = %v, want ErrNotOwned", err)
	}
}

func TestOpenRefusesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path, Options{}); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

func TestOpenRefusesRelativePath(t *testing.T) {
	if _, err := Open("state", Options{}); err == nil {
		t.Error("a relative state directory should be refused")
	}
}

func TestResolveDirIgnoresHomeAndXDG(t *testing.T) {
	t.Setenv("HOME", "/decoy/home")
	t.Setenv("XDG_STATE_HOME", "/decoy/xdg")

	got, err := ResolveDir()
	if err != nil {
		t.Fatalf("ResolveDir: %v", err)
	}

	if strings.Contains(got, "decoy") {
		t.Errorf("state directory %q follows an environment override", got)
	}

	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skipf("no user database entry: %v", err)
	}

	if want := filepath.Join(u.HomeDir, ".local", "state", "nitipan"); got != want {
		t.Errorf("state directory = %q, want %q", got, want)
	}
}

type resolveFailTest struct {
	Name   string
	Lookup func(string) (*user.User, error)
}

var resolveFailTests = []resolveFailTest{
	{
		Name:   "lookup fails",
		Lookup: func(string) (*user.User, error) { return nil, errors.New("no such user") },
	},
	{
		Name:   "empty home",
		Lookup: func(string) (*user.User, error) { return &user.User{}, nil },
	},
	{
		Name:   "relative home",
		Lookup: func(string) (*user.User, error) { return &user.User{HomeDir: "home/ripta"}, nil },
	},
}

func TestResolveDirFailsClosed(t *testing.T) {
	t.Setenv("HOME", "/usable/home")

	for _, test := range resolveFailTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ResolveDirWith(test.Lookup)
			if err == nil {
				t.Errorf("ResolveDirWith = %q, want an error and no fallback to $HOME", got)
			}
		})
	}
}

func TestPublishScanReplaceRemove(t *testing.T) {
	dir, _ := newDir(t, nil)
	l := mustLock(t, dir)

	if err := l.Publish(LeasesDir, idA, []byte("one\n")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := l.Publish(LeasesDir, idA, []byte("two\n")); !errors.Is(err, ErrExists) {
		t.Errorf("second Publish = %v, want ErrExists", err)
	}

	if err := l.Publish(LeasesDir, idB, []byte("b\n")); err != nil {
		t.Fatalf("Publish B: %v", err)
	}

	files, err := dir.Scan(LeasesDir)
	if err != nil || len(files) != 2 {
		t.Fatalf("Scan = %v, %v", files, err)
	}

	if err := l.Replace(LeasesDir, idA, []byte("three\n")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	data, err := dir.Read(LeasesDir, idA)
	if err != nil || string(data) != "three\n" {
		t.Errorf("Read = %q, %v", data, err)
	}

	if err := l.Remove(LeasesDir, idA); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if err := l.Remove(LeasesDir, idA); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("second Remove = %v, want not-exist", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir.Path(), LeasesDir))
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
}

func TestScanSkipsTempFilesAndForeignNames(t *testing.T) {
	dir, _ := newDir(t, nil)
	l := mustLock(t, dir)

	if err := l.Publish(LeasesDir, idA, []byte("a\n")); err != nil {
		t.Fatal(err)
	}

	leases := filepath.Join(dir.Path(), LeasesDir)
	writeRaw(t, filepath.Join(leases, ".tmp-"+idB), "partial")
	writeRaw(t, filepath.Join(leases, "notes.txt"), "hi")
	writeRaw(t, filepath.Join(leases, "odd.json"), "{}")

	files, err := dir.Scan(LeasesDir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, f := range files {
		got[f.ID] = f.BadName
	}

	if len(got) != 2 || got[idA] || !got["odd"] {
		t.Errorf("scan = %v, want the lease and the badly named json, no temp file", got)
	}
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanOfMissingDirectoryIsEmpty(t *testing.T) {
	dir, _ := newDir(t, nil)

	files, err := dir.Scan(TombstonesDir)
	if err != nil || len(files) != 0 {
		t.Errorf("Scan = %v, %v", files, err)
	}
}

func TestNewIDIsAValidUniqueLeaseID(t *testing.T) {
	seen := map[string]bool{}

	for range 100 {
		id := NewID()
		if !ValidID(id) || seen[id] {
			t.Fatalf("NewID = %q, want a valid ID not seen before", id)
		}

		seen[id] = true
	}
}

func TestIDsAreValidatedBeforeTouchingTheFilesystem(t *testing.T) {
	dir, _ := newDir(t, nil)
	l := mustLock(t, dir)

	for _, id := range []string{"", "../escape", "short", strings.ToLower(idA), idA + "x", "a/b"} {
		if err := l.Publish(LeasesDir, id, nil); !errors.Is(err, ErrBadID) {
			t.Errorf("Publish(%q) = %v, want ErrBadID", id, err)
		}

		if _, err := dir.Read(LeasesDir, id); !errors.Is(err, ErrBadID) {
			t.Errorf("Read(%q) = %v, want ErrBadID", id, err)
		}

		if err := l.Remove(LeasesDir, id); !errors.Is(err, ErrBadID) {
			t.Errorf("Remove(%q) = %v, want ErrBadID", id, err)
		}
	}
}

func TestLockSweepsTempFilesOfACrashedWriter(t *testing.T) {
	dir, _ := newDir(t, nil)

	leases := filepath.Join(dir.Path(), LeasesDir)
	tombs := filepath.Join(dir.Path(), TombstonesDir)
	writeRaw(t, filepath.Join(leases, ".tmp-"+idA), "partial")
	writeRaw(t, filepath.Join(tombs, ".tmp-"+idB), "partial")
	writeRaw(t, filepath.Join(leases, idB+".json"), "{}")

	mustLock(t, dir)

	for _, path := range []string{
		filepath.Join(leases, ".tmp-"+idA),
		filepath.Join(tombs, ".tmp-"+idB),
	} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the sweep: %v", path, err)
		}
	}

	if _, err := os.Stat(filepath.Join(leases, idB+".json")); err != nil {
		t.Errorf("the sweep removed a published record: %v", err)
	}
}

func TestSecondLockTimesOutWithItsOwnError(t *testing.T) {
	dir, _ := newDir(t, nil)
	mustLock(t, dir)

	// A second descriptor on the same file contends the way another process would.
	_, err := dir.Lock()
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("err = %v, want ErrLockBusy", err)
	}

	var busy *LockBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want a LockBusyError", err)
	}

	if busy.Waited < 40*time.Millisecond {
		t.Errorf("waited %s, want the whole budget", busy.Waited)
	}
}

func TestLockBusyErrorNamesAVerifiedHolderAndSaysWhetherItIsStopped(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		dir, table := newDir(t, nil)
		table.Procs[4242].Stopped = stopped

		mustLock(t, dir)

		_, err := dir.Lock()

		var busy *LockBusyError
		if !errors.As(err, &busy) || busy.Holder == nil {
			t.Fatalf("err = %v, want a verified holder", err)
		}

		if busy.Holder.PID != 4242 || !busy.StoppedKnown || busy.Stopped != stopped {
			t.Errorf("busy = %+v, want pid 4242 stopped=%v", busy, stopped)
		}

		want := "running"
		if stopped {
			want = "stopped"
		}

		if !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name the holder as %s", err, want)
		}
	}
}

type unverifiedHolderTest struct {
	Name   string
	Remove bool
	Start  record.ProcessStart
	Remote bool
}

var unverifiedHolderTests = []unverifiedHolderTest{
	{Name: "holder process is gone", Remove: true},
	{Name: "pid was reused", Start: record.ProcessStart{Platform: record.PlatformDarwin, Sec: 999}},
}

func TestLockBusyErrorDoesNotNameAnUnverifiedHolder(t *testing.T) {
	for _, test := range unverifiedHolderTests {
		t.Run(test.Name, func(t *testing.T) {
			dir, table := newDir(t, nil)
			mustLock(t, dir)

			if test.Remove {
				delete(table.Procs, 4242)
			} else {
				table.Procs[4242].Start = test.Start
			}

			_, err := dir.Lock()

			var busy *LockBusyError
			if !errors.As(err, &busy) {
				t.Fatalf("err = %v", err)
			}

			if busy.Holder != nil {
				t.Errorf("holder = %+v, want none", busy.Holder)
			}

			if strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), "unknown") {
				t.Errorf("message %q should say the holder is unknown", err)
			}
		})
	}
}

func TestLockBusyErrorIgnoresANoteFromAnotherMachine(t *testing.T) {
	dir, _ := newDir(t, func(o *Options) {
		o.Machine = record.Machine{Host: "elsewhere", ID: "machine-9"}
	})

	mustLock(t, dir)

	_, err := dir.Lock()

	var busy *LockBusyError
	if !errors.As(err, &busy) || busy.Holder != nil {
		t.Errorf("err = %v, want an unknown holder for a note from another machine", err)
	}
}

func TestLockIsReleasedByUnlock(t *testing.T) {
	dir, _ := newDir(t, nil)

	first, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}

	second, err := dir.Lock()
	if err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}

	_ = second.Unlock()
}

func TestWriteWithoutTheLockIsRefused(t *testing.T) {
	dir, _ := newDir(t, nil)

	l, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}

	if err := l.Publish(LeasesDir, idA, nil); err == nil {
		t.Error("Publish after Unlock should be refused")
	}
}

type formatTest struct {
	Name       string
	Marker     string
	WantRefuse bool
}

var formatTests = []formatTest{
	{Name: "absent marker is format 1", Marker: "", WantRefuse: false},
	{Name: "marker at this version", Marker: "1\n", WantRefuse: false},
	{Name: "marker above this version", Marker: "2\n", WantRefuse: true},
	{Name: "marker far above this version", Marker: "99", WantRefuse: true},
	{Name: "undecodable marker", Marker: "banana", WantRefuse: true},
	{Name: "empty marker", Marker: "\n", WantRefuse: true},
	{Name: "zero marker", Marker: "0", WantRefuse: true},
}

func TestFormatMarkerRefusesEveryWriteAndStillReads(t *testing.T) {
	for _, test := range formatTests {
		t.Run(test.Name, func(t *testing.T) {
			dir, _ := newDir(t, nil)

			seed := mustLock(t, dir)
			if err := seed.Publish(LeasesDir, idA, []byte("kept\n")); err != nil {
				t.Fatal(err)
			}

			if err := seed.Unlock(); err != nil {
				t.Fatal(err)
			}

			if test.Marker != "" {
				writeRaw(t, filepath.Join(dir.Path(), "format"), test.Marker)
			}

			writeRaw(t, filepath.Join(dir.Path(), LeasesDir, ".tmp-"+idB), "partial")

			l := mustLock(t, dir)

			err := l.Publish(LeasesDir, idB, []byte("new\n"))
			if test.WantRefuse {
				if !errors.Is(err, ErrFormatRefused) {
					t.Fatalf("Publish = %v, want ErrFormatRefused", err)
				}

				for name, op := range map[string]func() error{
					"Replace": func() error { return l.Replace(LeasesDir, idA, []byte("x\n")) },
					"Remove":  func() error { return l.Remove(LeasesDir, idA) },
				} {
					if err := op(); !errors.Is(err, ErrFormatRefused) {
						t.Errorf("%s = %v, want ErrFormatRefused", name, err)
					}
				}

				if _, err := os.Stat(filepath.Join(dir.Path(), LeasesDir, ".tmp-"+idB)); err != nil {
					t.Error("a refused writer swept a temp file, which is a write")
				}

				files, err := dir.Scan(LeasesDir)
				if err != nil || len(files) != 1 || string(files[0].Data) != "kept\n" {
					t.Errorf("a refused directory should still list: %v, %v", files, err)
				}

				return
			}

			if err != nil {
				t.Errorf("Publish = %v, want success", err)
			}
		})
	}
}

func TestFormatErrorSaysToUpgrade(t *testing.T) {
	dir, _ := newDir(t, nil)
	writeRaw(t, filepath.Join(dir.Path(), "format"), "7")

	l := mustLock(t, dir)

	var fe *FormatError
	if !errors.As(l.FormatErr(), &fe) || fe.Need != 7 {
		t.Fatalf("FormatErr = %v", l.FormatErr())
	}

	if !strings.Contains(fe.Error(), "upgrade") {
		t.Errorf("message %q does not say to upgrade", fe)
	}
}
