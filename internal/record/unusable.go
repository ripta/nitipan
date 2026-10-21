package record

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnusable matches every UnusableError.
var ErrUnusable = errors.New("unusable record")

// Reason says why a record cannot be used.
type Reason string

// Reasons a record is unusable.
const (
	ReasonHigherMajor       Reason = "higher major version"
	ReasonUndecodable       Reason = "envelope does not decode"
	ReasonFootprintMismatch Reason = "footprint disagrees with the body"
)

// UnusableError reports a record that this binary cannot use.
//
// Raw carries the top-level fields that could be read, so `list` can show them. Version is the
// version from the envelope when it could be read.
type UnusableError struct {
	Reason  Reason
	Version Version
	Raw     map[string]json.RawMessage
	Detail  error
}

// Error implements error.
func (e *UnusableError) Error() string {
	if e.Detail != nil {
		return fmt.Sprintf("unusable record (%s, version %s): %v", e.Reason, e.Version, e.Detail)
	}

	return fmt.Sprintf("unusable record (%s, version %s)", e.Reason, e.Version)
}

// Unwrap exposes the underlying decode error.
func (e *UnusableError) Unwrap() error {
	return e.Detail
}

// Is lets errors.Is match ErrUnusable.
func (e *UnusableError) Is(target error) bool {
	return target == ErrUnusable
}

// decodeEnvelope reads the envelope shared by records and tombstones, and enforces the version
// rules. It never looks at the body.
func decodeEnvelope(data []byte) (Envelope, map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Envelope{}, nil, &UnusableError{Reason: ReasonUndecodable, Detail: err}
	}

	var head struct {
		Version Version `json:"version"`
	}

	if err := json.Unmarshal(data, &head); err != nil {
		return Envelope{}, raw, &UnusableError{Reason: ReasonUndecodable, Detail: err}
	}

	if head.Version.Major > CurrentMajor {
		return Envelope{}, raw, &UnusableError{Reason: ReasonHigherMajor, Version: head.Version, Raw: raw}
	}

	if !head.Version.ReadableBy() {
		return Envelope{}, raw, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: head.Version,
			Detail:  errors.New("version is not one this format has used"),
		}
	}

	env := Envelope{ClearWhenDead: true}
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, raw, &UnusableError{Reason: ReasonUndecodable, Version: head.Version, Raw: raw, Detail: err}
	}

	if env.ID == "" {
		return Envelope{}, raw, &UnusableError{
			Reason:  ReasonUndecodable,
			Version: head.Version,
			Raw:     raw,
			Detail:  errors.New("record has no id"),
		}
	}

	return env, raw, nil
}
