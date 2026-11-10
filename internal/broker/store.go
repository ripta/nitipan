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
//
// Major 2 adds user identity, bindings, admissions, delegations, mail grants, and conversations. The
// migration binds every pinned label to the owner, creates the owner user, and rewrites acting_for on
// leases held by those labels. An older broker refuses the 2.0 store.
var StoreVersion = record.Version{Major: 2, Minor: 0}

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

	// OwnerID is the broker owner's stable user ID, assigned when the broker is first configured: when
	// the store is created, upgraded from 1.x, or first opened at 2.0 without one. Every label pinned
	// before the upgrade to store 2.0 is bound to it then.
	OwnerID string `json:"owner_id,omitempty"`

	// Users maps each user ID to their record. The owner is one of these.
	Users map[string]UserRecord `json:"users,omitempty"`

	// IssuerSubs maps each issuer plus sub to the user ID it resolves to. The key is
	// "<issuer>\x00<sub>", so no issuer can be shifted into the sub part.
	IssuerSubs map[string]string `json:"issuer_subs,omitempty"`

	// PendingAdmissions maps each unadmitted issuer plus sub to when it first signed in and its email.
	PendingAdmissions map[string]AdmissionRecord `json:"pending_admissions,omitempty"`

	// Bindings maps each label to its binding: the bound user ID, the last bound user, and the
	// derivation version used.
	Bindings map[string]BindingRecord `json:"bindings,omitempty"`

	// Delegations maps each user ID to the set of labels they delegated to. The inner map is keyed
	// by label for fast lookup.
	Delegations map[string]map[string]time.Time `json:"delegations,omitempty"`

	// MailGrants maps each user ID to the set of user IDs they granted mail from. The inner map is
	// keyed by sender user ID for fast lookup.
	MailGrants map[string]map[string]time.Time `json:"mail_grants,omitempty"`

	// Conversations holds each conversation of mail across users, keyed by the ID of its first message.
	Conversations map[string]ConversationRecord `json:"conversations,omitempty"`

	// CrossUserEnabled is true once the owner turns cross-user features on. It stays false while no
	// identity provider is configured or while any label reported plain HTTP.
	CrossUserEnabled bool `json:"cross_user_enabled,omitempty"`

	// KeySchemes maps the hash of each key a node registered with to the scheme it reported: "http" or
	// "https". A key that once reported "http" keeps it, whatever it reports later, since it may have been
	// read on the plain hop. A key with no entry never reported, as a key from before scheme reports did not.
	// The map keeps the mark after a forget, so a key that crossed plain HTTP and is approved again still
	// blocks cross-user features.
	KeySchemes map[string]string `json:"key_schemes,omitempty"`

	// PairingCodes maps each label to its active pairing code: the code's hash, the hash of the pending key
	// that sent it, and its expiry.
	PairingCodes map[string]PairingCodeRecord `json:"pairing_codes,omitempty"`
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

	// SenderUser is the sender's user label, set only on mail from another user, which is never Verified.
	// Conversation names the conversation whose grant admitted it. ReplyTo names the message it answers,
	// and is empty on a conversation's first message.
	SenderUser   string `json:"sender_user,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	ReplyTo      string `json:"reply_to,omitempty"`

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

// UserRecord is one user the broker knows. The owner is one of these.
type UserRecord struct {
	ID string `json:"id"`

	// UserLabel is the four-word label derived from the user ID. It names the user in acting_for,
	// cross-user mail, delegations, and grants.
	UserLabel string `json:"user_label"`

	// LabelDerivation is the version of the user label derivation.
	LabelDerivation int `json:"label_derivation"`

	// Email is what the identity provider reported, for display only.
	Email string `json:"email,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// AdmissionRecord is one unadmitted issuer plus sub that signed in.
type AdmissionRecord struct {
	Issuer string `json:"issuer"`
	Sub    string `json:"sub"`
	Email  string `json:"email"`

	FirstSeen time.Time `json:"first_seen"`
}

// BindingRecord is one label's binding to a user.
type BindingRecord struct {
	// UserID is the user the label is bound to, and empty once the label is forgotten. A label counts as
	// bound only while it is also pinned under its node's current key.
	UserID string `json:"user_id"`

	// LastBoundUser is the last user the label was bound to. Forget and revoke-user clear UserID and keep
	// it, so the next binding can tell a different user from the same one.
	LastBoundUser string `json:"last_bound_user"`

	// DerivationVersion is the user label derivation version that applied when this binding was made.
	DerivationVersion int `json:"derivation_version"`

	BoundAt time.Time `json:"bound_at"`
}

// ConversationRecord is one conversation of mail across users. SenderUser sent its first message and
// RecipientUser's grant to SenderUser admitted it. Every message of the conversation, replies in both
// directions included, rides that one grant, so revoking it stops them all.
//
// The broker keeps the record while any of its messages is held, and until 7 days after its last read.
type ConversationRecord struct {
	SenderUser    string `json:"sender_user"`
	RecipientUser string `json:"recipient_user"`

	CreatedAt       time.Time `json:"created_at"`
	LastMessageRead time.Time `json:"last_message_read,omitzero"`
}

// PairingCodeRecord is one label's active pairing code.
type PairingCodeRecord struct {
	// Hash is the SHA-256 of the pairing code the node printed.
	Hash string `json:"hash"`

	// KeyHash is the hash of the pending key that sent the code. A bind by the code pins that key and no
	// other, so a code never pins a key some other host registered under the label.
	KeyHash string `json:"key_hash"`

	ExpiresAt time.Time `json:"expires_at"`

	// WrongAttempts counts the wrong codes one signer submitted for this label. Five locks the signer
	// out of this label until expiry.
	WrongAttempts map[string]int `json:"wrong_attempts,omitempty"`
}

func newData() *Data {
	return &Data{
		Version: StoreVersion,
		Nodes:   map[string]NodeRecord{},
		Labels:  map[string]LabelRecord{},

		Pins:     map[string]PinRecord{},
		Sessions: map[string]map[string]SessionRecord{},
		Mail:     map[string]MailRecord{},

		Users:             map[string]UserRecord{},
		IssuerSubs:        map[string]string{},
		PendingAdmissions: map[string]AdmissionRecord{},
		Bindings:          map[string]BindingRecord{},
		Delegations:       map[string]map[string]time.Time{},
		MailGrants:        map[string]map[string]time.Time{},
		Conversations:     map[string]ConversationRecord{},
		KeySchemes:        map[string]string{},
		PairingCodes:      map[string]PairingCodeRecord{},
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

	out.Users = maps.Clone(d.Users)
	out.IssuerSubs = maps.Clone(d.IssuerSubs)
	out.PendingAdmissions = maps.Clone(d.PendingAdmissions)
	out.Bindings = maps.Clone(d.Bindings)

	out.Delegations = make(map[string]map[string]time.Time, len(d.Delegations))
	for k, v := range d.Delegations {
		out.Delegations[k] = maps.Clone(v)
	}

	out.MailGrants = make(map[string]map[string]time.Time, len(d.MailGrants))
	for k, v := range d.MailGrants {
		out.MailGrants[k] = maps.Clone(v)
	}

	out.Conversations = maps.Clone(d.Conversations)
	out.KeySchemes = maps.Clone(d.KeySchemes)

	out.PairingCodes = make(map[string]PairingCodeRecord, len(d.PairingCodes))
	for k, v := range d.PairingCodes {
		v.WrongAttempts = maps.Clone(v.WrongAttempts)
		out.PairingCodes[k] = v
	}

	return &out
}

// Migration turns the bytes of a store at one major into the bytes of the next major. now is the store's
// clock, for any record the migration creates.
type Migration func(old []byte, now time.Time) ([]byte, error)

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

	// Now is the wall clock the store stamps the records it creates on open with, such as the owner's
	// user record and each binding a migration makes. Default: time.Now.
	Now func() time.Time
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

	if opts.Now == nil {
		opts.Now = time.Now
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
		d := newData()
		d.Version = s.opts.Current

		if s.ownsUsers() {
			assignOwner(d, s.opts.Now())
		}

		if err := s.Persist(d); err != nil {
			return err
		}

		s.data = d

		return nil
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

	d.fillMaps()

	s.data = &d

	// A store of an older minor is stamped with this broker's version as it loads. The fields this
	// binary adds are written from the first persist on, and an older broker must refuse the file then.
	dirty := false

	if head.Version.Major < cur.Major || head.Version.Minor < cur.Minor {
		d.Version = cur
		dirty = true
	}

	// A store created before owners were assigned on creation has none. Its owner is assigned here, once,
	// and an upgraded store keeps the one its migration assigned.
	//
	// A store whose owner ID names no user gets the owner's record back under that ID, so the broker always
	// has an owner once its store is open.
	if s.ownsUsers() {
		_, hasRecord := d.Users[d.OwnerID]

		switch {
		case d.OwnerID == "":
			assignOwner(&d, s.opts.Now())
			dirty = true
		case !hasRecord:
			restoreOwner(&d, s.opts.Now())
			dirty = true
		}
	}

	if dirty {
		return s.Persist(&d)
	}

	return nil
}

// fillMaps gives every collection a store file left out an empty map, so callers never write to a nil one.
func (d *Data) fillMaps() {
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

	if d.Users == nil {
		d.Users = map[string]UserRecord{}
	}

	if d.IssuerSubs == nil {
		d.IssuerSubs = map[string]string{}
	}

	if d.PendingAdmissions == nil {
		d.PendingAdmissions = map[string]AdmissionRecord{}
	}

	if d.Bindings == nil {
		d.Bindings = map[string]BindingRecord{}
	}

	if d.Delegations == nil {
		d.Delegations = map[string]map[string]time.Time{}
	}

	if d.MailGrants == nil {
		d.MailGrants = map[string]map[string]time.Time{}
	}

	if d.Conversations == nil {
		d.Conversations = map[string]ConversationRecord{}
	}

	if d.KeySchemes == nil {
		d.KeySchemes = map[string]string{}
	}

	if d.PairingCodes == nil {
		d.PairingCodes = map[string]PairingCodeRecord{}
	}
}

// ownsUsers reports whether the store version this broker writes has users, and so a broker owner. A
// store at major 1 predates them.
func (s *Store) ownsUsers() bool {
	return s.opts.Current.Major >= 2
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

		raw, err = step(raw, s.opts.Now())
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

// StripHashFromKeptFile removes the key hash for a label from the kept .v1 file, so a rollback
// refuses it. The kept file holds the hashes of the keys pinned before the migration to major 2.
// It does what v1's Forget did: sets forgotten=true and deletes the node record.
func (s *Store) StripHashFromKeptFile(label string) error {
	keptPath := fmt.Sprintf("%s.v1", s.path)

	raw, err := os.ReadFile(keptPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("reading the kept file %s: %w", keptPath, err)
	}

	var full map[string]interface{}
	if err := json.Unmarshal(raw, &full); err != nil {
		return fmt.Errorf("decoding the kept file: %w", err)
	}

	labelRec := s.data.Labels[label]
	nk := NodeKey{MachineID: labelRec.MachineID, UID: labelRec.UID}

	nodes, _ := full["nodes"].(map[string]interface{})
	if nodes != nil {
		delete(nodes, nk.String())
	}

	pins, _ := full["pins"].(map[string]interface{})
	if pins != nil {
		if pinRec, ok := pins[label].(map[string]interface{}); ok {
			pinRec["forgotten"] = true
			pinRec["pinned_hash"] = ""
		}
	}

	updated, err := json.Marshal(full)
	if err != nil {
		return fmt.Errorf("encoding the kept file: %w", err)
	}

	if err := writeFileSynced(keptPath, append(updated, '\n')); err != nil {
		return fmt.Errorf("writing the kept file: %w", err)
	}

	return nil
}

// DeleteKeptFile removes the kept .v1 file. Once it is removed, a rollback is impossible.
func (s *Store) DeleteKeptFile() error {
	keptPath := fmt.Sprintf("%s.v1", s.path)

	if err := os.Remove(keptPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the kept file %s: %w", keptPath, err)
	}

	return nil
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
