// Package listing defines the `list --json` output, the supported way to read leases.
//
// The output has its own versioned schema, apart from the record format. A minor bump adds fields
// or new domain kinds. Consumers ignore fields they do not know, and treat a lease of a kind they do
// not know as opaque, judged by its liveness and footprint alone. A major bump breaks compatibility.
package listing

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

// SchemaVersion is the version of the output schema.
const SchemaVersion = "1.0"

// Output is the whole `list --json` document.
type Output struct {
	SchemaVersion string  `json:"schema_version"`
	Leases        []Lease `json:"leases"`
}

// Liveness is a lease's computed liveness. A consumer treats "unknown" as alive.
type Liveness struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Lease is one entry in the output.
//
// A lease of an unknown kind carries its envelope only. A record nitipan cannot use is an unknown
// entry with its file name and the raw fields that could be read.
type Lease struct {
	ID            string                     `json:"id,omitempty"`
	File          string                     `json:"file,omitempty"`
	Version       string                     `json:"version,omitempty"`
	Kind          string                     `json:"kind,omitempty"`
	Usable        bool                       `json:"usable"`
	Liveness      Liveness                   `json:"liveness"`
	Footprint     *record.Footprint          `json:"footprint,omitempty"`
	ClearWhenDead *bool                      `json:"clear_when_dead,omitempty"`
	Machine       *record.Machine            `json:"machine,omitempty"`
	Holder        *record.Holder             `json:"holder,omitempty"`
	Reach         string                     `json:"reach,omitempty"`
	Domain        *record.Domain             `json:"domain,omitempty"`
	ActingFor     string                     `json:"acting_for,omitempty"`
	Mode          string                     `json:"permission_mode,omitempty"`
	Description   string                     `json:"description,omitempty"`
	AcquiredAt    *time.Time                 `json:"acquired_at,omitempty"`
	Absorbs       []string                   `json:"absorbs,omitempty"`
	UnusableWhy   string                     `json:"unusable_reason,omitempty"`
	RawFields     map[string]json.RawMessage `json:"raw_fields,omitempty"`
}

// FromRecord builds the entry for a usable record.
//
// A record of an unknown domain kind reports its envelope: footprint, liveness, holder, and
// machine. Its body is opaque here, as it is to this binary.
func FromRecord(rec *record.Record, res liveness.Result) Lease {
	fp := rec.Footprint
	clearable := rec.ClearWhenDead
	machine := rec.Machine
	holder := rec.Holder

	l := Lease{
		ID:            rec.ID,
		Version:       rec.Version.String(),
		Kind:          rec.Domain.Kind,
		Usable:        true,
		Liveness:      Liveness{Status: res.Status.String(), Reason: res.Reason},
		Footprint:     &fp,
		ClearWhenDead: &clearable,
		Machine:       &machine,
		Holder:        &holder,
	}

	if !rec.BodyKnown {
		return l
	}

	domain := rec.Domain
	acquired := rec.AcquiredAt

	l.Reach = rec.Reach
	l.Domain = &domain
	l.ActingFor = rec.ActingFor
	l.Mode = rec.PermissionMode
	l.Description = rec.Description
	l.AcquiredAt = &acquired
	l.Absorbs = rec.Absorbs

	return l
}

// FromUnusable builds the unknown entry for a file nitipan cannot use.
func FromUnusable(file string, err *record.UnusableError) Lease {
	return Lease{
		File:        file,
		Version:     err.Version.String(),
		Usable:      false,
		Liveness:    Liveness{Status: liveness.Unknown.String(), Reason: "record cannot be used by this nitipan"},
		UnusableWhy: string(err.Reason),
		RawFields:   err.Raw,
	}
}

// Encode writes the document as indented JSON.
func Encode(w io.Writer, out Output) error {
	if out.SchemaVersion == "" {
		out.SchemaVersion = SchemaVersion
	}

	if out.Leases == nil {
		out.Leases = []Lease{}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding list output: %w", err)
	}

	return nil
}
