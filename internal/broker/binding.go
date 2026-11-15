package broker

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"time"
)

// Errors a bind by pairing code returns.
var (
	// ErrWrongPairingCode means the label has no pairing code, the code expired, or the code differs.
	ErrWrongPairingCode = errors.New("wrong or expired pairing code. Run nitipan node pair on the machine for a fresh one")

	// ErrPairingLocked means the signer sent too many wrong codes for the label.
	ErrPairingLocked = errors.New("too many wrong codes from this user for this label. Wait for the code to expire, " +
		"then run nitipan node pair on the machine for a fresh one")

	// ErrPairedKeyGone means the key that sent the code is no longer pending under the label.
	ErrPairedKeyGone = errors.New("the key that sent this pairing code is no longer waiting under the label. " +
		"Run nitipan node pair on the machine again")
)

// labelBoundUserLocked returns the user a label is bound to. A label is bound only while its binding names
// a user and the label is pinned under its node's current key. Forget clears the user and unpins the
// label, so a key that registers again after a forget acts as nobody until it is approved or bound again.
//
// Every decision on whether a label is bound goes through here.
func (b *Broker) labelBoundUserLocked(label string) (UserRecord, bool) {
	d := b.store.Data()

	binding, ok := d.Bindings[label]
	if !ok || binding.UserID == "" || !b.pinnedLocked(label) {
		return UserRecord{}, false
	}

	user, ok := d.Users[binding.UserID]

	return user, ok
}

// scopeUserLocked returns the user whose scope a label acts in. While cross-user features are off, the
// broker runs only the owner's scope. An unbound label then acts in it, and a label bound to any other user
// acts in none. The coworker's binding, leases, and grants stay as they are, so turning the features on
// later serves the coworker again.
//
// Once the features are on, a label acts in its bound user's scope, and an unbound label acts in none.
func (b *Broker) scopeUserLocked(label string) (UserRecord, bool) {
	enabled := b.store.Data().CrossUserEnabled

	if user, ok := b.labelBoundUserLocked(label); ok {
		if !enabled && user.ID != b.store.Data().OwnerID {
			return UserRecord{}, false
		}

		return user, true
	}

	if enabled {
		return UserRecord{}, false
	}

	return b.ownerLocked()
}

// scopeRefusalLocked returns what the broker tells a label that acts in no scope, and empty for a label it
// serves. Every node-API call but register and pair goes through it, so leases, discovery, break, notices,
// and mail all refuse the same labels.
func (b *Broker) scopeRefusalLocked(label string) string {
	if _, ok := b.scopeUserLocked(label); ok {
		return ""
	}

	if b.store.Data().CrossUserEnabled {
		return unboundRefusal
	}

	if _, bound := b.labelBoundUserLocked(label); bound {
		return crossUserOffRefusal
	}

	return ""
}

// scopeUserLabel is the user label of scopeUserLocked, and empty when the label acts in no scope.
func (b *Broker) scopeUserLabel(label string) string {
	user, _ := b.scopeUserLocked(label)

	return user.UserLabel
}

// pendingKeyLocked reports whether hash is a key waiting for approval under an unpinned label. Only such a
// key may send a pairing code.
func (b *Broker) pendingKeyLocked(label, hash string) bool {
	pin := b.store.Data().Pins[label]
	if pin.PinnedHash != "" {
		return false
	}

	found := false

	for h := range pin.Pending {
		if subtle.ConstantTimeCompare([]byte(h), []byte(hash)) == 1 {
			found = true
		}
	}

	return found
}

// checkPairingCodeLocked returns the key hash a label's pairing code was sent with, when code is that code
// and it has not expired. Five wrong codes from one signer for one label lock that signer out until the
// code expires. The lockout is per signer, so it does not touch the label's pending key or other signers.
func (b *Broker) checkPairingCodeLocked(label, code, signer string, now time.Time) (string, error) {
	pc, ok := b.store.Data().PairingCodes[label]
	if !ok || now.After(pc.ExpiresAt) {
		return "", ErrWrongPairingCode
	}

	if pc.WrongAttempts[signer] >= MaxWrongPairingAttempts {
		return "", ErrPairingLocked
	}

	if subtle.ConstantTimeCompare([]byte(HashPairingCode(code)), []byte(pc.Hash)) == 1 {
		return pc.KeyHash, nil
	}

	err := b.commit(func(d *Data) error {
		pc := d.PairingCodes[label]
		if pc.WrongAttempts == nil {
			pc.WrongAttempts = map[string]int{}
		}

		pc.WrongAttempts[signer]++
		d.PairingCodes[label] = pc

		return nil
	})
	if err != nil {
		return "", err
	}

	return "", ErrWrongPairingCode
}

// BindPaired binds a label to the user admitted under issuer plus sub, by the pairing code the label's node
// printed. A label with no live code, the broker never saw included, gets ErrWrongPairingCode, so the page
// says nothing of which labels exist. It pins exactly the key that sent the code, and discards the label's
// other pending keys.
//
// The admission, the code, and the bind are judged under one lock, so a "revoke user" that lands after the
// sign-in refuses the bind. Only the sign-in page calls it, after an identity proof.
func (b *Broker) BindPaired(issuer, sub, label, code string, now time.Time) (UserRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	user, err := b.admittedLocked(issuer, sub)
	if err != nil {
		return UserRecord{}, err
	}

	hash, err := b.checkPairingCodeLocked(label, code, user.ID, now)
	if err != nil {
		return UserRecord{}, err
	}

	if b.store.Data().Pins[label].PinnedHash != "" {
		return UserRecord{}, fmt.Errorf("label %q is already approved: %w", label, ErrAlreadyPinned)
	}

	if !b.pendingKeyLocked(label, hash) {
		return UserRecord{}, ErrPairedKeyGone
	}

	return user, b.bindLocked(label, user, hash, now)
}
