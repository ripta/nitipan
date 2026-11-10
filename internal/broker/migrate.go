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

	ownerID := ulid.MustNew(ulid.Now(), rand.Reader).String()
	ownerLabel := DeriveUserLabel(ownerID)

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
	v2.OwnerID = ownerID

	v2.Users[ownerID] = UserRecord{
		ID:              ownerID,
		UserLabel:       ownerLabel,
		LabelDerivation: UserLabelDerivation,
		CreatedAt:       now,
	}

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

	knownUserLabels := map[string]bool{ownerLabel: true}

	for label := range pinnedLabels {
		rewriteActingForInLeases(v2.Leases, label, ownerLabel, knownUserLabels)
	}

	return json.Marshal(v2)
}

// rewriteActingForInLeases rewrites acting_for in leases held by a label when it does not resolve
// to a known user label. This is shared between migration and binding.
func rewriteActingForInLeases(leases map[string]json.RawMessage, label, newUserLabel string, knownUserLabels map[string]bool) {
	for key, raw := range leases {
		var lease struct {
			ActingFor string `json:"acting_for"`
		}

		if err := json.Unmarshal(raw, &lease); err != nil {
			continue
		}

		var full map[string]interface{}
		if err := json.Unmarshal(raw, &full); err != nil {
			continue
		}

		holder, ok := full["holder"].(map[string]interface{})
		if !ok {
			continue
		}

		leaseLabel, ok := holder["label"].(string)
		if !ok || leaseLabel != label {
			continue
		}

		if lease.ActingFor == "" || lease.ActingFor == newUserLabel || knownUserLabels[lease.ActingFor] {
			continue
		}

		full["acting_for"] = newUserLabel

		rewritten, err := json.Marshal(full)
		if err != nil {
			continue
		}

		leases[key] = rewritten
	}
}
