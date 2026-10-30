package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/record"
)

// StoreVersion is the store format this binary writes and the highest it reads. A store of a higher
// major or a higher minor is refused at start. A rewrite would drop the fields this binary does not
// know, and a broker that cannot write cannot register a node.
var StoreVersion = record.Version{Major: 1, Minor: 0}

// Errors returned by the store.
var (
	// ErrStoreLocked means another broker holds the store.
	ErrStoreLocked = errors.New("another broker holds this store")

	// ErrStoreNewer means the store was written by a newer broker.
	ErrStoreNewer = errors.New("store was written by a newer broker")

	// ErrStoreVolume means the store path is on a volume where fsync may not reach the disk.
	ErrStoreVolume = errors.New("store path is not on a local volume")
)

// VolumeChecker says whether a path is on a local volume that is not synced by a service.
type VolumeChecker interface {
	Local(dir string) error
}

// Data is everything the broker persists. Mirrors and event queues are not here, since a node
// resyncs them from a snapshot when it reconnects.
type Data struct {
	Version record.Version `json:"version"`

	// Nodes holds the key each (machine, uid) pair registered, keyed by NodeKey.String().
	Nodes map[string]NodeRecord `json:"nodes"`

	// Labels is the seen-label table. It validates addresses and never allocates a label.
	Labels map[string]LabelRecord `json:"labels"`

	// Leases, Outcomes, and Counter are reserved for the broker's own grants. Their shape is the
	// grant code's to define, and the store keeps whatever it holds.
	Leases   map[string]json.RawMessage `json:"leases,omitempty"`
	Outcomes map[string]json.RawMessage `json:"outcomes,omitempty"`
	Counter  uint64                     `json:"counter,omitempty"`
}

// NodeRecord is what the broker keeps to authenticate one node.
type NodeRecord struct {
	// KeyHash is the SHA-256 of the instance key. The key itself is never stored.
	KeyHash string `json:"key_hash"`
	Label   string `json:"label"`
}

// LabelRecord is one row of the seen-label table.
type LabelRecord struct {
	MachineID string `json:"machine_id"`
	UID       int    `json:"uid"`

	// Host and User are what the node last reported, for display.
	Host string `json:"host"`
	User string `json:"user"`

	// Derivation names the label derivation that produced the label. A later derivation keeps old
	// labels valid by reading this.
	Derivation int `json:"derivation"`

	// LastSeen is wall-clock time, for display and for pruning.
	LastSeen time.Time `json:"last_seen"`
}

func newData() *Data {
	return &Data{
		Version: StoreVersion,
		Nodes:   map[string]NodeRecord{},
		Labels:  map[string]LabelRecord{},
	}
}

func (d *Data) clone() *Data {
	out := *d
	out.Nodes = make(map[string]NodeRecord, len(d.Nodes))
	out.Labels = make(map[string]LabelRecord, len(d.Labels))

	for k, v := range d.Nodes {
		out.Nodes[k] = v
	}

	for k, v := range d.Labels {
		out.Labels[k] = v
	}

	if d.Leases != nil {
		out.Leases = make(map[string]json.RawMessage, len(d.Leases))
		for k, v := range d.Leases {
			out.Leases[k] = v
		}
	}

	if d.Outcomes != nil {
		out.Outcomes = make(map[string]json.RawMessage, len(d.Outcomes))
		for k, v := range d.Outcomes {
			out.Outcomes[k] = v
		}
	}

	return &out
}

// Migration turns the bytes of a store at one major into the bytes of the next major.
type Migration func(old []byte) ([]byte, error)

// StoreOptions tune a store. The zero value is the production setup.
type StoreOptions struct {
	// Current is the store version this broker writes. Default: StoreVersion. A test sets it to
	// stand in for an older or newer broker.
	Current record.Version

	// Migrations are keyed by the major they read. A store at an older major runs through them in
	// order, and the previous file is kept first.
	Migrations map[int]Migration

	// Volume checks the store's directory. Default: the system check.
	Volume VolumeChecker

	// Home is the home directory whose legacy sync folders are refused. Default: the user database
	// entry.
	Home string
}

// Store is the broker's one file, held under a flock.
type Store struct {
	path string
	lock *os.File
	opts StoreOptions
	data *Data
}

const (
	tempPrefix = ".tmp-"
	lockSuffix = ".lock"
)

// OpenStore opens the store at path, creating it when missing, and takes its lock.
func OpenStore(path string, opts StoreOptions) (*Store, error) {
	if opts.Current == (record.Version{}) {
		opts.Current = StoreVersion
	}

	if opts.Volume == nil {
		opts.Volume = SystemVolume{}
	}

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("store path %q: want an absolute path", path)
	}

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the store directory %s: %w", dir, err)
	}

	if opts.Home == "" {
		if u, err := user.Current(); err == nil {
			opts.Home = u.HomeDir
		}
	}

	if err := checkSyncedFolder(dir, opts.Home); err != nil {
		return nil, err
	}

	if err := opts.Volume.Local(dir); err != nil {
		return nil, err
	}

	lock, err := os.OpenFile(path+lockSuffix, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the store lock: %w", err)
	}

	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()

		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w", path, ErrStoreLocked)
		}

		return nil, fmt.Errorf("locking the store: %w", err)
	}

	s := &Store{path: path, lock: lock, opts: opts}

	if err := s.load(); err != nil {
		_ = s.Close()

		return nil, err
	}

	return s, nil
}

// Close releases the lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}

	err := s.lock.Close()
	s.lock = nil

	if err != nil {
		return fmt.Errorf("releasing the store lock: %w", err)
	}

	return nil
}

// Data returns the current state. Callers treat it as read-only.
func (s *Store) Data() *Data {
	return s.data
}

func (s *Store) load() error {
	s.removeLeftovers()

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.data = newData()
		s.data.Version = s.opts.Current

		return s.Persist(s.data)
	}

	if err != nil {
		return fmt.Errorf("reading the store %s: %w", s.path, err)
	}

	var head struct {
		Version record.Version `json:"version"`
	}

	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("decoding the store %s: %w", s.path, err)
	}

	cur := s.opts.Current

	switch {
	case head.Version.Major > cur.Major:
		return fmt.Errorf("%s is version %s, this broker reads %d.x: %w", s.path, head.Version, cur.Major, ErrStoreNewer)
	case head.Version.Major == cur.Major && head.Version.Minor > cur.Minor:
		return fmt.Errorf("%s is version %s, this broker writes %s and cannot rewrite a newer minor. "+
			"Run the newer broker, or restore the store file from before the upgrade: %w", s.path, head.Version, cur, ErrStoreNewer)
	case head.Version.Major < cur.Major:
		raw, err = s.migrate(raw, head.Version)
		if err != nil {
			return err
		}
	}

	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("decoding the store %s: %w", s.path, err)
	}

	if d.Nodes == nil {
		d.Nodes = map[string]NodeRecord{}
	}

	if d.Labels == nil {
		d.Labels = map[string]LabelRecord{}
	}

	s.data = &d

	if head.Version.Major < cur.Major {
		d.Version = cur

		return s.Persist(&d)
	}

	return nil
}

// migrate keeps the previous file beside the store, then runs each migration from the store's major
// up to the current one. The kept file is written in full before the store changes, so an older
// broker can start from it after a rollback.
func (s *Store) migrate(raw []byte, from record.Version) ([]byte, error) {
	keep := fmt.Sprintf("%s.v%d", s.path, from.Major)

	if err := writeFileSynced(keep, raw); err != nil {
		return nil, fmt.Errorf("keeping the previous store as %s: %w", keep, err)
	}

	for major := from.Major; major < s.opts.Current.Major; major++ {
		step, ok := s.opts.Migrations[major]
		if !ok {
			return nil, fmt.Errorf("no migration from store major %d: %w", major, ErrStoreNewer)
		}

		var err error

		raw, err = step(raw)
		if err != nil {
			return nil, fmt.Errorf("migrating the store from major %d: %w", major, err)
		}
	}

	return raw, nil
}

// Persist writes d through a temp file, fsync, rename, and a directory sync. The caller swaps d into
// memory only after it returns, and acks only after that.
func (s *Store) Persist(d *Data) error {
	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding the store: %w", err)
	}

	tmp := filepath.Join(filepath.Dir(s.path), tempPrefix+filepath.Base(s.path))

	if err := writeFileSynced(tmp, append(data, '\n')); err != nil {
		return err
	}

	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("replacing the store: %w", err)
	}

	return syncDir(filepath.Dir(s.path))
}

// Swap installs d as the current state. Call it after Persist succeeded.
func (s *Store) Swap(d *Data) {
	s.data = d
}

// removeLeftovers deletes this store's own temp file, left by a crash between write and rename.
// Other files are never touched.
func (s *Store) removeLeftovers() {
	_ = os.Remove(filepath.Join(filepath.Dir(s.path), tempPrefix+filepath.Base(s.path)))
}

func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		return fmt.Errorf("writing %s: %w", path, err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()

		return fmt.Errorf("syncing %s: %w", path, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}

	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s to sync it: %w", dir, err)
	}

	defer d.Close()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}

	return nil
}

// legacySyncedFolders are the folders older sync clients keep directly under the home directory.
var legacySyncedFolders = []string{"Dropbox", "Google Drive", "OneDrive", "iCloud Drive", "Box", "Box Sync"}

// checkSyncedFolder refuses a directory that a sync service owns. Such a service can hold a file
// back or replace it, so a persisted write is not durable.
//
// It matches by path only. The File Provider folders under ~/Library/CloudStorage and
// ~/Library/Mobile Documents cover iCloud Drive, Dropbox, Google Drive, OneDrive, and Box on current
// macOS. The older top-level folders under the home directory are matched by name. A sync tool that
// syncs an arbitrary folder, and iCloud's Desktop and Documents sync, look like any other path and
// pass.
func checkSyncedFolder(dir, home string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}

	resolvedHome := home
	if h, err := filepath.EvalSymlinks(home); err == nil {
		resolvedHome = h
	}

	for _, p := range []string{dir, resolved} {
		for _, marker := range []string{"/Library/Mobile Documents", "/Library/CloudStorage"} {
			if strings.HasSuffix(p, marker) || strings.Contains(p, marker+"/") {
				return fmt.Errorf("%s is in a folder that a sync service owns: %w", dir, ErrStoreVolume)
			}
		}

		for _, h := range []string{home, resolvedHome} {
			if h == "" {
				continue
			}

			rest, ok := strings.CutPrefix(p, strings.TrimSuffix(h, "/")+"/")
			if !ok {
				continue
			}

			top, _, _ := strings.Cut(rest, "/")
			if isLegacySyncedFolder(top) {
				return fmt.Errorf("%s is in %s, a folder that a sync service owns: %w", dir, top, ErrStoreVolume)
			}
		}
	}

	return nil
}

func isLegacySyncedFolder(name string) bool {
	if strings.HasPrefix(name, "OneDrive") {
		return true
	}

	return slices.Contains(legacySyncedFolders, name)
}

// DefaultStorePath is the store location under the user's Application Support folder.
func DefaultStorePath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "nitipan", "broker.json")
}
