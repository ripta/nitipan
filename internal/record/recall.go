package record

import (
	"encoding/json"
	"errors"
	"time"
)

// Recall states. A recall is written as announcing, and becomes announced once its notice is
// published. A cancel marks it cancelling before it undoes anything, and a cancelling recall is never
// in force.
const (
	RecallAnnouncing = "announcing"
	RecallAnnounced  = "announced"
	RecallCancelling = "cancelling"
)

// Recall gives a sub-lease a deadline. Its ID is the sub-lease's lease ID, and it lives in recalls/,
// which binaries that predate recall never scan. The envelope holder is the holder of the parent
// lease, who made the recall.
//
// A recall never blocks a claim, so a writer gives it a footprint of "none". The sub-lease's own
// record does not change, so a reader of an older minor still reads the sub-lease it always did.
type Recall struct {
	Envelope

	// Parent is the ID of the lease the sub-lease was delegated from when the recall was made.
	Parent string `json:"parent"`

	RecalledAt time.Time `json:"recalled_at"`

	// Deadline is absolute and in UTC. It is wall-clock time, since it is read by other processes.
	Deadline time.Time `json:"deadline"`

	State string `json:"state"`
}

// DecodeRecall reads a recall. An unusable one comes back as an *UnusableError. A state this binary
// does not know is unusable, since a recall of an unknown state must never end a sub-lease.
func DecodeRecall(data []byte) (*Recall, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	r := &Recall{Envelope: env}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	switch {
	case r.Parent == "" || r.Deadline.IsZero():
		return nil, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: env.Version,
			Raw:     raw,
			Detail:  errors.New("recall names no parent lease or no deadline"),
		}
	case r.State != RecallAnnouncing && r.State != RecallAnnounced && r.State != RecallCancelling:
		return nil, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: env.Version,
			Raw:     raw,
			Detail:  errors.New("recall has a state this nitipan does not know"),
		}
	}

	return r, nil
}

// Rewritable reports an error when this binary must not rewrite the recall.
func (r *Recall) Rewritable() error {
	return rewritable(r.Version, r.ID)
}

// Encode writes the recall as one line of JSON. It refuses a recall of a higher minor.
func (r *Recall) Encode() ([]byte, error) {
	if err := r.Rewritable(); err != nil {
		return nil, err
	}

	return encode(r)
}
