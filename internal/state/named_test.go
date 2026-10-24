package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nameTest struct {
	Name string
	In   string
	OK   bool
}

var nameTests = []nameTest{
	{Name: "session file", In: "codex.t1.100.1792103390-449120.json", OK: true},
	{Name: "end mark", In: "codex.t1.100.1792103390-449120", OK: true},
	{Name: "empty", In: ""},
	{Name: "slash", In: "a/b"},
	{Name: "parent", In: "../a"},
	{Name: "dot only", In: "."},
	{Name: "hidden", In: ".tmp-x"},
	{Name: "nul", In: "a\x00b"},
	{Name: "too long", In: strings.Repeat("a", maxNameLen+1)},
}

func TestValidName(t *testing.T) {
	for _, test := range nameTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := ValidName(test.In); got != test.OK {
				t.Errorf("ValidName(%q) = %v", test.In, got)
			}
		})
	}
}

func TestRequestsAndNoticesAreCreatedOnDemandInADirectoryThatLacksThem(t *testing.T) {
	dir, _ := newDir(t, nil)

	for _, sub := range []string{RequestsDir, NoticesDir} {
		if files, err := dir.Scan(sub); err != nil || len(files) != 0 {
			t.Fatalf("scanning a missing %s: %v, %v", sub, files, err)
		}

		if names, err := ListNames(dir.Path(), sub); err != nil || len(names) != 0 {
			t.Fatalf("listing a missing %s: %v, %v", sub, names, err)
		}
	}

	l := mustLock(t, dir)

	if err := l.Publish(RequestsDir, idA, []byte("{}")); err != nil {
		t.Fatalf("publishing a request: %v", err)
	}

	// A notice's ID is a derived name, not a lease ID.
	notice := idA + "-request-granted.json"
	if err := l.PublishNamed(NoticesDir, notice, []byte("{}")); err != nil {
		t.Fatalf("publishing a notice: %v", err)
	}

	if err := l.PublishNamed(NoticesDir, notice, []byte("{}")); !errors.Is(err, ErrExists) {
		t.Errorf("a retry of the same notice: %v, want ErrExists", err)
	}

	if names, err := ListNames(dir.Path(), NoticesDir); err != nil || len(names) != 1 || names[0] != notice {
		t.Errorf("notices = %v, %v", names, err)
	}
}

func TestNamedFilesPublishReplaceRemoveAndList(t *testing.T) {
	dir, _ := newDir(t, nil)
	l := mustLock(t, dir)

	const name = "codex.t1.100.1-2.json"

	if err := l.PublishNamed(RegisteredDir, name, []byte("one")); err != nil {
		t.Fatal(err)
	}

	if err := l.PublishNamed(RegisteredDir, name, []byte("two")); !errors.Is(err, ErrExists) {
		t.Errorf("a second publish: %v", err)
	}

	if err := l.ReplaceNamed(RegisteredDir, name, []byte("three")); err != nil {
		t.Fatal(err)
	}

	got, err := ReadName(dir.Path(), RegisteredDir, name)
	if err != nil || string(got) != "three" {
		t.Errorf("read = %q, %v", got, err)
	}

	names, err := ListNames(dir.Path(), RegisteredDir)
	if err != nil || len(names) != 1 || names[0] != name {
		t.Errorf("names = %v, %v", names, err)
	}

	if err := l.RemoveNamed(RegisteredDir, name); err != nil {
		t.Fatal(err)
	}

	if err := l.RemoveNamed(RegisteredDir, name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("removing a missing file: %v", err)
	}

	if names, _ := ListNames(dir.Path(), RegisteredDir); len(names) != 0 {
		t.Errorf("names after removal = %v", names)
	}
}

func TestNamedFilesRefuseNamesThatEscapeTheDirectory(t *testing.T) {
	dir, _ := newDir(t, nil)
	l := mustLock(t, dir)

	for _, bad := range []string{"../escape", "a/b", ".hidden", ""} {
		if err := l.PublishNamed(RegisteredDir, bad, []byte("x")); !errors.Is(err, ErrBadName) {
			t.Errorf("PublishNamed(%q) = %v", bad, err)
		}

		if err := l.ReplaceNamed(RegisteredDir, bad, []byte("x")); !errors.Is(err, ErrBadName) {
			t.Errorf("ReplaceNamed(%q) = %v", bad, err)
		}

		if err := l.RemoveNamed(RegisteredDir, bad); !errors.Is(err, ErrBadName) {
			t.Errorf("RemoveNamed(%q) = %v", bad, err)
		}

		if _, err := ReadName(dir.Path(), RegisteredDir, bad); !errors.Is(err, ErrBadName) {
			t.Errorf("ReadName(%q) = %v", bad, err)
		}
	}
}

func TestListNamesSkipsTempFilesAndDirectoriesAndMissingDirectories(t *testing.T) {
	dir, _ := newDir(t, nil)

	if names, err := ListNames(dir.Path(), PresenceDir); err != nil || names != nil {
		t.Errorf("missing directory: %v, %v", names, err)
	}

	sub := filepath.Join(dir.Path(), PresenceDir)
	writeRaw(t, filepath.Join(sub, ".tmp-x"), "partial")
	writeRaw(t, filepath.Join(sub, "real"), "x")

	if err := os.MkdirAll(filepath.Join(sub, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	names, err := ListNames(dir.Path(), PresenceDir)
	if err != nil || len(names) != 1 || names[0] != "real" {
		t.Errorf("names = %v, %v", names, err)
	}
}

func TestLockSweepsTempFilesInTheSessionDirectories(t *testing.T) {
	dir, _ := newDir(t, nil)

	var temps []string

	for _, sub := range []string{RegisteredDir, PresenceDir, ExecutingDir} {
		path := filepath.Join(dir.Path(), sub, ".tmp-codex.t1.100.1-2.json")
		writeRaw(t, path, "partial")

		temps = append(temps, path)
	}

	real := filepath.Join(dir.Path(), RegisteredDir, "codex.t1.100.1-2.json")
	writeRaw(t, real, "{}")

	mustLock(t, dir)

	for _, path := range temps {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the sweep: %v", path, err)
		}
	}

	if _, err := os.Stat(real); err != nil {
		t.Errorf("the sweep removed a published session file: %v", err)
	}
}

func TestCreateMarkIsExclusiveAndTakesNoLock(t *testing.T) {
	dir, _ := newDir(t, nil)

	// The lock is held, and the mark still goes in.
	mustLock(t, dir)

	created, err := CreateMark(dir.Path(), EndedDir, "codex.t1.100.1-2")
	if err != nil || !created {
		t.Fatalf("first mark: %v, %v", created, err)
	}

	created, err = CreateMark(dir.Path(), EndedDir, "codex.t1.100.1-2")
	if err != nil || created {
		t.Errorf("second mark: %v, %v; an existing mark counts as success without creating", created, err)
	}

	info, err := os.Stat(filepath.Join(dir.Path(), EndedDir, "codex.t1.100.1-2"))
	if err != nil || info.Size() != 0 {
		t.Errorf("the mark is %v, %v; want an empty file", info, err)
	}

	if _, err := CreateMark(dir.Path(), EndedDir, "../x"); !errors.Is(err, ErrBadName) {
		t.Errorf("a bad name: %v", err)
	}
}

func TestAfterLockRunsUnderTheLockAfterTheSweepAndNeverFailsTheCall(t *testing.T) {
	var (
		ran      int
		warnings []error
		sawTemp  bool
	)

	dir, _ := newDir(t, func(o *Options) {
		o.AfterLock = func(l *Locked) error {
			ran++

			if err := l.PublishNamed(RegisteredDir, "x.json", []byte("{}")); err != nil {
				t.Errorf("AfterLock could not write under the lock: %v", err)
			}

			_, err := os.Stat(filepath.Join(l.Dir().Path(), LeasesDir, ".tmp-"+idA))
			sawTemp = err == nil

			return errors.New("upkeep failed")
		}
		o.Warn = func(err error) { warnings = append(warnings, err) }
	})

	writeRaw(t, filepath.Join(dir.Path(), LeasesDir, ".tmp-"+idA), "partial")

	l, err := dir.Lock()
	if err != nil {
		t.Fatalf("a failing AfterLock failed the call: %v", err)
	}

	_ = l.Unlock()

	if ran != 1 || sawTemp {
		t.Errorf("ran %d times, saw a temp file after the sweep: %v", ran, sawTemp)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "upkeep failed") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestAfterLockDoesNotRunWhenTheFormatMarkerForbidsWrites(t *testing.T) {
	ran := false

	dir, _ := newDir(t, func(o *Options) {
		o.AfterLock = func(*Locked) error {
			ran = true

			return nil
		}
	})

	writeRaw(t, filepath.Join(dir.Path(), "format"), "99\n")

	l, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	_ = l.Unlock()

	if ran {
		t.Error("upkeep ran under a format marker that forbids writes")
	}
}

func TestFormatErrAtReadsTheMarkerWithoutTheLock(t *testing.T) {
	dir, _ := newDir(t, nil)

	if err := FormatErrAt(dir.Path()); err != nil {
		t.Errorf("no marker: %v", err)
	}

	writeRaw(t, filepath.Join(dir.Path(), "format"), "99\n")

	if err := FormatErrAt(dir.Path()); !errors.Is(err, ErrFormatRefused) {
		t.Errorf("a newer marker: %v", err)
	}
}
