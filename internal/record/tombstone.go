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

	// Paths are the canonical keys of the broken lease. They only help the broken holder recognize
	// the lease, so a reader that does not know the field loses nothing.
	Paths []string `json:"paths,omitempty"`

	// Kind says what the notice reports. Empty means a break, which is what every tombstone meant
	// before the field existed. A reader that does not know the field still delivers the notice and
	// then removes it, so the field never makes an old reader more permissive.
	Kind string `json:"kind,omitempty"`

	// Detail is the sentence a notice of a kind other than a break carries.
	Detail string `json:"detail,omitempty"`
}

// Notice kinds. A break leaves Kind empty.
const (
	// NoticeMoved reports that a session's leases moved to a new host process.
	NoticeMoved = "moved"

	// NoticeRevived reports that a session registered again, which cleared its end marks.
	NoticeRevived = "revived"
)

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
