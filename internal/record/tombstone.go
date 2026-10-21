package record

import (
	"encoding/json"
	"time"
)

// Tombstone records that a lease was broken. It is addressed to the broken holder, whose identity
// the envelope carries. It shares the lease envelope, so the same version rules apply.
//
// A tombstone never blocks a claim, so a writer gives it a footprint of "none".
type Tombstone struct {
	Envelope

	// Lease is the ID of the lease that was broken.
	Lease string `json:"lease"`

	// BrokenBy is the caller that broke it. An anonymous caller has no agent.
	BrokenBy Holder `json:"broken_by"`

	// Anonymous is true when BrokenBy is an anonymous process rather than an agent session.
	Anonymous bool `json:"anonymous,omitempty"`

	BrokenAt time.Time `json:"broken_at"`
}

// DecodeTombstone reads a tombstone. An unusable one comes back as an *UnusableError.
func DecodeTombstone(data []byte) (*Tombstone, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	t := &Tombstone{Envelope: env}
	if err := json.Unmarshal(data, t); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	return t, nil
}

// Encode writes the tombstone as one line of JSON. It refuses a tombstone of a higher minor.
func (t *Tombstone) Encode() ([]byte, error) {
	if err := rewritable(t.Version, t.ID); err != nil {
		return nil, err
	}

	return encode(t)
}
