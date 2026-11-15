package broker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/space-armada/nitipan/internal/peercred"
	"github.com/space-armada/nitipan/internal/wire"
)

// maxPendingKeys bounds the keys waiting for approval under one label, so a host that keeps
// registering cannot grow the store. The oldest goes first.
const maxPendingKeys = 8

// Errors the approval commands return.
var (
	// ErrAdminPeer means the process on the admin socket is not the broker's OS user.
	ErrAdminPeer = errors.New("the admin socket accepts only the broker's own OS user")
)

// fingerprintMatches returns true if the hash's fingerprint equals the fingerprint, in constant time.
func fingerprintMatches(hash, fingerprint string) bool {
	return subtle.ConstantTimeCompare([]byte(wire.Fingerprint(hash)), []byte(fingerprint)) == 1
}

// notePending records a key a node presented, unless the label is pinned to it. It runs inside a
// commit.
func notePending(d *Data, label, hash string, now time.Time) {
	pin := d.Pins[label]
	if pin.PinnedHash != "" {
		return
	}

	if pin.Pending == nil {
		pin.Pending = map[string]time.Time{}
	}

	pin.Pending[hash] = now

	for len(pin.Pending) > maxPendingKeys {
		oldest := ""

		for h, at := range pin.Pending {
			if oldest == "" || at.Before(pin.Pending[oldest]) {
				oldest = h
			}
		}

		delete(pin.Pending, oldest)
	}

	d.Pins[label] = pin
}

// PendingKey is a key that waits for approval.
type PendingKey struct {
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

// PendingLabel is one label's approval state, for the pending command.
type PendingLabel struct {
	Label     string       `json:"label"`
	Host      string       `json:"host"`
	User      string       `json:"user"`
	Pinned    bool         `json:"pinned"`
	Forgotten bool         `json:"forgotten,omitempty"`
	Keys      []PendingKey `json:"keys,omitempty"`

	// Awaiting is the fingerprint the user approved that no key has presented yet.
	Awaiting string `json:"awaiting,omitempty"`
}

// Pending lists every label that is not approved, or has a key waiting. A label that is pinned and has
// no key waiting is left out.
func (b *Broker) Pending() []PendingLabel {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []PendingLabel

	for label, row := range b.store.Data().Labels {
		pin := b.store.Data().Pins[label]
		if pin.PinnedHash != "" && len(pin.Pending) == 0 && pin.Awaiting == "" {
			continue
		}

		pl := PendingLabel{Label: label, Host: row.Host, User: row.User, Pinned: pin.PinnedHash != "", Forgotten: pin.Forgotten, Awaiting: pin.Awaiting}

		for h, at := range pin.Pending {
			pl.Keys = append(pl.Keys, PendingKey{Fingerprint: wire.Fingerprint(h), At: at})
		}

		sort.Slice(pl.Keys, func(i, j int) bool { return pl.Keys[i].Fingerprint < pl.Keys[j].Fingerprint })

		out = append(out, pl)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })

	return out
}

// ErrAlreadyPinned means the label is approved with a key. Forget it before approving another.
var ErrAlreadyPinned = errors.New("run nitipan broker forget first if its machine was reinstalled")

// ErrBadFingerprint means the text is not a key fingerprint, four groups of four hex digits.
var ErrBadFingerprint = errors.New("not a key fingerprint, which is four groups of four hex digits such as 0123-4567-89ab-cdef")

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{4}(-[0-9a-f]{4}){3}$`)

// Approve pins the pending key of a label that has the fingerprint, and discards every other pending
// key for the label, so a host that submits a key of its own cannot block the approval. It reports
// whether a key was pinned, and the owner it binds the label to.
//
// A fingerprint no pending key has is recorded as awaited, and the label pins the first key that
// presents it. A bounded list of pending keys can be flooded off by a host that registers fresh keys
// while the real machine sleeps, and approval must not depend on that list. The fingerprint is a 64-bit
// prefix of the key's hash, so a host cannot present a key that matches without the user's own.
//
// Approval binds the label to the broker owner, since a command on the broker host is the owner's. An
// awaited key binds when it pins. With no owner record, approval pins the key alone.
//
// Approving a label again under its pinned key binds it, so a label pinned but left unbound has a way
// back.
//
// A label a coworker bound through the page is refused until it is forgotten. Taking it for the owner
// would bounce the coworker's mail.
//
// It is reachable only through the admin socket and by a caller in this process. No HTTP route and no
// request handoff kind calls it, since a session can reach both.
func (b *Broker) Approve(label, fingerprint string) (AdminApproved, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if !fingerprintPattern.MatchString(fingerprint) {
		return AdminApproved{}, fmt.Errorf("%q: %w", fingerprint, ErrBadFingerprint)
	}

	row, ok := b.store.Data().Labels[label]
	if !ok {
		return AdminApproved{}, fmt.Errorf("label %q: %w", label, ErrUnknownLabel)
	}

	owner, hasOwner := b.ownerLocked()
	pin := b.store.Data().Pins[label]

	var hash string

	switch {
	case pin.PinnedHash != "" && !fingerprintMatches(pin.PinnedHash, fingerprint):
		return AdminApproved{}, fmt.Errorf("label %q is already approved with another key: %w", label, ErrAlreadyPinned)
	case pin.PinnedHash != "":
		hash = pin.PinnedHash
	default:
		for h := range pin.Pending {
			if fingerprintMatches(h, fingerprint) {
				hash = h
			}
		}
	}

	if hash == "" {
		err := b.commit(func(d *Data) error {
			pin := d.Pins[label]
			pin.Awaiting = fingerprint
			d.Pins[label] = pin

			return nil
		})

		return AdminApproved{Owner: owner.UserLabel}, err
	}

	bound, isBound := b.labelBoundUserLocked(label)

	switch {
	case !hasOwner && pin.PinnedHash != "":
		return AdminApproved{Pinned: true}, nil
	case !hasOwner:
		return AdminApproved{Pinned: true}, b.pinLocked(label, NodeKey{MachineID: row.MachineID, UID: row.UID}, hash)
	case pin.PinnedHash != "" && isBound && bound.ID == owner.ID:
		return AdminApproved{Pinned: true, Owner: owner.UserLabel}, nil
	case pin.PinnedHash != "" && isBound:
		return AdminApproved{}, fmt.Errorf("label %q is bound to the user %s: %w", label, bound.UserLabel, ErrAlreadyPinned)
	}

	return AdminApproved{Pinned: true, Owner: owner.UserLabel}, b.bindLocked(label, owner, hash, b.cfg.Now())
}

// ownerLocked returns the broker owner's user record, and false when the broker has none.
func (b *Broker) ownerLocked() (UserRecord, bool) {
	d := b.store.Data()

	if d.OwnerID == "" {
		return UserRecord{}, false
	}

	owner, ok := d.Users[d.OwnerID]

	return owner, ok
}

// pinLocked pins a key to a label, makes it the node's key, and closes a stream opened under another.
func (b *Broker) pinLocked(label string, nk NodeKey, hash string) error {
	prev := b.store.Data().Nodes[nk.String()].KeyHash

	err := b.commit(func(d *Data) error {
		d.Pins[label] = PinRecord{PinnedHash: hash}

		if rec, ok := d.Nodes[nk.String()]; ok {
			rec.KeyHash = hash
			d.Nodes[nk.String()] = rec
		}

		return nil
	})
	if err != nil {
		return err
	}

	// A node registered under another key holds a stream that the pinned key does not own.
	if ns, ok := b.nodes[nk]; ok && ns.stream != nil && prev != hash {
		ns.stream.cancel()
	}

	b.wakeLabelLocked(label)

	return nil
}

// Forget unpins a label, drops its node's key and its pairing code, and settles its mail. Mail still
// queued waits for the label's next approved key and is never handed to an unapproved one. Mail already
// stored on the forgotten machine and never read bounces to its sender without its body, since the broker
// no longer holds it.
//
// The label's binding loses its user and keeps its last bound user, so the next binding can tell a new
// user from the same one. Delegations to the label stay, and act on nothing while the label is unbound. A
// binding to the same user, such as a reinstalled machine, keeps them. A binding to another user drops
// them.
func (b *Broker) Forget(label string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.forgetLocked(label)
}

// forgetLocked forgets one label in its own commit, as Forget describes.
func (b *Broker) forgetLocked(label string) error {
	if _, ok := b.store.Data().Labels[label]; !ok {
		return fmt.Errorf("label %q: %w", label, ErrUnknownLabel)
	}

	now := b.cfg.Now()

	err := b.commit(func(d *Data) error {
		b.forgetInto(d, label, now)

		return nil
	})
	if err != nil {
		return err
	}

	b.forgottenLocked(label)

	return nil
}

// forgetInto makes a forget's changes to the store inside a commit, so a caller can forget several labels
// in one write. Its caller runs forgottenLocked for each label once the commit lands.
func (b *Broker) forgetInto(d *Data, label string, now time.Time) {
	row := d.Labels[label]
	nk := NodeKey{MachineID: row.MachineID, UID: row.UID}

	d.Pins[label] = PinRecord{Forgotten: true, ForgottenAt: now}
	delete(d.Nodes, nk.String())

	if binding, ok := d.Bindings[label]; ok && binding.UserID != "" {
		binding.LastBoundUser = binding.UserID
		binding.UserID = ""
		d.Bindings[label] = binding
	}

	delete(d.PairingCodes, label)

	for id, m := range d.Mail {
		if m.ToLabel != label || m.Bounced {
			continue
		}

		switch m.Stage {
		case wire.StageStored:
			if m.Kind == wire.EventMail {
				b.bounceInto(d, m, wire.ReasonForgotten, false, now)
			}
		case wire.StageQueued:
			// An ack lost before the wipe can no longer be told apart from a message that never
			// arrived. The next approved key receives it again.
			m.Sent = false
			d.Mail[id] = m
		}
	}
}

// forgottenLocked does what a forget does outside the store, once its commit landed. It closes the
// label's stream, drops its live sessions, and strips its hash from the kept file.
func (b *Broker) forgottenLocked(label string) {
	row := b.store.Data().Labels[label]

	if ns, ok := b.nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}]; ok {
		if ns.stream != nil {
			ns.stream.cancel()
		}

		ns.liveSessions = nil
	}

	if err := b.store.StripHashFromKeptFile(label); err != nil {
		b.cfg.Logger.Error("stripping hash from kept file", "label", label, "error", err)
	}
}

// wakeLabelLocked wakes the stream of the node that owns a label, so queued mail goes out.
func (b *Broker) wakeLabelLocked(label string) {
	row, ok := b.store.Data().Labels[label]
	if !ok {
		return
	}

	ns, ok := b.nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}]
	if !ok || ns.stream == nil {
		return
	}

	select {
	case ns.stream.wake <- struct{}{}:
	default:
	}
}

// pinnedLocked reports whether a label is pinned to its node's current key. Mail moves only for such a
// node.
func (b *Broker) pinnedLocked(label string) bool {
	row, ok := b.store.Data().Labels[label]
	if !ok {
		return false
	}

	rec, ok := b.store.Data().Nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}.String()]
	if !ok {
		return false
	}

	pin := b.store.Data().Pins[label].PinnedHash

	return pin != "" && pin == rec.KeyHash
}

// Admin request bodies.
type adminApprove struct {
	Label       string `json:"label"`
	Fingerprint string `json:"fingerprint"`
}

type adminForget struct {
	Label string `json:"label"`
}

// AdminApproved answers the approve command. Pinned is false when the fingerprint is awaited.
type AdminApproved struct {
	Pinned bool `json:"pinned"`

	// Owner is the user label of the broker owner the label is bound to, or binds to once an awaited key
	// pins. It is empty when the store's owner record is missing, and nothing is bound.
	Owner string `json:"owner,omitempty"`
}

// AdminPending answers the pending command.
type AdminPending struct {
	Labels []PendingLabel `json:"labels"`
}

// Paths the admin socket serves. They exist only there. The network handler serves none of them.
const (
	AdminPathPending        = "/pending"
	AdminPathApprove        = "/approve"
	AdminPathForget         = "/forget"
	AdminPathConfirmUpgrade = "/confirm-upgrade"
	AdminPathAdmissions     = "/admissions"
	AdminPathAdmit          = "/admit"
	AdminPathLink           = "/link"
	AdminPathBindings       = "/bindings"
	AdminPathRevokeUser     = "/revoke-user"
	AdminPathCrossUser      = "/cross-user"
	AdminPathBreak          = "/break"
)

// AdminHandler serves the approval commands. It is mounted on the admin socket only.
func (b *Broker) AdminHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET "+AdminPathPending, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, AdminPending{Labels: b.Pending()})
	})
	mux.HandleFunc("POST "+AdminPathApprove, b.handleAdminApprove)
	mux.HandleFunc("POST "+AdminPathForget, b.handleAdminForget)
	mux.HandleFunc("POST "+AdminPathConfirmUpgrade, b.handleAdminConfirmUpgrade)
	mux.HandleFunc("GET "+AdminPathAdmissions, b.handleAdminAdmissions)
	mux.HandleFunc("POST "+AdminPathAdmit, b.handleAdminAdmit)
	mux.HandleFunc("POST "+AdminPathLink, b.handleAdminLink)
	mux.HandleFunc("GET "+AdminPathBindings, b.handleAdminBindings)
	mux.HandleFunc("POST "+AdminPathRevokeUser, b.handleAdminRevokeUser)
	mux.HandleFunc("POST "+AdminPathCrossUser, b.handleAdminCrossUser)
	mux.HandleFunc("POST "+AdminPathBreak, b.handleAdminBreak)

	return mux
}

func (b *Broker) handleAdminApprove(w http.ResponseWriter, r *http.Request) {
	var req adminApprove

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	ans, err := b.Approve(req.Label, req.Fingerprint)
	if err != nil {
		b.adminResult(w, err)

		return
	}

	writeJSON(w, http.StatusOK, ans)
}

func (b *Broker) handleAdminForget(w http.ResponseWriter, r *http.Request) {
	var req adminForget

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	b.adminResult(w, b.Forget(req.Label))
}

func (b *Broker) handleAdminConfirmUpgrade(w http.ResponseWriter, _ *http.Request) {
	if err := b.store.DeleteKeptFile(); err != nil {
		writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, err.Error())

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdminAdmissions answers the admissions command, sorted by when each issuer plus sub first signed in.
type AdminAdmissions struct {
	Admissions []AdmissionRecord `json:"admissions"`
}

func (b *Broker) handleAdminAdmissions(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	ans := AdminAdmissions{Admissions: slices.Collect(maps.Values(b.store.Data().PendingAdmissions))}
	b.mu.Unlock()

	slices.SortFunc(ans.Admissions, func(x, y AdmissionRecord) int {
		if c := x.FirstSeen.Compare(y.FirstSeen); c != 0 {
			return c
		}

		return strings.Compare(x.Issuer+"\x00"+x.Sub, y.Issuer+"\x00"+y.Sub)
	})

	writeJSON(w, http.StatusOK, ans)
}

func (b *Broker) handleAdminAdmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer string `json:"issuer"`
		Sub    string `json:"sub"`
		Email  string `json:"email"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	user, err := b.AdmitUser(req.Issuer, req.Sub, req.Email, b.cfg.Now())
	if err != nil {
		b.adminResult(w, err)

		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (b *Broker) handleAdminLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer string `json:"issuer"`
		Sub    string `json:"sub"`
		UserID string `json:"user_id"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	if err := b.LinkIssuerSub(req.Issuer, req.Sub, req.UserID); err != nil {
		b.adminResult(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdminBinding is one bound label in the bindings list.
type AdminBinding struct {
	Label     string    `json:"label"`
	UserID    string    `json:"user_id"`
	UserLabel string    `json:"user_label"`
	Email     string    `json:"email"`
	Host      string    `json:"host"`
	User      string    `json:"user"`
	LastSeen  time.Time `json:"last_seen"`
	BoundAt   time.Time `json:"bound_at"`
}

// AdminBindings answers the bindings command. It lists only labels that are bound now, sorted by label.
type AdminBindings struct {
	Bindings []AdminBinding `json:"bindings"`
}

func (b *Broker) handleAdminBindings(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()

	d := b.store.Data()

	var ans AdminBindings

	for _, label := range slices.Sorted(maps.Keys(d.Bindings)) {
		user, bound := b.labelBoundUserLocked(label)
		if !bound {
			continue
		}

		row := d.Labels[label]

		ans.Bindings = append(ans.Bindings, AdminBinding{
			Label:     label,
			UserID:    user.ID,
			UserLabel: user.UserLabel,
			Email:     user.Email,
			Host:      row.Host,
			User:      row.User,
			LastSeen:  row.LastSeen,
			BoundAt:   d.Bindings[label].BoundAt,
		})
	}

	b.mu.Unlock()

	writeJSON(w, http.StatusOK, ans)
}

func (b *Broker) handleAdminRevokeUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	revoked, err := b.RevokeUser(req.UserID)
	if err != nil {
		b.adminResult(w, err)

		return
	}

	writeJSON(w, http.StatusOK, revoked)
}

func (b *Broker) handleAdminCrossUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable bool `json:"enable"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	unbound, err := b.SetCrossUserEnabled(req.Enable)
	if err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, AdminCrossUser{Enabled: req.Enable, Unbound: unbound})
}

// AdminCrossUser answers the cross-user command. Unbound lists, when the command turned cross-user
// features on, every label that can reach the broker and is bound to nobody. Each is refused from then on.
type AdminCrossUser struct {
	Enabled bool     `json:"enabled"`
	Unbound []string `json:"unbound_labels,omitempty"`
}

func (b *Broker) handleAdminBreak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeaseID string `json:"lease_id"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

		return
	}

	if req.LeaseID == "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the break names no lease ID")

		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	g := b.g.grants[req.LeaseID]
	if g == nil {
		writeErr(w, http.StatusNotFound, wire.CodeNotFound, "the broker has no such lease")

		return
	}

	over := g.State == wire.StateTaken || g.State == wire.StateEnded
	if over {
		err := b.mutate(func(s *gstate) error {
			delete(s.grants, req.LeaseID)

			return nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, err.Error())

			return
		}

		delete(b.dl, req.LeaseID)
		w.WriteHeader(http.StatusNoContent)

		return
	}

	by := "broker owner"
	if owner := b.store.Data().Users[b.store.Data().OwnerID].UserLabel; owner != "" {
		by += " " + owner
	}

	err := b.mutate(func(s *gstate) error {
		e := s.grants[req.LeaseID]
		e.State = wire.StateEnded
		e.Revision++
		e.EndedBy = by

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, err.Error())

		return
	}

	delete(b.dl, req.LeaseID)
	b.announceLocked(b.g.grants[req.LeaseID])

	w.WriteHeader(http.StatusNoContent)
}

func (b *Broker) adminResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrUnknownLabel):
		writeErr(w, http.StatusNotFound, wire.CodeBadRequest, err.Error())
	case errors.Is(err, ErrBadFingerprint), errors.Is(err, ErrAlreadyPinned), errors.Is(err, ErrOwnerUser):
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, err.Error())
	}
}

// AdminSocketPath is where the admin socket lives for a store: beside the store, in its 0700
// directory.
func AdminSocketPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), "admin.sock")
}

// peerListener refuses a connection from any OS user but the broker's own.
type peerListener struct {
	net.Listener

	uid  int
	warn func(error)
}

func (l peerListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		peer, err := peercred.Of(conn)
		if err == nil && peer.UID == l.uid {
			return conn, nil
		}

		if err == nil {
			err = fmt.Errorf("uid %d: %w", peer.UID, ErrAdminPeer)
		}

		l.warn(err)

		_ = conn.Close()
	}
}

// socketPathLimit is the longest unix socket path the platform takes, less the terminating byte.
func socketPathLimit() int {
	if runtime.GOOS == "darwin" {
		return 103
	}

	return 107
}

// checkAdminPath judges the admin socket's path before the broker listens. The directory must belong to
// the broker's user and be closed to others, a file already at the path must be a stale socket and
// nothing else, and the path must fit the platform's limit. A path that is too long would otherwise
// fail inside listen with an error that names no cause.
func checkAdminPath(path string, uid int) error {
	if limit := socketPathLimit(); len(path) > limit {
		return fmt.Errorf("the admin socket path %s is %d bytes, and a unix socket path holds at most %d on %s. "+
			"Put the store in a shorter directory", path, len(path), limit, runtime.GOOS)
	}

	dir := filepath.Dir(path)

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("checking the admin socket directory: %w", err)
	}

	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return fmt.Errorf("%s is not owned by the broker's user (uid %d)", dir, uid)
	}

	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible to others (mode %04o). Fix it with chmod 700", dir, info.Mode().Perm())
	}

	cur, err := os.Lstat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("checking %s: %w", path, err)
	case cur.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("%s exists and is not a socket, so the broker will not remove it", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing a stale admin socket: %w", err)
	}

	return nil
}

// ServeAdmin serves the admin handler on a unix socket until ctx ends. The socket's directory must be
// private to the broker's user, and each connection's peer UID must equal the broker's.
func (b *Broker) ServeAdmin(ctx context.Context, path string) error {
	if err := checkAdminPath(path, os.Getuid()); err != nil {
		return err
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listening on the admin socket %s: %w", path, err)
	}

	defer os.Remove(path)

	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()

		return fmt.Errorf("restricting the admin socket: %w", err)
	}

	warn := func(err error) { b.cfg.Logger.Warn("admin socket refused a connection", "error", err) }
	srv := &http.Server{Handler: b.AdminHandler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)

	go func() { errc <- srv.Serve(peerListener{Listener: ln, uid: os.Getuid(), warn: warn}) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serving the admin socket: %w", err)
	case <-ctx.Done():
	}

	shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = srv.Shutdown(shut)

	return nil
}
