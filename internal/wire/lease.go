package wire

import "github.com/space-armada/nitipan/internal/repoid"

// Paths for the lease calls. A broker that does not serve one answers 404, which a node reports as
// "broker too old".
const (
	PathClaim   = Prefix + "/claim"
	PathRelease = Prefix + "/release"
	PathBreak   = Prefix + "/break"
	PathRenew   = Prefix + "/renew"
	PathList    = Prefix + "/list"

	// PathFence takes a fence token report. A broker that predates tokens answers 404.
	PathFence = Prefix + "/fence"
)

// EventLease is the event kind that carries a Lease whose state changed.
const EventLease = "lease"

// EventClaimRefused carries a ClaimRefused to the node of a lease that refused a coworker's claim the
// coworker cannot see. A node that predates it ignores the kind, as it ignores every kind it does not know.
const EventClaimRefused = "claim-refused"

// ClaimRefused names the holder's own lease and the coworker's user label. It never carries the names
// the coworker asked for, so the coworker puts no text of its choosing in front of the holder.
type ClaimRefused struct {
	Lease    string `json:"lease"`
	Coworker string `json:"coworker"`
}

// Reaches a broker grants.
const (
	ReachUser   = "user"
	ReachShared = "shared"
)

// Lease states.
const (
	StateLive   = "live"
	StateLapsed = "lapsed"

	// StateTaken marks a lapsed lease another holder's grant overlapped. StateEnded marks a lease that
	// a break ended. Both are end facts, kept until the holder's node acks them.
	StateTaken = "taken"
	StateEnded = "ended"
)

// Liveness values a node reports for a holder.
const (
	LiveAlive   = "alive"
	LiveUnknown = "unknown"
)

// Result codes of the lease calls.
const (
	CodeGranted  = "granted"
	CodeRefused  = "refused"
	CodeReleased = "released"
	CodeBroken   = "broken"
	CodeRenewed  = "renewed"
	CodeRevived  = "revived"
	CodeTaken    = "taken"
	CodeEnded    = "ended"
	CodeNotFound = "not_found"
)

// Holder names the session that holds a broker lease. Key is the digest the node computes from the
// holder's identity, which the broker compares to tell sessions of one node apart.
type Holder struct {
	Key       string `json:"key"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id,omitempty"`
	Handle    string `json:"handle,omitempty"`
	Name      string `json:"name,omitempty"`
	PID       int    `json:"pid,omitempty"`
	User      string `json:"user,omitempty"`
}

// Lease is the broker's view of one grant. Expiry is absolute, and Revision rises on every state
// change, so a node keeps the copy with the higher revision.
type Lease struct {
	ID       string `json:"id"`
	Reach    string `json:"reach"`
	State    string `json:"state"`
	Revision uint64 `json:"revision"`

	// Token orders grants for fencing. Each new grant gets max(counter+1, wall clock in microseconds), and a
	// revival keeps the token. It stays under 2^53 until about the year 2255, so JSON readers hold it exactly.
	Token uint64 `json:"token,omitempty"`

	ExpiresMillis int64 `json:"expires_ms,omitempty"`
	TermMillis    int64 `json:"term_ms,omitempty"`
	LapsedMillis  int64 `json:"lapsed_ms,omitempty"`

	Entries     []repoid.Name `json:"entries"`
	Anchor      string        `json:"anchor"`
	Holder      Holder        `json:"holder"`
	ActingFor   string        `json:"acting_for,omitempty"`
	Description string        `json:"description,omitempty"`

	// Node is the label of the node that holds the lease. Liveness is what that node last reported.
	Node     string `json:"node"`
	Liveness string `json:"liveness,omitempty"`

	// TakenBy names the holder whose grant overlapped a lapsed lease. EndedBy names who broke it.
	TakenBy string `json:"taken_by,omitempty"`
	EndedBy string `json:"ended_by,omitempty"`
}

// Claim asks for a lease. The ID is both the request ID, which is the idempotency key, and the lease ID
// on a grant.
type Claim struct {
	Version           Version       `json:"version"`
	ID                string        `json:"id"`
	Reach             string        `json:"reach"`
	Entries           []repoid.Name `json:"entries"`
	Derivation        int           `json:"derivation"`
	Anchor            string        `json:"anchor"`
	Holder            Holder        `json:"holder"`
	ActingFor         string        `json:"acting_for,omitempty"`
	ActingForExplicit bool          `json:"acting_for_explicit,omitempty"`
	Description       string        `json:"description,omitempty"`
}

// Release ends a lease its holder no longer wants, or that a node found dead or unanchored. With Name
// set and no ID, it releases the requesting holder's leases that the name overlaps.
type Release struct {
	Version   Version      `json:"version"`
	RequestID string       `json:"request_id"`
	ID        string       `json:"id,omitempty"`
	Name      *repoid.Name `json:"name,omitempty"`
	Holder    Holder       `json:"holder"`

	// Cause is "released", "anchor-ended", or "holder-dead". Only the first checks the holder.
	Cause string `json:"cause"`
}

// Release causes.
const (
	CauseReleased    = "released"
	CauseAnchorEnded = "anchor-ended"
	CauseHolderDead  = "holder-dead"
)

// Break ends another holder's lease.
type Break struct {
	Version   Version `json:"version"`
	RequestID string  `json:"request_id"`
	ID        string  `json:"id"`
	Force     bool    `json:"force"`
	By        Holder  `json:"by"`
}

// Result answers claim, release, and break. Others lists the leases that refused a claim, or that a
// release ended.
type Result struct {
	Version Version `json:"version"`
	Code    string  `json:"code"`
	Message string  `json:"message,omitempty"`
	Lease   *Lease  `json:"lease,omitempty"`
	Others  []Lease `json:"others,omitempty"`

	// Overlaps lists the mirrored machine leases a granted claim overlaps. The grant stands.
	Overlaps []Overlap `json:"overlaps,omitempty"`
}

// RenewItem names a lease to renew and the liveness of its holder.
type RenewItem struct {
	ID       string `json:"id"`
	Liveness string `json:"liveness"`
}

// Renew renews many leases in one call.
type Renew struct {
	Version Version     `json:"version"`
	Items   []RenewItem `json:"items"`
}

// RenewResult is the outcome for one lease. LapsedMillis is how long a revived lease sat lapsed.
type RenewResult struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Lease        *Lease `json:"lease,omitempty"`
	LapsedMillis int64  `json:"lapsed_ms,omitempty"`
	TakenBy      string `json:"taken_by,omitempty"`
	EndedBy      string `json:"ended_by,omitempty"`
}

// Renewed answers a Renew.
type Renewed struct {
	Version Version       `json:"version"`
	Results []RenewResult `json:"results"`
}

// ListRequest asks for every broker lease.
type ListRequest struct {
	Version Version `json:"version"`
}

// Listed answers a ListRequest.
type Listed struct {
	Version Version `json:"version"`
	Leases  []Lease `json:"leases"`
}

// SnapshotAck is what the broker answers a snapshot with: its view of the node's leases, end facts
// included. A node that sent no Acked entry for a fact receives it again.
type SnapshotAck struct {
	Version Version `json:"version"`
	Leases  []Lease `json:"leases"`

	// Overlaps lists the overlaps between grants and mirrored machine leases that touch this node's
	// leases. An older node ignores the field.
	Overlaps []Overlap `json:"overlaps,omitempty"`
}

// FenceReport tells the broker a fence token a receiver refused a push over, or that a pusher read from a
// fence ref. A restored broker may have counted fewer tokens than it once issued.
type FenceReport struct {
	Version Version `json:"version"`
	Token   uint64  `json:"token"`
}

// Fence verdicts.
const (
	// FenceKnown means the token is at or below the counter, so the broker issued it or a later one.
	FenceKnown = "known"

	// FenceRaised means the token was above the counter and within the bound, so the broker raised its counter
	// to it.
	FenceRaised = "raised"

	// FenceUnusable means the token is above the bound, so no honest counter could have issued it. The
	// reporter treats the fence ref that carried it as unusable.
	FenceUnusable = "unusable"
)

// FenceVerdict answers a FenceReport. Counter is the broker's counter after the report.
type FenceVerdict struct {
	Version Version `json:"version"`
	Verdict string  `json:"verdict"`
	Counter uint64  `json:"counter"`
}
