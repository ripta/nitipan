package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/wire"
)

// Errors for user operations.
var (
	// ErrUserLabelCollision means two users derive the same user label.
	ErrUserLabelCollision = errors.New("two users derive the same user label")

	// ErrUnknownUser means no user with that ID exists.
	ErrUnknownUser = errors.New("no such user")

	// ErrUserAlreadyAdmitted means the issuer plus sub is already admitted to a user.
	ErrUserAlreadyAdmitted = errors.New("this issuer plus sub is already admitted to a user")
)

// CreateUser creates a new user with a broker-assigned ID and derived user label. It checks for
// label collisions before committing.
func (b *Broker) CreateUser(email string, now time.Time) (UserRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

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

		return nil
	})

	return user, err
}

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

// StorePairingCode stores a pairing code hash for a label with an expiry.
func (b *Broker) StorePairingCode(label, hash string, expiresAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.commit(func(d *Data) error {
		d.PairingCodes[label] = PairingCodeRecord{
			Hash:      hash,
			ExpiresAt: expiresAt,
		}

		return nil
	})
}

// CheckPairingCode checks if a pairing code matches the stored hash for a label in constant time.
// It returns true if the code is correct and not expired. Five wrong codes from one signer for
// one label lock that signer out until the code expires. The lockout is per signer, so it does
// not affect the legitimate user or other signers.
func (b *Broker) CheckPairingCode(label, code, signerUser string, now time.Time) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pc, ok := b.store.Data().PairingCodes[label]
	if !ok {
		return false, nil
	}

	if now.After(pc.ExpiresAt) {
		return false, nil
	}

	if pc.WrongAttempts != nil && pc.WrongAttempts[signerUser] >= MaxWrongPairingAttempts {
		return false, fmt.Errorf("too many wrong codes from this user for this label")
	}

	hash := HashPairingCode(code)

	if subtle.ConstantTimeCompare([]byte(hash), []byte(pc.Hash)) != 1 {
		err := b.commit(func(d *Data) error {
			pc := d.PairingCodes[label]
			if pc.WrongAttempts == nil {
				pc.WrongAttempts = map[string]int{}
			}

			pc.WrongAttempts[signerUser]++
			d.PairingCodes[label] = pc

			return nil
		})

		return false, err
	}

	return true, nil
}

// ClearPairingCode removes a pairing code for a label.
func (b *Broker) ClearPairingCode(label string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.commit(func(d *Data) error {
		delete(d.PairingCodes, label)

		return nil
	})
}

// BindLabelToUser binds a label to a user by pinning its pending key and creating a binding record.
// If the label is bound to a different user than before, it bounces queued mail and drops delegations.
func (b *Broker) BindLabelToUser(label, userID string, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	labelRec, labelExists := b.store.Data().Labels[label]
	if !labelExists {
		return fmt.Errorf("label %q not found", label)
	}

	pin := b.store.Data().Pins[label]

	if pin.PinnedHash != "" {
		return fmt.Errorf("label %q is already pinned: run forget first if the machine was reinstalled", label)
	}

	if len(pin.Pending) == 0 {
		return fmt.Errorf("label %q has no pending keys", label)
	}

	var hash string
	for h := range pin.Pending {
		hash = h
		break
	}

	nk := NodeKey{MachineID: labelRec.MachineID, UID: labelRec.UID}

	existingBinding := b.store.Data().Bindings[label]
	lastBoundUser := userID
	if existingBinding.UserID != "" {
		lastBoundUser = existingBinding.LastBoundUser
	}

	userChanged := existingBinding.UserID != "" && existingBinding.UserID != userID

	if userChanged {
		if err := b.bounceMailForLabel(label); err != nil {
			return fmt.Errorf("bouncing mail for rebound label: %w", err)
		}

		b.dropDelegationsToLabel(label)
	}

	return b.commit(func(d *Data) error {
		d.Pins[label] = PinRecord{PinnedHash: hash}

		if rec, ok := d.Nodes[nk.String()]; ok {
			rec.KeyHash = hash
			d.Nodes[nk.String()] = rec
		}

		user := d.Users[userID]
		d.Bindings[label] = BindingRecord{
			UserID:            userID,
			LastBoundUser:     lastBoundUser,
			DerivationVersion: UserLabelDerivation,
			BoundAt:           now,
		}

		knownUserLabels := make(map[string]bool)
		for _, u := range d.Users {
			knownUserLabels[u.UserLabel] = true
		}

		rewriteActingForInLeases(d.Leases, label, user.UserLabel, knownUserLabels)

		return nil
	})
}

func (b *Broker) bounceMailForLabel(label string) error {
	now := time.Now()

	return b.commit(func(d *Data) error {
		for _, m := range d.Mail {
			if m.ToLabel != label || m.Bounced {
				continue
			}

			if m.Stage == "stored" && m.Kind == "mail" {
				b.bounceInto(d, m, "recipient user changed", false, now)
			}
		}

		return nil
	})
}

func (b *Broker) dropDelegationsToLabel(label string) {
	for userID, delegs := range b.store.Data().Delegations {
		if _, ok := delegs[label]; ok {
			_ = b.commit(func(d *Data) error {
				if d.Delegations[userID] != nil {
					delete(d.Delegations[userID], label)
				}

				return nil
			})
		}
	}
}

// RevokeUser forgets every label bound to a user, removes the admission, and drops grants and delegations.
func (b *Broker) RevokeUser(userID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.store.Data().Users[userID]; !ok {
		return fmt.Errorf("user %q: %w", userID, ErrUnknownUser)
	}

	var labelsToForget []string
	for label, binding := range b.store.Data().Bindings {
		if binding.UserID == userID {
			labelsToForget = append(labelsToForget, label)
		}
	}

	for _, label := range labelsToForget {
		if err := b.forgetLocked(label); err != nil {
			return err
		}
	}

	for _, label := range labelsToForget {
		b.dropDelegationsToLabel(label)
	}

	return b.commit(func(d *Data) error {
		for issuerSub, uid := range d.IssuerSubs {
			if uid == userID {
				delete(d.IssuerSubs, issuerSub)
			}
		}

		delete(d.Delegations, userID)
		delete(d.MailGrants, userID)

		for _, delegs := range d.Delegations {
			delete(delegs, userID)
		}

		for _, grants := range d.MailGrants {
			delete(grants, userID)
		}

		return nil
	})
}

// SetCrossUserEnabled turns cross-user features on or off. It refuses when: no OIDC provider is
// configured, the upgrade binding has not run (no OwnerID), the broker's own listen address is
// neither loopback nor served with TLS, any bound label reported plain HTTP, or any bound label
// never reported its scheme. It lists unbound labels before acting.
func (b *Broker) SetCrossUserEnabled(enable bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	d := b.store.Data()

	if enable {
		if b.cfg.OIDCConfig == nil {
			return fmt.Errorf("cannot enable cross-user features: no identity provider configured. " +
				"Configure an OIDC issuer in the broker config, then retry. " +
				"See the broker configuration section in README.md")
		}

		if d.OwnerID == "" {
			return fmt.Errorf("cannot enable cross-user features: the upgrade binding has not run. " +
				"Run the migration with 'nitipan broker upgrade-bindings' on the broker host, then retry")
		}

		if b.cfg.Listen != "" {
			host, _, err := net.SplitHostPort(b.cfg.Listen)
			if err != nil {
				return fmt.Errorf("cannot enable cross-user features: listen address %q: %w", b.cfg.Listen, err)
			}

			if !b.cfg.TLS && wire.ClassifyHost(host) != wire.HostLoopback {
				return fmt.Errorf("cannot enable cross-user features: listen address %q requires TLS or a loopback address. "+
					"Cross-user features need the broker's own listener on loopback, or serving TLS itself. "+
					"Set cert_file and key_file, or listen on 127.0.0.1 or ::1", b.cfg.Listen)
			}
		}

		var unboundLabels []string
		for label := range d.Labels {
			if _, bound := d.Bindings[label]; !bound {
				unboundLabels = append(unboundLabels, label)
			}
		}

		if len(unboundLabels) > 0 {
			b.cfg.Logger.Info("unbound labels found", "count", len(unboundLabels), "labels", unboundLabels)
		}

		for label, scheme := range d.SchemeReports {
			if scheme == "http" {
				return fmt.Errorf("cannot enable cross-user features: label %q reported plain HTTP. "+
					"Forget the label with 'nitipan broker forget %s' on the broker host, then "+
					"have the node run 'nitipan node rekey' and approve the new key. "+
					"Once all plain-HTTP labels are forgotten and re-approved over HTTPS, retry", label, label)
			}
		}

		for label := range d.Bindings {
			if _, reported := d.SchemeReports[label]; !reported {
				return fmt.Errorf("cannot enable cross-user features: label %q never reported scheme. "+
					"This label is from an older node. Forget it with 'nitipan broker forget %s' on the broker host, "+
					"upgrade the node's nitipan binary, then approve the label again. "+
					"Once all labels have reported their scheme, retry", label, label)
			}
		}
	}

	return b.commit(func(d *Data) error {
		d.CrossUserEnabled = enable

		return nil
	})
}
