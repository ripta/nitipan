package record

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
)

// Mark guards the keys of a lease that `break --force` removed. It is addressed to the broken
// holder, whose identity the envelope carries.
//
// A tombstone is delivered and removed on the holder's next call, so the signal it gives lapses
// before the breaker can claim the keys. A mark stays until the keys are claimed by someone else,
// until a user removes it, or until the holder dies. While it stands, the local git hook refuses the
// holder's ref updates on its keys, and the holder cannot claim them again.
//
// A mark never blocks anyone else's claim, so a writer gives it a footprint of "none". It lives in
// marks/, which binaries that predate it never scan.
type Mark struct {
	Envelope

	// Lease is the ID of the lease that was broken.
	Lease string `json:"lease"`

	// Keys are the broken lease's path keys that the mark still guards. A key leaves the list when
	// someone else's live record covers it, or when a grant to the holder covers it.
	Keys []keys.Key `json:"keys"`

	// BrokenBy, Anonymous, BrokenAt, and Cause repeat the tombstone's account of how the lease
	// ended, so the refusal can quote it after the tombstone is gone.
	BrokenBy  Holder    `json:"broken_by"`
	Anonymous bool      `json:"anonymous,omitempty"`
	BrokenAt  time.Time `json:"broken_at"`
	Cause     string    `json:"cause,omitempty"`
}

// DecodeMark reads a mark. An unusable one comes back as an *UnusableError.
func DecodeMark(data []byte) (*Mark, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	m := &Mark{Envelope: env}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	if m.Lease == "" || len(m.Keys) == 0 {
		return nil, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: env.Version,
			Raw:     raw,
			Detail:  errors.New("mark names no lease or no keys"),
		}
	}

	return m, nil
}

// Rewritable reports an error when this binary must not rewrite the mark.
func (m *Mark) Rewritable() error {
	return rewritable(m.Version, m.ID)
}

// Encode writes the mark as one line of JSON. It refuses a mark of a higher minor.
func (m *Mark) Encode() ([]byte, error) {
	if err := m.Rewritable(); err != nil {
		return nil, err
	}

	return encode(m)
}

// AsTombstone rebuilds the break the mark remembers, so one sentence describes both.
func (m *Mark) AsTombstone() *Tombstone {
	return &Tombstone{
		Envelope:  m.Envelope,
		Lease:     m.Lease,
		BrokenBy:  m.BrokenBy,
		Anonymous: m.Anonymous,
		BrokenAt:  m.BrokenAt,
		Paths:     keys.Paths(m.Keys),
		Cause:     m.Cause,
	}
}
