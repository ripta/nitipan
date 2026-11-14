package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/wire"
)

var (
	// ErrUnknownUserLabel means no user has the user label a page action named.
	ErrUnknownUserLabel = errors.New("no user has that user label")

	// ErrSelfGrant means a user named themselves in a mail grant. Mail between one user's own machines
	// needs no grant.
	ErrSelfGrant = errors.New("mail between your own machines needs no grant")

	// ErrUserLabelCollision means two users derive the same user label.
	ErrUserLabelCollision = errors.New("two users derive the same user label")

	// ErrUnknownUser means no user with that ID exists.
	ErrUnknownUser = errors.New("no such user")

	// ErrUserAlreadyAdmitted means the issuer plus sub is already admitted to a user.
	ErrUserAlreadyAdmitted = errors.New("this issuer plus sub is already admitted to a user")

	// ErrNotAdmitted means no user is admitted under the issuer plus sub, or the admission was removed.
	ErrNotAdmitted = errors.New("not admitted")
)

// issuerSubKey returns the key for IssuerSubs map.
func issuerSubKey(issuer, sub string) string {
	return issuer + "\x00" + sub
}

// AdmitUser creates a user and maps an issuer plus sub to it.
func (b *Broker) AdmitUser(issuer, sub, email string, now time.Time) (UserRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := issuerSubKey(issuer, sub)

	if existingID, ok := b.store.Data().IssuerSubs[key]; ok {
		return UserRecord{}, fmt.Errorf("issuer %q sub %q already resolves to user %q: %w", issuer, sub, existingID, ErrUserAlreadyAdmitted)
	}

	userID := ulid.MustNew(ulid.Timestamp(now), rand.Reader).String()
	userLabel := DeriveUserLabel(userID)

	for _, existing := range b.store.Data().Users {
		if existing.UserLabel == userLabel {
			return UserRecord{}, fmt.Errorf("user %q and %q: %w", userID, existing.ID, ErrUserLabelCollision)
		}
	}

	user := UserRecord{
		ID:              userID,
		UserLabel:       userLabel,
		LabelDerivation: UserLabelDerivation,
		Email:           email,
		CreatedAt:       now,
	}

	err := b.commit(func(d *Data) error {
		d.Users[userID] = user
		d.IssuerSubs[key] = userID
		delete(d.PendingAdmissions, key)

		return nil
	})

	return user, err
}

// LinkIssuerSub maps an issuer plus sub to an existing user. It refuses if the issuer plus sub
// already resolves to any user.
func (b *Broker) LinkIssuerSub(issuer, sub, userID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := issuerSubKey(issuer, sub)

	if existingID, ok := b.store.Data().IssuerSubs[key]; ok {
		existing := b.store.Data().Users[existingID]

		return fmt.Errorf("issuer %q sub %q already resolves to user %s (%s): cannot link", issuer, sub, existingID, existing.Email)
	}

	if _, ok := b.store.Data().Users[userID]; !ok {
		return fmt.Errorf("user %q: %w", userID, ErrUnknownUser)
	}

	return b.commit(func(d *Data) error {
		d.IssuerSubs[key] = userID

		return nil
	})
}

// RecordPendingAdmission records an unadmitted issuer plus sub that signed in.
func (b *Broker) RecordPendingAdmission(issuer, sub, email string, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := issuerSubKey(issuer, sub)

	if _, ok := b.store.Data().IssuerSubs[key]; ok {
		return nil
	}

	return b.commit(func(d *Data) error {
		if _, ok := d.PendingAdmissions[key]; !ok {
			d.PendingAdmissions[key] = AdmissionRecord{
				Issuer:    issuer,
				Sub:       sub,
				Email:     email,
				FirstSeen: now,
			}
		}

		return nil
	})
}

// PairingCodeExpiry is how long a pairing code is valid.
const PairingCodeExpiry = 10 * time.Minute

// MaxWrongPairingAttempts is how many wrong codes lock a signer out of a label.
const MaxWrongPairingAttempts = 5

// GeneratePairingCode generates a four-word pairing code from the word list.
func GeneratePairingCode() string {
	words := make([]string, 4)

	var buf [4]byte
	for i := range words {
		if _, err := rand.Read(buf[:]); err != nil {
			panic(err)
		}

		index := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
		words[i] = handle.Words()[int(index)%handle.WordCount]
	}

	return strings.Join(words, "-")
}

// HashPairingCode returns the SHA-256 hash of a pairing code.
func HashPairingCode(code string) string {
	h := sha256.Sum256([]byte(code))

	return hex.EncodeToString(h[:])
}

// bindLocked pins a key to a label and binds the label to a user in one commit, as approval and the
// page's bind both do. It makes the key the node's key, closes a stream opened under another, and wakes
// the label and the senders of any mail the binding bounced.
func (b *Broker) bindLocked(label string, user UserRecord, hash string, now time.Time) error {
	row := b.store.Data().Labels[label]
	nk := NodeKey{MachineID: row.MachineID, UID: row.UID}
	prev := b.store.Data().Nodes[nk.String()].KeyHash

	next := b.g.clone()

	var senders []string

	err := b.commit(func(d *Data) error {
		senders = b.bindInto(d, next, label, user, hash, now)

		return next.encode(d)
	})
	if err != nil {
		return err
	}

	b.g = next

	if ns, ok := b.nodes[nk]; ok && ns.stream != nil && prev != hash {
		ns.stream.cancel()
	}

	b.wakeLabelLocked(label)

	for _, l := range senders {
		b.wakeSendersLocked(l)
	}

	return nil
}

// bindInto pins a key to a label and binds the label to a user. It runs inside a commit, and moves the
// label's leases in s, which the caller encodes into d. It returns the labels whose senders the binding
// bounced mail to. The binding consumes the label's pairing code.
//
// A label bound to a different user than it last was starts clean: every message held for it bounces,
// and every delegation to it is dropped. Forget keeps the label's last bound user, so a reissued machine
// whose label repeats is caught here.
func (b *Broker) bindInto(d *Data, s *gstate, label string, user UserRecord, hash string, now time.Time) []string {
	row := d.Labels[label]
	nk := NodeKey{MachineID: row.MachineID, UID: row.UID}

	existing := d.Bindings[label]

	previous := existing.UserID
	if previous == "" {
		previous = existing.LastBoundUser
	}

	var senders []string

	if previous != "" && previous != user.ID {
		senders = b.bounceHeldMailInto(d, label, now)

		for _, delegations := range d.Delegations {
			delete(delegations, label)
		}
	}

	d.Pins[label] = PinRecord{PinnedHash: hash}
	delete(d.PairingCodes, label)

	if rec, ok := d.Nodes[nk.String()]; ok {
		rec.KeyHash = hash
		d.Nodes[nk.String()] = rec
	}

	d.Bindings[label] = BindingRecord{
		UserID:            user.ID,
		LastBoundUser:     user.ID,
		DerivationVersion: UserLabelDerivation,
		BoundAt:           now,
	}

	moveLeasesToBoundUser(s, d, label, user.UserLabel)

	return senders
}

// moveLeasesToBoundUser sets acting_for on every lease label holds to the label's newly bound user. A
// renewal carries only the node credential, so a value the label claimed under before would otherwise
// outlive the binding. That covers an unbound label's lease, counted in the owner's scope, which already
// names a known user.
//
// A lease held for a user who still delegates to the label keeps that user, since the delegation
// authorized it. A rebind to a different user has already dropped every delegation to the label, so its
// leases all move.
func moveLeasesToBoundUser(s *gstate, d *Data, label, userLabel string) {
	delegators := map[string]bool{}

	for delegatorID, delegations := range d.Delegations {
		if _, ok := delegations[label]; !ok {
			continue
		}

		if delegator, ok := d.Users[delegatorID]; ok && delegator.UserLabel != "" {
			delegators[delegator.UserLabel] = true
		}
	}

	for _, g := range s.grants {
		if g.Node != label || g.ActingFor == userLabel || delegators[g.ActingFor] {
			continue
		}

		g.ActingFor = userLabel
	}
}

// bounceHeldMailInto bounces every message the broker holds for a label that is bound to a different
// user, and drops every bounce waiting for it, so the new user receives none of the old user's mail. It
// runs inside a commit and returns the sender labels to wake.
//
// Queued mail bounces with its body, whether or not the broker sent it, since the broker still holds the
// body. A message stored on the old machine and never read bounces without one. Forget has already
// bounced those, so this catches only one stored since.
//
// Mail one of the label's own sessions sent to another bounces back to the label. That bounce is the old
// user's too, so the last pass drops it with the rest.
func (b *Broker) bounceHeldMailInto(d *Data, label string, now time.Time) []string {
	var held []MailRecord

	for _, m := range d.Mail {
		if m.ToLabel == label && m.Kind == wire.EventMail && !m.Bounced && m.Stage != wire.StageRead {
			held = append(held, m)
		}
	}

	var senders []string

	for _, m := range held {
		b.bounceInto(d, m, wire.ReasonRebound, m.Stage == wire.StageQueued, now)

		if m.FromLabel != label {
			senders = append(senders, m.FromLabel)
		}
	}

	for id, m := range d.Mail {
		if m.ToLabel == label && m.Kind == wire.EventBounce {
			delete(d.Mail, id)
		}
	}

	return senders
}

// RevokedUser is what revoking a user removed.
type RevokedUser struct {
	// Labels are the labels bound to the user, which the revoke forgot. Sorted.
	Labels []string `json:"labels"`

	// Leases are the live or lapsed leases the revoke ended. Sorted.
	Leases []string `json:"leases"`

	// Admissions counts the issuer plus sub pairs that resolved to the user.
	Admissions int `json:"admissions"`

	// Delegations counts the user's delegations and the delegations to the user's labels.
	Delegations int `json:"delegations"`

	// MailGrants counts the user's mail grants and the grants to the user.
	MailGrants int `json:"mail_grants"`
}

// RevokeUser forgets every label bound to a user, removes the user's admissions, and drops every
// delegation and mail grant naming the user in either direction. It ends every live or lapsed lease held
// for the user, and every one the user's labels hold, the leases they hold for other users under their
// delegations included. Each holder's node writes a notice.
//
// It is one commit, so a failed store write leaves the user whole.
func (b *Broker) RevokeUser(userID string) (RevokedUser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	user, ok := b.store.Data().Users[userID]
	if !ok {
		return RevokedUser{}, fmt.Errorf("user %q: %w", userID, ErrUnknownUser)
	}

	out := RevokedUser{Labels: []string{}, Leases: []string{}}
	bound := map[string]bool{}

	for label, binding := range b.store.Data().Bindings {
		if binding.UserID == userID {
			out.Labels = append(out.Labels, label)
			bound[label] = true
		}
	}

	slices.Sort(out.Labels)

	for id, g := range b.g.grants {
		if endable(g) && (g.ActingFor == user.UserLabel || bound[g.Node]) {
			out.Leases = append(out.Leases, id)
		}
	}

	slices.Sort(out.Leases)

	now := b.cfg.Now()
	next := b.g.clone()

	err := b.commit(func(d *Data) error {
		for _, label := range out.Labels {
			b.forgetInto(d, label, now)
		}

		for issuerSub, uid := range d.IssuerSubs {
			if uid == userID {
				delete(d.IssuerSubs, issuerSub)
				out.Admissions++
			}
		}

		out.Delegations = len(d.Delegations[userID])
		delete(d.Delegations, userID)

		for delegator, delegations := range d.Delegations {
			for label := range delegations {
				if bound[label] {
					delete(delegations, label)
					out.Delegations++
				}
			}

			if len(delegations) == 0 {
				delete(d.Delegations, delegator)
			}
		}

		out.MailGrants = len(d.MailGrants[userID])
		delete(d.MailGrants, userID)

		for granter, grants := range d.MailGrants {
			if _, ok := grants[userID]; ok {
				delete(grants, userID)
				out.MailGrants++
			}

			if len(grants) == 0 {
				delete(d.MailGrants, granter)
			}
		}

		for _, id := range out.Leases {
			g := next.grants[id]
			g.State = wire.StateEnded
			g.Revision++
			g.EndedBy = "user revoked"
		}

		return next.encode(d)
	})
	if err != nil {
		return RevokedUser{}, err
	}

	b.g = next

	for _, label := range out.Labels {
		b.forgottenLocked(label)
	}

	for _, id := range out.Leases {
		delete(b.dl, id)
		b.announceLocked(b.g.grants[id])
	}

	return out, nil
}

// SetCrossUserEnabled turns cross-user features on or off. It refuses to turn them on when no OIDC
// provider is configured, the owner ID names no user record, the broker's own listen address is neither
// loopback nor served with TLS, or any label's current key reported plain HTTP or never reported its
// scheme. A forgotten label has no current key, so a retired machine never blocks it.
//
// Turning them on answers with every label that can reach the broker and is bound to nobody, sorted. Each
// is refused from then on, so the owner sees the list before it takes effect.
//
// Turning them off makes the broker serve only the owner's labels and unbound labels again. Every other
// user's binding, leases, delegations, and grants stay, so turning them on again serves those users as before.
func (b *Broker) SetCrossUserEnabled(enable bool) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	d := b.store.Data()

	if !enable {
		return nil, b.commit(func(d *Data) error {
			d.CrossUserEnabled = false

			return nil
		})
	}

	if b.cfg.OIDCConfig == nil {
		return nil, fmt.Errorf("cannot enable cross-user features: no identity provider configured. " +
			"Add an \"oidc\" block to ~/.config/nitipan/broker.json, then retry. " +
			"See \"The oidc block\" in docs/operations/broker.md")
	}

	// Opening the store assigns the owner or restores its record, so a running broker never lands here.
	if _, ok := b.ownerLocked(); !ok {
		return nil, fmt.Errorf("cannot enable cross-user features: the store's owner ID %q names no user record. "+
			"The broker restores the owner's record when it opens its store, so restart the broker, then retry", d.OwnerID)
	}

	if b.cfg.Listen != "" {
		host, _, err := net.SplitHostPort(b.cfg.Listen)
		if err != nil {
			return nil, fmt.Errorf("cannot enable cross-user features: listen address %q: %w", b.cfg.Listen, err)
		}

		if !b.cfg.TLS && wire.ClassifyHost(host) != wire.HostLoopback {
			return nil, fmt.Errorf("cannot enable cross-user features: listen address %q requires TLS or a loopback address. "+
				"Cross-user features need the broker's own listener on loopback, or serving TLS itself. "+
				"Set cert_file and key_file, or listen on 127.0.0.1 or ::1", b.cfg.Listen)
		}
	}

	labels := slices.Sorted(maps.Keys(d.Labels))

	var unbound []string

	for _, label := range labels {
		row := d.Labels[label]
		rec, reachable := d.Nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}.String()]

		if reachable {
			if _, bound := b.labelBoundUserLocked(label); !bound {
				unbound = append(unbound, label)
			}
		}

		for _, hash := range []string{rec.KeyHash, d.Pins[label].PinnedHash} {
			if hash == "" {
				continue
			}

			switch d.KeySchemes[hash] {
			case schemeHTTPS:
			case schemeHTTP:
				return nil, fmt.Errorf("cannot enable cross-user features: label %q reported plain HTTP under its current key. "+
					"Forget the label with 'nitipan broker forget %s' on the broker host, then "+
					"have the node run 'nitipan node rekey' and approve the new key. "+
					"Once every such label is forgotten, or approved again under a new key, retry", label, label)
			default:
				return nil, fmt.Errorf("cannot enable cross-user features: label %q never reported scheme. "+
					"This label is from an older node. Forget it with 'nitipan broker forget %s' on the broker host, "+
					"upgrade the node's nitipan binary, run 'nitipan node rekey', then approve the new key. "+
					"Once every label has reported its scheme, retry", label, label)
			}
		}
	}

	return unbound, b.commit(func(d *Data) error {
		d.CrossUserEnabled = true

		return nil
	})
}

// endable reports whether a revoke ends the lease. An end fact already says who took or ended it, so a
// revoke leaves it as it is.
func endable(g *grant) bool {
	return g.State == wire.StateLive || g.State == wire.StateLapsed
}

// admittedLocked resolves the user an issuer plus sub was admitted under. The page checks it inside the
// action's own lock, so a "revoke user" that lands after the sign-in refuses the action.
func (b *Broker) admittedLocked(issuer, sub string) (UserRecord, error) {
	d := b.store.Data()

	userID, ok := d.IssuerSubs[issuerSubKey(issuer, sub)]
	if !ok {
		return UserRecord{}, fmt.Errorf("issuer %q sub %q: %w", issuer, sub, ErrNotAdmitted)
	}

	user, ok := d.Users[userID]
	if !ok {
		return UserRecord{}, fmt.Errorf("user %q: %w", userID, ErrUnknownUser)
	}

	return user, nil
}

// GrantDelegation grants a delegation from the user admitted under issuer plus sub to label, and returns
// the user. The label may then claim with acting_for set to the user. It still breaks only the leases it
// holds and the leases of its own user, never the user's other leases.
func (b *Broker) GrantDelegation(issuer, sub, label string, now time.Time) (UserRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	user, err := b.admittedLocked(issuer, sub)
	if err != nil {
		return UserRecord{}, err
	}

	err = b.commit(func(d *Data) error {
		if d.Delegations[user.ID] == nil {
			d.Delegations[user.ID] = make(map[string]time.Time)
		}

		d.Delegations[user.ID][label] = now

		return nil
	})

	return user, err
}

// RevokeDelegation revokes the delegation from the user admitted under issuer plus sub to label. Every
// live or lapsed lease that label holds for the user ends, and the holder's node writes a notice. It
// returns the user.
func (b *Broker) RevokeDelegation(issuer, sub, label string) (UserRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	user, err := b.admittedLocked(issuer, sub)
	if err != nil {
		return UserRecord{}, err
	}

	var toEnd []string

	for id, g := range b.g.grants {
		if g.ActingFor == user.UserLabel && g.Node == label && endable(g) {
			toEnd = append(toEnd, id)
		}
	}

	err = b.mutate(func(s *gstate) error {
		for _, id := range toEnd {
			g := s.grants[id]
			g.State = wire.StateEnded
			g.Revision++
			g.EndedBy = "delegation revoked"
		}

		return nil
	})
	if err != nil {
		return UserRecord{}, err
	}

	for _, id := range toEnd {
		delete(b.dl, id)
		b.announceLocked(b.g.grants[id])
	}

	err = b.commit(func(d *Data) error {
		if d.Delegations[user.ID] != nil {
			delete(d.Delegations[user.ID], label)
			if len(d.Delegations[user.ID]) == 0 {
				delete(d.Delegations, user.ID)
			}
		}

		return nil
	})

	return user, err
}

// userByLabelLocked finds the user whose user label is userLabel.
func (b *Broker) userByLabelLocked(userLabel string) (UserRecord, error) {
	for _, u := range b.store.Data().Users {
		if u.UserLabel == userLabel {
			return u, nil
		}
	}

	return UserRecord{}, fmt.Errorf("%q: %w", userLabel, ErrUnknownUserLabel)
}

// GrantMail lets the user admitted under issuer plus sub receive mail from the user whose user label is
// senderLabel. It returns the granting user and the sender. Only the sign-in page calls it, after an
// identity proof, so no request through a node can grant one.
func (b *Broker) GrantMail(issuer, sub, senderLabel string, now time.Time) (granter, sender UserRecord, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	granter, err = b.admittedLocked(issuer, sub)
	if err != nil {
		return UserRecord{}, UserRecord{}, err
	}

	sender, err = b.userByLabelLocked(senderLabel)
	if err != nil {
		return UserRecord{}, UserRecord{}, err
	}

	if sender.ID == granter.ID {
		return UserRecord{}, UserRecord{}, ErrSelfGrant
	}

	err = b.commit(func(d *Data) error {
		if d.MailGrants[granter.ID] == nil {
			d.MailGrants[granter.ID] = map[string]time.Time{}
		}

		d.MailGrants[granter.ID][sender.ID] = now

		return nil
	})

	return granter, sender, err
}

// RevokeMail withdraws the mail grant from the user admitted under issuer plus sub to the user whose
// user label is senderLabel. Every later message of the conversations the grant admitted, replies
// included, bounces when the broker re-checks it before delivery.
func (b *Broker) RevokeMail(issuer, sub, senderLabel string) (granter, sender UserRecord, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	granter, err = b.admittedLocked(issuer, sub)
	if err != nil {
		return UserRecord{}, UserRecord{}, err
	}

	sender, err = b.userByLabelLocked(senderLabel)
	if err != nil {
		return UserRecord{}, UserRecord{}, err
	}

	err = b.commit(func(d *Data) error {
		delete(d.MailGrants[granter.ID], sender.ID)

		if len(d.MailGrants[granter.ID]) == 0 {
			delete(d.MailGrants, granter.ID)
		}

		return nil
	})

	return granter, sender, err
}
