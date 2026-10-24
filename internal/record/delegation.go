package record

import (
	"encoding/json"
	"errors"
	"time"
)

// Request states. A pending request has no state written. A request in the granting state was
// marked by `grant` before it published the sub-lease, so it is never granted a second time.
const (
	RequestGranting = "granting"
)

// Request asks the holder of a lease to delegate part of it. The envelope holder is the requester,
// whose exact identity a grant binds. The request's ID becomes the sub-lease's lease ID when the
// request is granted.
//
// A request never blocks a claim, so a writer gives it a footprint of "none". It lives in requests/,
// which binaries that predate sub-leases never scan.
type Request struct {
	Envelope

	// Parent is the ID of the lease the requester asks to carve a sub-lease from.
	Parent string `json:"parent"`

	// State is empty while the request is pending.
	State string `json:"state,omitempty"`

	Domain         Domain    `json:"domain"`
	ActingFor      string    `json:"acting_for"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	Description    string    `json:"description,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

// DecodeRequest reads a request. An unusable one comes back as an *UnusableError.
func DecodeRequest(data []byte) (*Request, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	r := &Request{Envelope: env}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	if r.Parent == "" || r.Domain.Kind != KindPath {
		return nil, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: env.Version,
			Raw:     raw,
			Detail:  errors.New("request has no parent lease or no path domain"),
		}
	}

	return r, nil
}

// Encode writes the request as one line of JSON. It refuses a request of a higher minor.
func (r *Request) Encode() ([]byte, error) {
	if err := rewritable(r.Version, r.ID); err != nil {
		return nil, err
	}

	return encode(r)
}

// Notice causes. A cause is part of a notice's ID, so the pair of target and cause names one notice.
const (
	CauseRequestGranted  = "request-granted"
	CauseRequestDeclined = "request-declined"
	CauseRequestLapsed   = "request-lapsed"
)

// Notice tells a holder about something that happened to a lease or request of theirs. It is
// addressed to the holder the envelope names.
//
// A notice is not a tombstone. It lives in notices/, which binaries that predate sub-leases never
// scan, because their `release` reports a tombstoned lease ID as broken. Its ID is derived from its
// target and cause, so a writer that retries after a crash finds the notice already published.
type Notice struct {
	Envelope

	// Cause says what happened. Target is the ID of the lease or request it happened to.
	Cause  string `json:"cause"`
	Target string `json:"target"`

	// Detail is the sentence the addressee reads. Paths are the keys involved.
	Detail string   `json:"detail"`
	Paths  []string `json:"paths,omitempty"`

	At time.Time `json:"at"`
}

// NoticeID derives a notice's ID, and its file name without the suffix, from its target and cause.
func NoticeID(target, cause string) string {
	return target + "-" + cause
}

// DecodeNotice reads a notice. An unusable one comes back as an *UnusableError.
func DecodeNotice(data []byte) (*Notice, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	n := &Notice{Envelope: env}
	if err := json.Unmarshal(data, n); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	return n, nil
}

// Encode writes the notice as one line of JSON. It refuses a notice of a higher minor.
func (n *Notice) Encode() ([]byte, error) {
	if err := rewritable(n.Version, n.ID); err != nil {
		return nil, err
	}

	return encode(n)
}
