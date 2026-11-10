package broker

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/record"
)

// UserLabelDerivation versions the user label derivation.
const UserLabelDerivation = 1

// userLabelDomain keeps a user label from equalling a machine label derived from the same inputs.
const userLabelDomain = "nitipan-user-label-v1:"

// DeriveUserLabel returns the four-word label of a user ID.
func DeriveUserLabel(userID string) string {
	return handle.Derive(userLabelDomain+userID, "")
}

// MigrateV1ToV2 migrates a store from major 1 to major 2. It assigns an owner ID, creates the owner
// user, binds every pinned label to the owner, and rewrites acting_for on leases held by those labels.
func MigrateV1ToV2(old []byte) ([]byte, error) {
	var v1 Data

	if err := json.Unmarshal(old, &v1); err != nil {
		return nil, fmt.Errorf("decoding v1 store: %w", err)
	}

	now := time.Now()

	v2 := newData()
	v2.Version = record.Version{Major: 2, Minor: 0}
	v2.Nodes = v1.Nodes
	v2.Labels = v1.Labels
	v2.Pins = v1.Pins
	v2.Sessions = v1.Sessions
	v2.Mail = v1.Mail
	v2.Outcomes = v1.Outcomes
	v2.Counter = v1.Counter

	owner := assignOwner(v2, now)
	ownerID, ownerLabel := owner.ID, owner.UserLabel

	pinnedLabels := map[string]bool{}

	for label, pin := range v1.Pins {
		if pin.PinnedHash != "" {
			v2.Bindings[label] = BindingRecord{
				UserID:            ownerID,
				LastBoundUser:     ownerID,
				DerivationVersion: UserLabelDerivation,
				BoundAt:           now,
			}

			pinnedLabels[label] = true
		}
	}

	v2.Leases = make(map[string]json.RawMessage, len(v1.Leases))

	for key, raw := range v1.Leases {
		v2.Leases[key] = raw
	}

	if err := rewriteActingForInLeases(v2.Leases, pinnedLabels, ownerLabel); err != nil {
		return nil, err
	}

	return json.Marshal(v2)
}

// assignOwner creates the broker owner's user and records its ID in d. The ID is assigned once, when the
// store is first created or upgraded, and stays the owner's for the life of the store.
func assignOwner(d *Data, now time.Time) UserRecord {
	d.OwnerID = ulid.MustNew(ulid.Now(), rand.Reader).String()

	return restoreOwner(d, now)
}

// restoreOwner writes the user record of the owner d names. Bindings and grants point at the owner's ID,
// so a missing record comes back under that same ID, never a new one.
func restoreOwner(d *Data, now time.Time) UserRecord {
	owner := UserRecord{
		ID:              d.OwnerID,
		UserLabel:       DeriveUserLabel(d.OwnerID),
		LabelDerivation: UserLabelDerivation,
		CreatedAt:       now,
	}

	d.Users[d.OwnerID] = owner

	return owner
}

// leaseHolderNode is the part of a stored lease the migration reads. Node is the label of the node that
// holds the lease, which every stored lease carries, as wire.Lease does.
type leaseHolderNode struct {
	Node      string `json:"node"`
	ActingFor string `json:"acting_for"`
}

// rewriteActingForInLeases moves every lease a pinned label holds to the owner's user label. No user
// label exists before the upgrade but the owner's, and no delegation either, so every such lease moves. A
// binding on a running broker moves leases with moveLeasesToBoundUser instead.
//
// The rewrite edits only acting_for in each lease's JSON object, so every other field keeps its bytes.
func rewriteActingForInLeases(leases map[string]json.RawMessage, pinnedLabels map[string]bool, ownerLabel string) error {
	ownerValue, err := json.Marshal(ownerLabel)
	if err != nil {
		return fmt.Errorf("encoding the owner's user label: %w", err)
	}

	for key, raw := range leases {
		var lease leaseHolderNode
		if err := json.Unmarshal(raw, &lease); err != nil {
			return fmt.Errorf("decoding the stored lease %s: %w", key, err)
		}

		if !pinnedLabels[lease.Node] || lease.ActingFor == ownerLabel {
			continue
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("decoding the stored lease %s: %w", key, err)
		}

		fields["acting_for"] = ownerValue

		rewritten, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("encoding the stored lease %s: %w", key, err)
		}

		leases[key] = rewritten
	}

	return nil
}
