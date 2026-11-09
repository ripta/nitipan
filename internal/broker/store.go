package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
//
// Minor 1 added pins, reported sessions, and mail. A 1.0 store reads under a 1.1 broker with those empty,
// and an older broker refuses the 1.1 store, since a rewrite would drop the mail it holds.
var StoreVersion = record.Version{Major: 1, Minor: 1}

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

	// Pins holds the approval state of each label: the pinned key hash, the keys waiting for approval,
	// and whether the label was forgotten.
	Pins map[string]PinRecord `json:"pins,omitempty"`

	// Sessions holds every session ID each label ever reported, keyed by label and then session ID. An
	// address resolves against it, so a stored copy of an address never strands.
	Sessions map[string]map[string]SessionRecord `json:"sessions,omitempty"`

	// Mail holds the messages that are queued for a node, and the stage of each one it has sent on.
	Mail map[string]MailRecord `json:"mail,omitempty"`
}

// PinRecord is the approval state of one label.
type PinRecord struct {
	// PinnedHash is the hash of the key the user approved. A pinned label refuses every other key,
	// whether or not its stream is live.
	PinnedHash string `json:"pinned_hash,omitempty"`

	// Pending maps the hash of each key a node presented since the last approval to when it did.
	Pending map[string]time.Time `json:"pending,omitempty"`

	// Awaiting is the fingerprint of a key the user approved before it reached the broker. The label pins
	// the first key that presents it. Without this, a host that floods the label with keys while the real
	// machine sleeps could evict the real key from Pending before the user approves it.
	Awaiting string `json:"awaiting,omitempty"`

	Forgotten   bool      `json:"forgotten,omitempty"`
	ForgottenAt time.Time `json:"forgotten_at,omitzero"`
}

// SessionRecord is one session ID a node reported for its label.
type SessionRecord struct {
	Agent string `json:"agent"`

	// NameVersion is the mail name derivation that applied when the broker first saw the session.
	NameVersion int       `json:"name_version"`
	FirstSeen   time.Time `json:"first_seen"`
}

// MailRecord is one message the broker holds. Its body is dropped once the recipient's node stored it.
type MailRecord struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`

	FromLabel   string `json:"from_label,omitempty"`
	FromAgent   string `json:"from_agent,omitempty"`
	FromSession string `json:"from_session,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	ToLabel     string `json:"to_label"`
	ToSession   string `json:"to_session"`
	ToAddress   string `json:"to_address"`

	Body     string `json:"body,omitempty"`
	Verified bool   `json:"verified,omitempty"`

	// Stage is queued, stored, or read. Sent is true once the broker wrote the message to a stream, and
	// is what keeps a message the broker sent from bouncing as unsent.
	Stage   string    `json:"stage"`
	Sent    bool      `json:"sent,omitempty"`
	StageAt time.Time `json:"stage_at"`

	EnqueuedAt time.Time `json:"enqueued_at"`

	// Reason and Orig are set on a bounce. Bounced and BounceReason are set on the message that bounced.
	Reason       string `json:"reason,omitempty"`
	Orig         string `json:"orig,omitempty"`
	BodyDropped  bool   `json:"body_dropped,omitempty"`
	Bounced      bool   `json:"bounced,omitempty"`
	BounceReason string `json:"bounce_reason,omitempty"`
}

// NodeRecord is what the broker keeps to authenticate one node.
type NodeRecord struct {
	// KeyHash is the SHA-256 of the instance key. The key itself is never stored.
	KeyHash string `json:"key_hash"`
	Label   string `json:"label"`

	// MailCap and HTTPS are what the node reported at its last registration. They let the broker answer
	// "recipient node too old" and refuse mail for a plain HTTP node while the node is offline.
	MailCap int  `json:"mail_cap,omitempty"`
	HTTPS   bool `json:"https,omitempty"`
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

		Pins:     map[string]PinRecord{},
		Sessions: map[string]map[string]SessionRecord{},
		Mail:     map[string]MailRecord{},
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

	out.Pins = make(map[string]PinRecord, len(d.Pins))

	for k, v := range d.Pins {
		v.Pending = maps.Clone(v.Pending)
		out.Pins[k] = v
	}

	out.Sessions = make(map[string]map[string]SessionRecord, len(d.Sessions))

	for k, v := range d.Sessions {
		out.Sessions[k] = maps.Clone(v)
	}

	out.Mail = maps.Clone(d.Mail)

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

	if d.Mail == nil {
		d.Mail = map[string]MailRecord{}
	}

	if d.Pins == nil {
		d.Pins = map[string]PinRecord{}
	}

	if d.Sessions == nil {
		d.Sessions = map[string]map[string]SessionRecord{}
	}

	s.data = &d

	// A store of an older minor is stamped with this broker's version as it loads. The fields this
	// binary adds are written from the first persist on, and an older broker must refuse the file then.
	if head.Version.Major < cur.Major || head.Version.Minor < cur.Minor {
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
