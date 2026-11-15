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

// SchemaVersion is the version of the output schema. Minor 1 adds the domain kind "resource", the
// remote flag, and the effective count and live holders of a local resource lease. Minor 2 adds
// sub-leases: the parent link, the computed delegated list, the owner and grantor_of marks of a
// filtered query, and the top-level requests and notices arrays.
const SchemaVersion = "1.2"

// Output is the whole `list --json` document.
type Output struct {
	SchemaVersion string  `json:"schema_version"`
	Leases        []Lease `json:"leases"`

	// Tombstones are the tombstones this call delivered or reaped, plus any it found unusable and left
	// in place, marked unknown. It is always an array, empty when there were none, so a consumer never
	// has to test for absence.
	Tombstones []Tombstone `json:"tombstones"`

	// Requests are the pending sub-lease requests. A request that `grant` has marked as granting is
	// not pending, so it is left out. A filtered query lists only the requests whose keys overlap
	// the filter. It is always an array.
	Requests []Request `json:"requests"`

	// Notices are the notices this call delivered to its caller. Delivery removes a notice, so a
	// consumer that reads only the JSON would lose it if it went to stderr alone. It is always an
	// array, and empty when the call delivered none, which includes every call that read without
	// the lock.
	Notices []Notice `json:"notices"`

	// SlotConfigPath is where the slot counts are configured. The text output prints it. The JSON
	// document leaves it out, since a consumer has no use for it.
	SlotConfigPath string `json:"-"`
}

// Tombstone actions.
const (
	TombstoneDelivered = "delivered"
	TombstoneReaped    = "reaped"
	TombstoneUnknown   = "unknown"
)

// Tombstone is one tombstone list reported. Delivered means it was addressed to the caller. Reaped
// means its holder is dead. Unknown means nitipan cannot use the file.
type Tombstone struct {
	ID        string         `json:"id,omitempty"`
	File      string         `json:"file,omitempty"`
	Action    string         `json:"action"`
	Lease     string         `json:"lease,omitempty"`
	Holder    *record.Holder `json:"holder,omitempty"`
	BrokenBy  *record.Holder `json:"broken_by,omitempty"`
	Anonymous bool           `json:"anonymous,omitempty"`
	BrokenAt  *time.Time     `json:"broken_at,omitempty"`

	// Summary is the sentence `list` also writes to stderr, so a consumer that discards the array
	// can still show a person what happened.
	Summary string `json:"summary,omitempty"`
}

// Request is one pending sub-lease request.
type Request struct {
	ID string `json:"id"`

	// Parent is the lease the requester asks to share. It can name a lease that no longer exists,
	// when the call could not repair the request.
	Parent      string        `json:"parent"`
	Requester   record.Holder `json:"requester"`
	Liveness    Liveness      `json:"liveness"`
	Keys        []string      `json:"keys"`
	Description string        `json:"description,omitempty"`
	RequestedAt time.Time     `json:"requested_at"`
}

// Notice is one notice list delivered. Summary is the sentence `list` also writes to stderr.
type Notice struct {
	ID      string    `json:"id"`
	Cause   string    `json:"cause"`
	Target  string    `json:"target"`
	Paths   []string  `json:"paths,omitempty"`
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
}

// Delegation is one live sub-lease in a parent's delegated list.
type Delegation struct {
	ID   string   `json:"id"`
	Keys []string `json:"keys"`
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
	ID            string            `json:"id,omitempty"`
	File          string            `json:"file,omitempty"`
	Version       string            `json:"version,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Usable        bool              `json:"usable"`
	Liveness      Liveness          `json:"liveness"`
	Hint          string            `json:"hint,omitempty"`
	Footprint     *record.Footprint `json:"footprint,omitempty"`
	ClearWhenDead *bool             `json:"clear_when_dead,omitempty"`
	Machine       *record.Machine   `json:"machine,omitempty"`
	Holder        *record.Holder    `json:"holder,omitempty"`
	Reach         string            `json:"reach,omitempty"`
	Domain        *record.Domain    `json:"domain,omitempty"`
	ActingFor     string            `json:"acting_for,omitempty"`
	Mode          string            `json:"permission_mode,omitempty"`
	Description   string            `json:"description,omitempty"`
	AcquiredAt    *time.Time        `json:"acquired_at,omitempty"`
	Absorbs       []string          `json:"absorbs,omitempty"`
	NameShared    bool              `json:"name_shared,omitempty"`

	// Parent is the lease this one was delegated from, as stored. It stays set while the parent is
	// dead or gone, until a locked call repairs the record.
	Parent string `json:"parent,omitempty"`

	// Delegated lists the live sub-leases of this lease. It is computed at read time and never
	// stored. It is absent on a lease with none, so its presence says the lease is a parent.
	Delegated []Delegation `json:"delegated,omitempty"`

	// Owner and GrantorOf answer "who owns this path" for a filtered query, and are absent on an
	// unfiltered list, which has no path to answer for. Owner is false on a parent whose match lies
	// wholly inside its sub-leases. GrantorOf lists the live sub-leases of this lease whose keys the
	// query overlaps. It is absent when there are none.
	Owner     *bool    `json:"owner,omitempty"`
	GrantorOf []string `json:"grantor_of,omitempty"`

	// Remote is true on a resource lease written on another machine. Another machine's resource is a
	// different resource, so a consumer that counts the holders of a name skips these.
	Remote bool `json:"remote,omitempty"`

	// EffectiveSlots is the number of holders this machine admits on a local resource lease's name.
	// SlotHolders are the IDs of the live local leases on that name. Both are computed, never stored,
	// and a remote lease carries neither, since this machine's counts say nothing about another's.
	EffectiveSlots *int     `json:"effective_slots,omitempty"`
	SlotHolders    []string `json:"slot_holders,omitempty"`

	UnusableWhy string                     `json:"unusable_reason,omitempty"`
	RawFields   map[string]json.RawMessage `json:"raw_fields,omitempty"`
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
		Parent:        rec.Parent,
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

	if out.Tombstones == nil {
		out.Tombstones = []Tombstone{}
	}

	if out.Requests == nil {
		out.Requests = []Request{}
	}

	if out.Notices == nil {
		out.Notices = []Notice{}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding list output: %w", err)
	}

	return nil
}
