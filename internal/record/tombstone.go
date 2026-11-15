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

	// Paths are the canonical keys of the broken lease, or its resource name. They only help the
	// broken holder recognize the lease, so a reader that does not know the field loses nothing.
	Paths []string `json:"paths,omitempty"`

	// Kind says what the notice reports. Empty means a break, which is what every tombstone meant
	// before the field existed. A reader that does not know the field still delivers the notice and
	// then removes it, so the field never makes an old reader more permissive.
	Kind string `json:"kind,omitempty"`

	// Detail is the sentence a notice of a kind other than a break carries.
	Detail string `json:"detail,omitempty"`

	// Fenced is true on a break that handled its fence mark when it was written: it wrote the mark, or
	// found every key already covered. A tombstone without it comes from a binary that predates marks,
	// and delivery then writes the mark. A reader that does not know the field loses nothing, since it
	// delivers the tombstone and removes it as it always did.
	Fenced bool `json:"fenced,omitempty"`

	// Cause says how a break came about. Empty means an ordinary break. It is separate from Kind, which
	// already means a notice that is not a break. A reader that does not know the field delivers the
	// tombstone as a plain break, which is what a revoke is to the sub-lessee.
	Cause string `json:"cause,omitempty"`
}

// CauseRevoked is the tombstone cause of a sub-lease its parent's holder ended.
const CauseRevoked = "revoked"

// CauseRecallExpired is the tombstone cause of a sub-lease whose recall deadline passed. A reader that
// does not know it delivers the tombstone as a plain break, as it does a revoke.
const CauseRecallExpired = "recalled"

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
