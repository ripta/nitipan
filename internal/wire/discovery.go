package wire

import (
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/repoid"
)

// Paths for the discovery queries. A broker that does not serve one answers 404, which a node reports
// as "broker too old".
const (
	PathWhoHolds = Prefix + "/who/holds"
	PathWhoIs    = Prefix + "/who/is"
)

// Derivation states of a mirrored lease's repo entries.
const (
	// DerivedOK means Entries are the lease's repo entries. An empty list means the lease names no
	// repo, so it overlaps no repo name.
	DerivedOK = "ok"

	// DerivedFailed means the derivation was refused or failed. The lease reads as unknown.
	DerivedFailed = "failed"
)

// Derived is the repo entries of a mirrored machine lease, as the CLI or the node derived them. A
// mirror that carries none reads as unknown, never as no overlap, so a node that predates the field
// cannot make a lease look harmless.
type Derived struct {
	State string `json:"state"`

	// Derivation is the root-set derivation that produced Entries. A broker that does not know it reads
	// the lease as unknown.
	Derivation int           `json:"derivation"`
	Entries    []repoid.Name `json:"entries,omitempty"`
	Reason     string        `json:"reason,omitempty"`
}

// Lease states a discovery answer reports, beside the broker's own StateLive and StateLapsed.
const (
	// StateMirrored marks a machine lease the broker knows from its node's snapshot.
	StateMirrored = "mirrored"

	// StateStale marks a lease whose node has sent nothing for longer than one term. A mirror's Stale
	// flag stays set when its state is unknown.
	StateStale = "stale"

	// StateUnknown marks a mirror the broker cannot judge: its record is unusable, or its repo entries
	// are missing or failed.
	StateUnknown = "unknown"
)

// WhoHolds asks which holders overlap a subject. At least one of Names, Path, Resource, and Address is
// set. The broker answers with the holders any of them reaches.
type WhoHolds struct {
	Version Version `json:"version"`

	// Names are repo names. They match broker grants and every machine's mirrors by their repo entries.
	Names []repoid.Name `json:"names,omitempty"`

	// Path is a canonical path on the asking node's machine. It matches only that node's mirrors.
	Path *keys.Key `json:"path,omitempty"`

	// Resource is a resource name. It matches the mirrors of every machine.
	Resource string `json:"resource,omitempty"`

	// Address is a <handle>@<label> qualified name. It matches that holder's leases on that machine.
	Address string `json:"address,omitempty"`
}

// WhoIs asks for every holder with a name or handle.
type WhoIs struct {
	Version Version `json:"version"`
	Name    string  `json:"name"`
}

// Found is one lease that answers a discovery query.
type Found struct {
	Lease     string `json:"lease"`
	Qualified string `json:"qualified"`
	Reach     string `json:"reach"`
	State     string `json:"state"`
	Stale     bool   `json:"stale,omitempty"`

	Holder Holder `json:"holder"`
	Node   string `json:"node"`
	Host   string `json:"host,omitempty"`

	Description string `json:"description,omitempty"`
	Liveness    string `json:"liveness,omitempty"`
	Anchor      string `json:"anchor,omitempty"`

	// Names are a grant's stored root set, or a mirror's derived entries. Paths and Resource describe a
	// mirror's own keys.
	Names    []repoid.Name `json:"names,omitempty"`
	Paths    []string      `json:"paths,omitempty"`
	Resource string        `json:"resource,omitempty"`

	// Why says what the broker could not judge, for a mirror in the unknown state.
	Why string `json:"why,omitempty"`

	ExpiresMillis int64 `json:"expires_ms,omitempty"`
}

// WhoHoldsAnswer answers a WhoHolds. Notes carry what the query could not search.
type WhoHoldsAnswer struct {
	Version Version  `json:"version"`
	Found   []Found  `json:"found"`
	Notes   []string `json:"notes,omitempty"`
}

// WhoIsAnswer answers a WhoIs. Duplicate is true when more than one holder carries the name.
type WhoIsAnswer struct {
	Version   Version `json:"version"`
	Found     []Found `json:"found"`
	Holders   int     `json:"holders"`
	Duplicate bool    `json:"duplicate"`

	// Notes say what the lookup could not judge.
	Notes []string `json:"notes,omitempty"`
}

// Overlap reports a user or shared grant and a mirrored machine lease that name the same repo files.
// The broker refuses neither. The ID is stable for the pair, so a standing overlap is one report.
type Overlap struct {
	ID     string `json:"id"`
	Grant  Found  `json:"grant"`
	Mirror Found  `json:"mirror"`
}
