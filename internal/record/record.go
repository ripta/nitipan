package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
)

// Liveness kinds. A reader that does not know a kind, or finds it absent, reads the holder's
// liveness as unknown.
const (
	LivenessProcess = "process"
	LivenessSession = "session"
)

// Domain kinds and reaches this binary writes.
const (
	KindPath     = "path"
	KindResource = "resource"
	ReachMachine = "machine"
)

// ErrNewerRecord is returned when a write would rewrite a record of a higher minor version. An old
// binary would drop the fields it does not know.
var ErrNewerRecord = errors.New("record has a newer minor version than this nitipan writes")

// Machine names the host a record was written on.
type Machine struct {
	Host string `json:"host"`
	ID   string `json:"id,omitempty"`
}

// Holder identifies the holder of a lease: its agent, session, handle, and process.
type Holder struct {
	Agent     string       `json:"agent"`
	SessionID string       `json:"session_id,omitempty"`
	Handle    string       `json:"handle,omitempty"`
	Name      string       `json:"name,omitempty"`
	PID       int          `json:"pid"`
	Start     ProcessStart `json:"start"`
	User      string       `json:"user,omitempty"`
}

// Envelope is the part of a record that every version keeps readable. Records and tombstones share
// it.
type Envelope struct {
	Version       Version   `json:"version"`
	ID            string    `json:"id"`
	Footprint     Footprint `json:"footprint"`
	ClearWhenDead bool      `json:"clear_when_dead"`
	Liveness      string    `json:"liveness"`
	Machine       Machine   `json:"machine"`
	Holder        Holder    `json:"holder"`
}

// Domain is what the lease names. Kind "path" uses Checkout, Scopes, and Keys. Kind "resource" uses
// Name and Slots.
type Domain struct {
	Kind     string     `json:"kind"`
	Checkout string     `json:"checkout,omitempty"`
	Scopes   []string   `json:"scopes,omitempty"`
	Keys     []keys.Key `json:"keys,omitempty"`

	// Name is the resource name. Slots is the count the holder was admitted under. An absent count
	// means exclusive.
	Name  string `json:"name,omitempty"`
	Slots int    `json:"slots,omitempty"`
}

// Record is one lease.
type Record struct {
	Envelope

	Reach          string    `json:"reach"`
	Domain         Domain    `json:"domain"`
	ActingFor      string    `json:"acting_for"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	Description    string    `json:"description,omitempty"`
	AcquiredAt     time.Time `json:"acquired_at"`
	Absorbs        []string  `json:"absorbs,omitempty"`

	// BodyKnown is false when the domain kind is one this binary does not understand. Such a record
	// is judged by its envelope alone.
	BodyKnown bool `json:"-"`
}

// Decode reads a lease record.
//
// A record this binary cannot use comes back as an *UnusableError. A record of a higher minor
// decodes with its unknown fields ignored. A record of an unknown domain kind decodes with
// BodyKnown false.
func Decode(data []byte) (*Record, error) {
	env, raw, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}

	rec := &Record{Envelope: env}

	kind := domainKind(raw["domain"])
	if kind != KindPath && kind != KindResource {
		rec.Domain.Kind = kind

		return rec, nil
	}

	if err := json.Unmarshal(data, rec); err != nil {
		return nil, &UnusableError{Reason: ReasonUndecodable, Version: env.Version, Raw: raw, Detail: err}
	}

	rec.BodyKnown = true

	if err := rec.checkFootprint(); err != nil {
		return nil, &UnusableError{Reason: ReasonFootprintMismatch, Version: env.Version, Raw: raw, Detail: err}
	}

	return rec, nil
}

// checkFootprint verifies that the footprint equals what the body names. It resolves nothing.
func (r *Record) checkFootprint() error {
	if r.Domain.Kind == KindResource {
		if !r.Footprint.SameName(r.Domain.Name) {
			return errors.New("footprint is not exactly one name entry equal to the name in the body")
		}

		return nil
	}

	if !r.Footprint.SamePathKeys(r.Domain.Keys) {
		return errors.New("footprint does not equal the canonical keys in the body")
	}

	return nil
}

// domainKind reads the kind from a domain of any shape. A domain that is not an object has no kind,
// so the record is judged by its envelope alone.
func domainKind(domain json.RawMessage) string {
	var d struct {
		Kind string `json:"kind"`
	}

	if json.Unmarshal(domain, &d) != nil {
		return ""
	}

	return d.Kind
}

// Rewritable reports an error when this binary must not rewrite the record.
func (r *Record) Rewritable() error {
	return rewritable(r.Version, r.ID)
}

// Encode writes the record as one line of JSON. It refuses a record of a higher minor.
func (r *Record) Encode() ([]byte, error) {
	if err := r.Rewritable(); err != nil {
		return nil, err
	}

	return encode(r)
}

// Clearable reports whether any reader may clear the record once its holder is dead.
func (r *Record) Clearable() bool {
	return r.ClearWhenDead
}

func rewritable(v Version, id string) error {
	if v.Newer() || !v.ReadableBy() {
		return fmt.Errorf("record %s version %s: %w", id, v, ErrNewerRecord)
	}

	return nil
}

func encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding record: %w", err)
	}

	return append(data, '\n'), nil
}
