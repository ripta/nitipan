package broker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/record"
)

func openTest(t *testing.T, path string, opts StoreOptions) (*Store, error) {
	t.Helper()

	if opts.Volume == nil {
		opts.Volume = localVolume{}
	}

	return OpenStore(path, opts)
}

func TestStoreWritesAVersionedFileThatSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	s, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	next := s.Data().clone()
	next.Nodes["m/1"] = NodeRecord{KeyHash: "h", Label: "a-b-c-d"}
	next.Counter = 7

	if err := s.Persist(next); err != nil {
		t.Fatal(err)
	}

	s.Swap(next)
	_ = s.Close()

	s2, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	defer s2.Close()

	d := s2.Data()
	if d.Version != StoreVersion || d.Nodes["m/1"].Label != "a-b-c-d" || d.Counter != 7 {
		t.Errorf("reloaded = %+v", d)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("store file mode = %v, %v, want 0600", info, err)
	}
}

func TestSecondBrokerOnTheSameStoreFailsOnItsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	first, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = openTest(t, path, StoreOptions{})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second open = %v, want ErrStoreLocked", err)
	}

	_ = first.Close()

	if s, err := openTest(t, path, StoreOptions{}); err != nil {
		t.Errorf("open after the first closed: %v", err)
	} else {
		_ = s.Close()
	}
}

func TestStartRemovesItsOwnLeftoverTempFileOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.json")
	leftover := filepath.Join(dir, tempPrefix+"broker.json")
	other := filepath.Join(dir, tempPrefix+"someone-else")

	for _, p := range []string{leftover, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("own leftover temp file survived: %v", err)
	}

	if _, err := os.Stat(other); err != nil {
		t.Errorf("another file was removed: %v", err)
	}
}

type deniedVolume struct{}

func (deniedVolume) Local(dir string) error {
	return errors.Join(ErrStoreVolume, errors.New(dir+" is on a network volume"))
}

type volumeCase struct {
	Name   string
	Dir    string
	Volume VolumeChecker
	Refuse bool
}

// Dir is relative to the fake home directory, which is also the root of the temp directory.
var volumeCases = []volumeCase{
	{"network share", "nitipan", deniedVolume{}, true},
	{"iCloud Drive via Mobile Documents", "Library/Mobile Documents/com~apple~CloudDocs/nitipan", localVolume{}, true},
	{"iCloud Drive folder name", "iCloud Drive/nitipan", localVolume{}, true},
	{"File Provider folder for Dropbox", "Library/CloudStorage/Dropbox/nitipan", localVolume{}, true},
	{"File Provider folder for Google Drive", "Library/CloudStorage/GoogleDrive-me@example.com/My Drive", localVolume{}, true},
	{"File Provider folder for OneDrive", "Library/CloudStorage/OneDrive-Personal", localVolume{}, true},
	{"the CloudStorage folder itself", "Library/CloudStorage", localVolume{}, true},
	{"legacy Dropbox", "Dropbox/nitipan", localVolume{}, true},
	{"legacy Google Drive", "Google Drive/nitipan", localVolume{}, true},
	{"legacy OneDrive", "OneDrive/nitipan", localVolume{}, true},
	{"legacy OneDrive with a suffix", "OneDrive - Acme Corp/nitipan", localVolume{}, true},
	{"Application Support", "Library/Application Support/nitipan", localVolume{}, false},
	{"a folder merely named like one deeper in", "work/Dropbox-notes/nitipan", localVolume{}, false},
}

func TestStoreRefusesNetworkAndSyncedFolders(t *testing.T) {
	for _, tc := range volumeCases {
		t.Run(tc.Name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(home, tc.Dir, "broker.json")

			s, err := OpenStore(path, StoreOptions{Volume: tc.Volume, Home: home})
			if s != nil {
				_ = s.Close()
			}

			if tc.Refuse != errors.Is(err, ErrStoreVolume) {
				t.Errorf("OpenStore = %v, want refusal %v", err, tc.Refuse)
			}
		})
	}
}

func TestStoreRefusesASymlinkIntoASyncedFolder(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, "Library", "CloudStorage", "Dropbox")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(home, "plain")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err = OpenStore(filepath.Join(link, "broker.json"), StoreOptions{Volume: localVolume{}, Home: home})
	if !errors.Is(err, ErrStoreVolume) {
		t.Errorf("OpenStore through a symlink = %v", err)
	}
}

func TestSystemVolumeAcceptsALocalTempDirectory(t *testing.T) {
	if err := (SystemVolume{}).Local(t.TempDir()); err != nil && !strings.Contains(err.Error(), "cannot check") {
		t.Errorf("the temp directory read as non-local: %v", err)
	}
}

func v(major, minor int) record.Version { return record.Version{Major: major, Minor: minor} }

// seedMajorOne writes a store as a major-1 broker would.
func seedMajorOne(t *testing.T, path string) []byte {
	t.Helper()

	s, err := openTest(t, path, StoreOptions{Current: v(1, 0)})
	if err != nil {
		t.Fatal(err)
	}

	next := s.Data().clone()
	next.Nodes["m/1"] = NodeRecord{KeyHash: "h", Label: "w-x-y-z"}

	if err := s.Persist(next); err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestMajorMigrationKeepsThePreviousFileAndAnOlderBrokerStartsFromIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")
	before := seedMajorOne(t, path)

	migrate := map[int]Migration{1: func(old []byte) ([]byte, error) {
		return []byte(strings.Replace(string(old), `"version":"1.0"`, `"version":"2.0"`, 1)), nil
	}}

	s, err := openTest(t, path, StoreOptions{Current: v(2, 0), Migrations: migrate})
	if err != nil {
		t.Fatal(err)
	}

	if s.Data().Version != v(2, 0) || s.Data().Nodes["m/1"].Label != "w-x-y-z" {
		t.Errorf("migrated store = %+v", s.Data())
	}

	_ = s.Close()

	kept, err := os.ReadFile(path + ".v1")
	if err != nil || string(kept) != string(before) {
		t.Fatalf("kept previous file = %q, %v, want the pre-migration bytes", kept, err)
	}

	// The migrated file is refused by the older broker, as it must be.
	if _, err := openTest(t, path, StoreOptions{Current: v(1, 0)}); !errors.Is(err, ErrStoreNewer) {
		t.Fatalf("older broker on the migrated store = %v, want ErrStoreNewer", err)
	}

	// After a rollback the operator points the older broker at the kept file.
	rolled := filepath.Join(filepath.Dir(path), "rolled.json")
	if err := os.WriteFile(rolled, kept, 0o600); err != nil {
		t.Fatal(err)
	}

	old, err := openTest(t, rolled, StoreOptions{Current: v(1, 0)})
	if err != nil {
		t.Fatalf("older broker could not start from the kept file: %v", err)
	}

	defer old.Close()

	if old.Data().Nodes["m/1"].Label != "w-x-y-z" {
		t.Errorf("older broker lost data: %+v", old.Data())
	}
}

func TestMigrationFailureLeavesTheStoreUntouchedAndKeepsThePreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")
	before := seedMajorOne(t, path)

	migrate := map[int]Migration{1: func([]byte) ([]byte, error) { return nil, errors.New("boom") }}

	if _, err := openTest(t, path, StoreOptions{Current: v(2, 0), Migrations: migrate}); err == nil {
		t.Fatal("open succeeded with a failing migration")
	}

	got, _ := os.ReadFile(path)
	if string(got) != string(before) {
		t.Error("a failed migration changed the store")
	}

	if _, err := os.Stat(path + ".v1"); err != nil {
		t.Errorf("previous file not kept: %v", err)
	}
}

func TestStoreWithAHigherMinorIsRefusedAtStartWithBothVersionsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	s, err := openTest(t, path, StoreOptions{Current: v(1, 3)})
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	_, err = openTest(t, path, StoreOptions{Current: v(1, 0)})
	if !errors.Is(err, ErrStoreNewer) {
		t.Fatalf("older broker on a higher minor = %v, want ErrStoreNewer at start", err)
	}

	for _, want := range []string{"1.3", "1.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name version %s", err, want)
		}
	}
}

func TestStoreKeepsFieldsReservedForLaterWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	s, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	next := s.Data().clone()
	next.Leases = map[string]json.RawMessage{"L1": json.RawMessage(`{"grant":1}`)}
	next.Outcomes = map[string]json.RawMessage{"R1": json.RawMessage(`{"ok":true}`)}
	next.Counter = 41

	if err := s.Persist(next); err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	s2, err := openTest(t, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	defer s2.Close()

	d := s2.Data()
	if string(d.Leases["L1"]) != `{"grant":1}` || string(d.Outcomes["R1"]) != `{"ok":true}` || d.Counter != 41 {
		t.Errorf("reserved fields = %+v", d)
	}
}

func TestAFailedStoreWriteIsFatalAndNotVisibleOrAcked(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	// Removing the directory makes the next temp file creation fail.
	if err := os.RemoveAll(filepath.Dir(h.path)); err != nil {
		t.Fatal(err)
	}

	_, e := h.tryRegister(bob)
	if e == nil || e.Code != "unavailable" {
		t.Fatalf("register during a failed write = %v, want an unavailable refusal", e)
	}

	select {
	case <-h.b.Done():
	default:
		t.Fatal("a failed write did not stop the broker")
	}

	if h.b.Err() == nil {
		t.Error("Err is nil after a fatal write")
	}

	if _, seen := h.store.Data().Nodes[bob.key().String()]; seen {
		t.Error("the node became visible although its write failed")
	}
}
