package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// DefaultHandoffWait is how long a command waits for the node's reply.
const DefaultHandoffWait = 30 * time.Second

// Errors a wider-reach command reports.
var (
	// ErrOutcomeUnknown means the node claimed the request and did not answer in time.
	ErrOutcomeUnknown = errors.New("the node has the request, and its outcome is not known yet")

	// ErrBrokerTooOld means the broker does not serve the call.
	ErrBrokerTooOld = errors.New("broker too old")

	// ErrBrokerUnreachable means the node could not get an answer from the broker. It is not a refusal.
	ErrBrokerUnreachable = errors.New("the broker did not answer")
)

// brokerCallError is a broker call that failed on the broker's side or on the way there.
type brokerCallError struct {
	kind    string
	message string
}

// Error implements error.
func (e *brokerCallError) Error() string {
	return fmt.Sprintf("the broker call for %s failed: %s", e.kind, e.message)
}

// Is matches ErrBrokerUnreachable.
func (e *brokerCallError) Is(target error) bool { return target == ErrBrokerUnreachable }

func (a *app) handoffWait() time.Duration {
	if a.deps.HandoffWait > 0 {
		return a.deps.HandoffWait
	}

	return DefaultHandoffWait
}

func (a *app) deriver(stateDir string) *rootset.Deriver {
	if a.deps.Roots != nil {
		return a.deps.Roots
	}

	return &rootset.Deriver{CachePath: filepath.Join(stateDir, handoff.RootsCache)}
}

// holderEnv builds the envelope that identifies the calling holder to the node and the broker.
func holderEnv(h adapter.Holder, local record.Machine) record.Envelope {
	return record.Envelope{Liveness: h.Adapter.Liveness, Machine: local, Holder: h.Record()}
}

func (a *app) requester(local record.Machine) handoff.Requester {
	self := a.deps.Procs.Self()
	start, _ := a.deps.Procs.Start(self)

	return handoff.Requester{PID: self, Start: start, Machine: local}
}

// nodeFor requires a live node that knows the request kind.
func (a *app) nodeFor(kind string) error {
	p, err := a.requireNode()
	if err != nil {
		return err
	}

	version := handoff.KindSince(kind)
	if version == 0 {
		version = handoff.RequestVersion
	}

	if err := p.Supports(kind, version); err != nil {
		return fmt.Errorf("%w. Upgrade the node's nitipan: it restarts on its own when its executable changes", err)
	}

	return nil
}

// nodeAt is nodeFor at an explicit request version, or nodeFor itself for a version of 0.
func (a *app) nodeAt(kind string, version int) error {
	if version == 0 {
		return a.nodeFor(kind)
	}

	return a.nodeForVersion(kind, version)
}

// nodeForVersion requires a live node that knows the request kind at the given version.
func (a *app) nodeForVersion(kind string, version int) error {
	p, err := a.requireNode()
	if err != nil {
		return err
	}

	if err := p.Supports(kind, version); err != nil {
		return fmt.Errorf("%w. Upgrade the node's nitipan: it restarts on its own when its executable changes", err)
	}

	return nil
}

// roundTrip hands a request to the node and waits for the reply without holding the state lock. A
// request the node claimed and did not answer leaves an in-flight record, so a retry of the same
// command reuses the request ID and the broker returns the same answer.
func (a *app) roundTrip(stateDir, kind, id, inflight string, body any) (handoff.Reply, error) {
	return a.roundTripAt(stateDir, kind, id, inflight, body, 0)
}

// roundTripAt is roundTrip at an explicit request version. A version of 0 writes the lowest version that
// carries the kind. A request whose meaning a newer version changes rides that version, as a claim for
// another user rides handoff.ActingForSince, so an older node answers it "node too old" instead of
// forwarding it under the old meaning.
func (a *app) roundTripAt(stateDir, kind, id, inflight string, body any, version int) (handoff.Reply, error) {
	local, err := a.deps.Machine()
	if err != nil {
		return handoff.Reply{}, fmt.Errorf("identifying this machine: %w", err)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return handoff.Reply{}, fmt.Errorf("encoding the %s request: %w", kind, err)
	}

	req := handoff.Request{ID: id, Kind: kind, Requester: a.requester(local), CreatedAt: a.deps.Now().UTC(), Body: raw}

	if version != 0 {
		err = handoff.SubmitVersion(stateDir, req, version)
	} else {
		err = handoff.Submit(stateDir, req)
	}

	if err != nil {
		return handoff.Reply{}, fmt.Errorf("handing the %s request to the node: %w", kind, err)
	}

	deadline := time.Now().Add(a.handoffWait())

	for {
		rep, err := handoff.ReadReply(stateDir, id)
		if err == nil {
			handoff.Consume(stateDir, id)

			if inflight != "" {
				handoff.ClearInflight(stateDir, inflight)
			}

			return rep, replyErr(kind, rep)
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return handoff.Reply{}, err
		}

		if time.Now().After(deadline) {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if handoff.Withdraw(stateDir, id) {
		if inflight != "" {
			handoff.ClearInflight(stateDir, inflight)
		}

		return handoff.Reply{}, fmt.Errorf("the node did not pick up the %s request within %v, so it was withdrawn. Is the node stuck?",
			kind, a.handoffWait())
	}

	if inflight != "" {
		_ = handoff.NoteInflight(stateDir, inflight, id, a.deps.Now().UTC())
	}

	return handoff.Reply{}, fmt.Errorf("%w: the %s request %s may still finish. Its outcome arrives as a notice on your next nitipan "+
		"call, and running the same command again returns the same answer", ErrOutcomeUnknown, kind, id)
}

func replyErr(kind string, rep handoff.Reply) error {
	if rep.Error == nil {
		return nil
	}

	switch rep.Error.Code {
	case handoff.CodeTooOld:
		return fmt.Errorf("%s", rep.Error.Message)
	case handoff.CodeBrokerTooOld:
		hint := "Upgrade the broker, or use machine reach"
		if strings.HasPrefix(kind, "mail") {
			hint = "Upgrade the broker, since only a broker that serves mail can carry it"
		}

		return fmt.Errorf("%w: the broker does not serve the %s call. %s (%s)", ErrBrokerTooOld, kind, hint, rep.Error.Message)
	case handoff.CodeExpired:
		return fmt.Errorf("%w. Run the command again", handoff.ErrRequestExpired)
	}

	if rep.Error.Code == handoff.CodeBroker {
		return &brokerCallError{kind: kind, message: rep.Error.Message}
	}

	return fmt.Errorf("the broker call for %s failed: %s", kind, rep.Error.Message)
}

// wideReach reads --reach. It returns the empty string for machine reach.
func wideReach(cmd *cobra.Command) (string, error) {
	reach, err := cmd.Flags().GetString("reach")
	if err != nil {
		return "", fmt.Errorf("reading --reach: %w", err)
	}

	switch reach {
	case "", record.ReachMachine:
		return "", nil
	case wire.ReachUser, wire.ReachShared:
		return reach, nil
	}

	return "", fmt.Errorf("%w: --reach is machine, user, or shared, got %q", exitcode.ErrUsage, reach)
}

// claimKey is what makes two claim commands the same command, for the in-flight record.
type claimKey struct {
	Reach   string
	Entries []repoid.Name
	Anchor  string
}

func (a *app) runAcquireWide(cmd *cobra.Command, h adapter.Holder, reach string) error {
	if cmd.Flags().Changed("resource") || cmd.Flags().Changed("replace") || cmd.Flags().Changed("wait") || cmd.Flags().Changed("slots") {
		return fmt.Errorf("%w: --reach %s claims a checkout. It does not combine with --resource, --replace, --wait, or --slots",
			exitcode.ErrUsage, reach)
	}

	// A claim for another user rides the request version that marks acting_for explicit.
	version := 0
	if af := optString(cmd, "acting-for"); af != nil && *af != h.User {
		version = handoff.ActingForSince
	}

	if err := a.nodeAt(handoff.KindClaim, version); err != nil {
		return err
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	domain, err := a.domain(cmd)
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	canon, err := e.Canon.Domain(domain.Checkout, domain.Scopes)
	if err != nil {
		return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	d := a.deriver(stateDir)

	var results []rootset.Result

	for _, k := range canon.Keys {
		r, err := d.Derive(k)
		if err != nil {
			return fmt.Errorf("%w: a %s claim names a repo, and %w", exitcode.ErrRefused, reach, err)
		}

		results = append(results, r)
	}

	entries := rootset.Union(results...)

	anchor, created, err := a.takeAnchor(cmd, e, h, domain)
	if err != nil {
		return err
	}

	local, _ := a.deps.Machine()
	env := anchor.Envelope
	env.Machine = local

	actingFor := h.User
	if af := optString(cmd, "acting-for"); af != nil {
		actingFor = *af
	}

	body := handoff.ClaimBody{
		Reach: reach, Entries: entries, Derivation: rootset.Version, Holder: env, ActingFor: actingFor,
		Anchor: handoff.Anchor{ID: anchor.ID, Keys: anchor.Domain.Keys, Covered: canon.Keys, Created: created},
	}

	if desc := optString(cmd, "description"); desc != nil {
		body.Description = *desc
	}

	flight := handoff.InflightKey(handoff.HolderKey(env.Holder, env.Liveness), handoff.KindClaim,
		claimKey{Reach: reach, Entries: entries, Anchor: anchor.ID})

	id, ok := handoff.LookupInflight(stateDir, flight, a.deps.Now())
	if !ok {
		id = e.NewID()
	}

	rep, err := a.roundTripAt(stateDir, handoff.KindClaim, id, flight, body, version)
	if err != nil {
		// Only an unknown outcome keeps the anchor, since the claim may still be granted.
		if !errors.Is(err, ErrOutcomeUnknown) {
			a.releaseTakenAnchor(e, h, anchor, created)
		}

		return err
	}

	var res wire.Result
	if err := json.Unmarshal(rep.Body, &res); err != nil {
		return fmt.Errorf("decoding the claim result: %w", err)
	}

	if res.Code != wire.CodeGranted {
		a.releaseTakenAnchor(e, h, anchor, created)

		return fmt.Errorf("%w: %s", exitcode.ErrRefused, res.Message)
	}

	text := ""
	if len(results) > 0 {
		text = results[0].Text
	}

	a.saveToken(stateDir, res.Lease.ID, res.Lease.Token)
	a.printGranted(res.Lease, entries, text, anchor.ID, created)
	a.printOverlaps(res.Overlaps)

	return nil
}

// saveToken stores the token a broker reply carried in the lease's sidecar. The CLI does this from the
// reply it read, so a node that predates the field cannot leave the sidecar without it. It never creates
// a sidecar.
func (a *app) saveToken(stateDir, id string, token uint64) {
	if token == 0 {
		return
	}

	sc, err := handoff.ReadSidecar(stateDir, id)
	if err != nil || sc.Token == token {
		return
	}

	dir, err := state.Open(stateDir, a.deps.StateOptions)
	if err != nil {
		a.warn(err)

		return
	}

	l, err := dir.Lock()
	if err != nil {
		a.warn(err)

		return
	}

	defer func() { _ = l.Unlock() }()

	sc, err = handoff.ReadSidecar(stateDir, id)
	if err != nil {
		return
	}

	sc.Token = token

	if err := handoff.WriteSidecar(l, sc); err != nil {
		a.warn(err)
	}
}

// reportFence tells the broker a fence token seen on a remote, through the node. The broker raises its
// counter to a token within its bound and refuses one above it. See wire.FenceReport.
func (a *app) reportFence(token uint64) (wire.FenceVerdict, error) {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return wire.FenceVerdict{}, fmt.Errorf("resolving the state directory: %w", err)
	}

	if err := a.nodeFor(handoff.KindFence); err != nil {
		return wire.FenceVerdict{}, err
	}

	rep, err := a.roundTrip(stateDir, handoff.KindFence, state.NewID(), "", handoff.FenceBody{Token: token})
	if err != nil {
		return wire.FenceVerdict{}, err
	}

	var out wire.FenceVerdict
	if err := json.Unmarshal(rep.Body, &out); err != nil {
		return wire.FenceVerdict{}, fmt.Errorf("decoding the fence verdict: %w", err)
	}

	return out, nil
}

// takeAnchor takes, or reuses, the machine lease the wider-reach lease hangs on. "Already covered"
// counts as success, and the covering lease becomes the anchor.
func (a *app) takeAnchor(cmd *cobra.Command, e *lease.Engine, h adapter.Holder, domain lease.Domain) (*record.Record, bool, error) {
	req := lease.AcquireRequest{
		Holder: h, Domain: domain, Name: optString(cmd, "name"), Mode: optString(cmd, "permission-mode"),
		Description: optString(cmd, "description"),
	}

	res, err := e.Acquire(req)

	var covered *lease.CoveredError

	switch {
	case err == nil:
		return res.Lease, res.Outcome == lease.Created, nil
	case errors.As(err, &covered):
		if len(covered.Leases) != 1 {
			return nil, false, fmt.Errorf("%w: several of your leases cover this claim together, and a wider-reach lease hangs on one. "+
				"Release them and claim again, or claim a path one of them covers", exitcode.ErrRefused)
		}

		a.clearCreated(covered.Leases[0].ID)

		return covered.Leases[0], false, nil
	}

	return nil, false, err
}

// clearCreated records that a later call found the anchor already covering a claim, so releasing the
// wider-reach lease no longer releases the anchor.
func (a *app) clearCreated(anchorID string) {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return
	}

	scs, _, err := handoff.Sidecars(stateDir)
	if err != nil {
		return
	}

	for _, sc := range scs {
		if sc.Anchor.ID != anchorID || !sc.Anchor.Created {
			continue
		}

		sc.Anchor.Created = false

		dir, err := state.Open(stateDir, a.deps.StateOptions)
		if err != nil {
			return
		}

		l, err := dir.Lock()
		if err != nil {
			a.warn(err)

			return
		}

		if err := handoff.WriteSidecar(l, sc); err != nil {
			a.warn(err)
		}

		_ = l.Unlock()
	}
}

// releaseTakenAnchor releases an anchor the refused claim took new.
func (a *app) releaseTakenAnchor(e *lease.Engine, h adapter.Holder, anchor *record.Record, created bool) {
	if !created {
		return
	}

	if _, err := e.Release(lease.ReleaseRequest{Holder: h, IDs: []string{anchor.ID}}); err != nil {
		a.warn(fmt.Errorf("releasing the anchor %s the refused claim took: %w", anchor.ID, err))
	}
}

func (a *app) printGranted(l *wire.Lease, entries []repoid.Name, text, anchor string, created bool) {
	out := a.deps.Stdout

	fmt.Fprintf(out, "acquired %s lease %s\n", l.Reach, l.ID)

	for i, n := range entries {
		if i == 0 && text != "" {
			fmt.Fprintf(out, "  name:   %s\n", n.Text(text))

			continue
		}

		fmt.Fprintf(out, "  name:   %s\n", n)
	}

	how := "reused"
	if created {
		how = "taken new"
	}

	fmt.Fprintf(out, "  anchor: machine lease %s (%s)\n", anchor, how)

	if l.Token != 0 {
		fmt.Fprintf(out, "  token:  %d\n", l.Token)
	}

	fmt.Fprintf(out, "  expires %s, renewed by the node every %v\n", time.UnixMilli(l.ExpiresMillis).Local().Format(time.RFC3339),
		time.Duration(l.TermMillis)*time.Millisecond/3)
}

// mySidecars returns the sidecars of the holder with this key.
func (a *app) mySidecars(stateDir string, key string) ([]handoff.Sidecar, error) {
	scs, _, err := handoff.Sidecars(stateDir)
	if err != nil {
		return nil, err
	}

	var out []handoff.Sidecar

	for _, sc := range scs {
		if handoff.HolderKey(sc.Holder.Holder, sc.Holder.Liveness) == key {
			out = append(out, sc)
		}
	}

	return out, nil
}

// reportRisk says when this holder's broker leases are at risk or lapsed, from the node's deadline file.
func (a *app) reportRisk(h adapter.Holder) {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return
	}

	d, err := handoff.ReadDeadlines(stateDir, handoff.HolderKey(h.Record(), h.Adapter.Liveness))
	if err != nil {
		return
	}

	for _, line := range d.Risk(a.deps.Now()) {
		fmt.Fprintf(a.deps.Stderr, "nitipan: warning: %s\n", line)
	}
}

func (a *app) readLocal(stateDir, id string) *record.Record {
	dir, err := state.OpenRead(stateDir, a.deps.StateOptions)
	if err != nil {
		return nil
	}

	data, err := dir.Read(state.LeasesDir, id)
	if err != nil {
		return nil
	}

	rec, err := record.Decode(data)
	if err != nil {
		return nil
	}

	return rec
}

func sameKeys(a, b []keys.Key) bool {
	if len(a) != len(b) {
		return false
	}

	for _, k := range a {
		found := false

		for _, o := range b {
			if keys.Equal(k, o) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// releaseAnchors decides, for each released wider-reach lease, whether its anchor goes too. The anchor
// goes only when the claim created it, its keys are unchanged, it absorbed nothing, and no other
// wider-reach lease names it.
func (a *app) releaseAnchors(e *lease.Engine, h adapter.Holder, stateDir string, removed []handoff.Sidecar) {
	all, _, _ := handoff.Sidecars(stateDir)

	for _, sc := range removed {
		rec := a.readLocal(stateDir, sc.Anchor.ID)
		if rec == nil {
			continue
		}

		reason := ""

		switch {
		case !sc.Anchor.Created:
			reason = "it was already held, or another call found it covering a claim"
		case !sameKeys(rec.Domain.Keys, sc.Anchor.Keys):
			reason = "its keys changed since the claim"
		case len(rec.Absorbs) > 0:
			reason = "it merged other leases"
		default:
			for _, other := range all {
				if other.ID != sc.ID && other.Anchor.ID == sc.Anchor.ID {
					reason = "lease " + other.ID + " is anchored to it too"
				}
			}
		}

		if reason != "" {
			fmt.Fprintf(a.deps.Stdout, "anchor %s stays as an ordinary machine lease: %s\n", sc.Anchor.ID, reason)

			continue
		}

		if _, err := e.Release(lease.ReleaseRequest{Holder: h, IDs: []string{sc.Anchor.ID}}); err != nil {
			a.warn(fmt.Errorf("releasing the anchor %s: %w", sc.Anchor.ID, err))

			continue
		}

		fmt.Fprintf(a.deps.Stdout, "released anchor %s\n", sc.Anchor.ID)
	}
}

// brokerTargets splits release arguments into local lease IDs, broker lease IDs, and typed names.
//
// An ID with no record on this machine may name a broker lease another of the user's machines holds,
// such as one a shared agent holds for them. It goes to the broker while a node runs here, and the
// broker decides whether it is the user's to release.
func (a *app) brokerTargets(stateDir string, env record.Envelope, ids []string) (local, remote []string, names []repoid.Name, err error) {
	nodeRuns := sync.OnceValue(func() bool {
		_, err := a.requireNode()

		return err == nil
	})
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, repoid.Prefix):
			n, perr := repoid.Parse(id)
			if perr != nil {
				return nil, nil, nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, perr)
			}

			names = append(names, n)
		default:
			key := handoff.InflightKey(handoff.HolderKey(env.Holder, env.Liveness), handoff.KindRelease, handoff.ReleaseBody{ID: id, Holder: env})

			_, rerr := handoff.ReadSidecar(stateDir, id)

			switch {
			case rerr == nil, len(handoff.InflightSidecars(stateDir, key, a.deps.Now())) > 0:
				remote = append(remote, id)
			case !isLocalID(stateDir, id) && nodeRuns():
				remote = append(remote, id)
			default:
				local = append(local, id)
			}
		}
	}

	return local, remote, names, nil
}

// releaseBroker releases wider-reach leases through the node and returns the sidecars it removed.
func (a *app) releaseBroker(stateDir string, env record.Envelope, ids []string, names []repoid.Name) ([]handoff.Sidecar, error) {
	if err := a.nodeFor(handoff.KindRelease); err != nil {
		return nil, err
	}

	var (
		removed []handoff.Sidecar
		errs    []error
	)

	targets := make([]handoff.ReleaseBody, 0, len(ids)+len(names))
	for _, id := range ids {
		targets = append(targets, handoff.ReleaseBody{ID: id, Holder: env})
	}

	for i := range names {
		targets = append(targets, handoff.ReleaseBody{Name: &names[i], Holder: env})
	}

	for _, body := range targets {
		key := handoff.InflightKey(handoff.HolderKey(env.Holder, env.Liveness), handoff.KindRelease, body)

		id, ok := handoff.LookupInflight(stateDir, key, a.deps.Now())
		if !ok {
			id = state.NewID()
		}

		pending := handoff.InflightSidecars(stateDir, key, a.deps.Now())

		if sc, err := handoff.ReadSidecar(stateDir, body.ID); body.ID != "" && err == nil {
			pending = []handoff.Sidecar{sc}

			if err := handoff.NoteInflightWith(stateDir, key, id, a.deps.Now().UTC(), pending); err != nil {
				errs = append(errs, err)

				continue
			}
		}

		rep, err := a.roundTrip(stateDir, handoff.KindRelease, id, key, body)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		var out handoff.ReleaseReply

		if err := json.Unmarshal(rep.Body, &out); err != nil {
			errs = append(errs, fmt.Errorf("decoding the release result: %w", err))

			continue
		}

		if out.Result.Code != wire.CodeReleased {
			errs = append(errs, fmt.Errorf("%w: %s", exitcode.ErrNoSuchLease, out.Result.Message))

			continue
		}

		fmt.Fprintf(a.deps.Stdout, "released %s (%s lease)\n", out.Result.Lease.ID, out.Result.Lease.Reach)

		for _, o := range out.Result.Others {
			fmt.Fprintf(a.deps.Stdout, "released %s (%s lease)\n", o.ID, o.Reach)
		}

		if len(out.Sidecars) == 0 {
			out.Sidecars = pending
		}

		removed = append(removed, out.Sidecars...)
	}

	return removed, errors.Join(errs...)
}

// runBreakBroker breaks leases that are not in this machine's state directory.
func (a *app) runBreakBroker(stateDir string, ids []string, force bool) error {
	if err := a.nodeFor(handoff.KindBreak); err != nil {
		return err
	}

	local, _ := a.deps.Machine()
	user, _ := a.deps.User()
	by := record.Holder{Agent: "nitipan break", Name: fmt.Sprintf("%s on %s", user, local.Host)}

	var errs []error

	for _, target := range ids {
		body := handoff.BreakBody{ID: target, Force: force, By: by}
		key := handoff.InflightKey("break", handoff.KindBreak, body)

		id, ok := handoff.LookupInflight(stateDir, key, a.deps.Now())
		if !ok {
			id = state.NewID()
		}

		rep, err := a.roundTrip(stateDir, handoff.KindBreak, id, key, body)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		var res wire.Result
		if err := json.Unmarshal(rep.Body, &res); err != nil {
			errs = append(errs, fmt.Errorf("decoding the break result: %w", err))

			continue
		}

		switch res.Code {
		case wire.CodeBroken:
			fmt.Fprintf(a.deps.Stdout, "broken broker lease %s; its holder's node writes it a notice that names you. "+
				"The anchor stays as an ordinary machine lease\n", target)
		case wire.CodeNotFound:
			errs = append(errs, fmt.Errorf("%w: %s", exitcode.ErrNoSuchLease, res.Message))
		default:
			errs = append(errs, fmt.Errorf("%w: %s", exitcode.ErrRefused, res.Message))
		}
	}

	return errors.Join(errs...)
}

// isLocalID reports whether the ID names a file in this machine's lease, tombstone, or mark directory.
func isLocalID(stateDir, id string) bool {
	for _, sub := range []string{state.LeasesDir, state.TombstonesDir, state.MarksDir} {
		names, _ := state.ListNames(stateDir, sub)
		for _, n := range names {
			if strings.HasPrefix(n, id) {
				return true
			}
		}
	}

	return false
}

func (a *app) renewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "renew [lease-id ...]",
		Short: "Renew your broker leases now and print their new expiry",
		Long: `Renew broker leases through the node at once. With no lease ID, renew every broker lease you hold.

The node already renews them on its own, so a session never needs this. It is for a check that wants a
fresh expiry before an action that must not outlive the lease.`,
		RunE: func(cmd *cobra.Command, ids []string) error {
			return a.wrapErr(a.runRenew(ids))
		},
	}
}

func (a *app) runRenew(ids []string) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	local, err := a.deps.Machine()
	if err != nil {
		return fmt.Errorf("identifying this machine: %w", err)
	}

	env := holderEnv(h, local)

	if len(ids) == 0 {
		mine, err := a.mySidecars(stateDir, handoff.HolderKey(env.Holder, env.Liveness))
		if err != nil {
			return err
		}

		for _, sc := range mine {
			ids = append(ids, sc.ID)
		}
	}

	if len(ids) == 0 {
		fmt.Fprintln(a.deps.Stdout, "no broker leases to renew")

		return nil
	}

	out, err := a.renewNow(stateDir, env, ids)
	if err != nil {
		return err
	}

	var errs []error

	for _, r := range out.Results {
		switch r.Code {
		case wire.CodeRenewed, wire.CodeRevived:
			a.saveToken(stateDir, r.ID, r.Lease.Token)

			fmt.Fprintf(a.deps.Stdout, "renewed %s; expires %s%s\n", r.ID, time.UnixMilli(r.Lease.ExpiresMillis).Local().Format(time.RFC3339),
				tokenText(r.Lease.Token))
		case wire.CodeTaken:
			errs = append(errs, fmt.Errorf("%w: lease %s was lost to %s", exitcode.ErrRefused, r.ID, r.TakenBy))
		case wire.CodeEnded:
			errs = append(errs, fmt.Errorf("%w: lease %s ended%s", exitcode.ErrRefused, r.ID, endedBy(r.EndedBy)))
		default:
			errs = append(errs, fmt.Errorf("%w: lease %s: the broker has no record of it", exitcode.ErrNoSuchLease, r.ID))
		}
	}

	return errors.Join(errs...)
}

// renewNow renews broker leases through the node and waits for the broker's answer.
func (a *app) renewNow(stateDir string, env record.Envelope, ids []string) (wire.Renewed, error) {
	if err := a.nodeFor(handoff.KindRenew); err != nil {
		return wire.Renewed{}, err
	}

	rep, err := a.roundTrip(stateDir, handoff.KindRenew, state.NewID(), "", handoff.RenewBody{IDs: ids, Holder: env})
	if err != nil {
		return wire.Renewed{}, err
	}

	var out wire.Renewed
	if err := json.Unmarshal(rep.Body, &out); err != nil {
		return wire.Renewed{}, fmt.Errorf("decoding the renew result: %w", err)
	}

	return out, nil
}

func tokenText(token uint64) string {
	if token == 0 {
		return ""
	}

	return fmt.Sprintf("; token %d", token)
}

func endedBy(by string) string {
	if by == "" {
		return ""
	}

	return " (broken by " + by + ")"
}

// brokerLeases gathers the broker leases list prints. A node that is not running leaves this holder's
// own leases on file shown with an unknown state.
func (a *app) brokerLeases(h *adapter.Holder) []listing.BrokerLease {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return nil
	}

	key := ""
	if h != nil {
		key = handoff.HolderKey(h.Record(), h.Adapter.Liveness)
	}

	var risk handoff.Deadlines

	if key != "" {
		risk, _ = handoff.ReadDeadlines(stateDir, key)
	}

	riskOf := func(id string) (string, bool) {
		for _, line := range risk.Risk(a.deps.Now()) {
			if strings.Contains(line, " "+id+" ") {
				return line, true
			}
		}

		return "", false
	}

	if a.nodeFor(handoff.KindList) == nil {
		rep, err := a.roundTrip(stateDir, handoff.KindList, state.NewID(), "", struct{}{})
		if err == nil {
			var out wire.Listed
			if json.Unmarshal(rep.Body, &out) == nil {
				var leases []listing.BrokerLease

				for _, l := range out.Leases {
					bl := fromWire(l, key != "" && l.Holder.Key == key)
					bl.Risk, bl.AtRisk = riskOf(l.ID)
					leases = append(leases, bl)
				}

				return leases
			}
		}
	}

	return a.leasesOnFile(stateDir, key, riskOf)
}

func fromWire(l wire.Lease, mine bool) listing.BrokerLease {
	names := make([]string, len(l.Entries))
	for i, n := range l.Entries {
		names[i] = n.String()
	}

	bl := listing.BrokerLease{
		ID: l.ID, Reach: l.Reach, State: l.State, Names: names, Anchor: l.Anchor, Holder: holderLine(l.Holder), Node: l.Node,
		Liveness: l.Liveness, Mine: mine, Token: l.Token,
	}

	if l.ExpiresMillis != 0 {
		bl.Expires = time.UnixMilli(l.ExpiresMillis).Local().Format(time.RFC3339)
	}

	return bl
}

func holderLine(h wire.Holder) string {
	parts := []string{}
	if h.Handle != "" {
		parts = append(parts, fmt.Sprintf("handle %q", h.Handle))
	}

	if h.Name != "" {
		parts = append(parts, fmt.Sprintf("agent name %q", h.Name))
	}

	agent := h.Agent
	if h.SessionID != "" {
		agent += " session " + h.SessionID
	}

	return strings.Join(append(parts, agent), ", ")
}

// leasesOnFile lists this holder's broker leases from the node's files, when the broker cannot be asked.
// The state of a lease no live node confirms is unknown.
func (a *app) leasesOnFile(stateDir, key string, riskOf func(string) (string, bool)) []listing.BrokerLease {
	scs, unusable, err := handoff.Sidecars(stateDir)
	if err != nil {
		return nil
	}

	var out []listing.BrokerLease

	for _, sc := range scs {
		if key == "" || handoff.HolderKey(sc.Holder.Holder, sc.Holder.Liveness) != key {
			continue
		}

		names := make([]string, len(sc.Entries))
		for i, n := range sc.Entries {
			names[i] = n.String()
		}

		bl := listing.BrokerLease{
			ID: sc.ID, Reach: sc.Reach, State: "unknown", Names: names, Anchor: sc.Anchor.ID, Mine: true, Token: sc.Token,
			Holder: holderLine(wire.Holder{Handle: sc.Holder.Holder.Handle, Agent: sc.Holder.Holder.Agent, SessionID: sc.Holder.Holder.SessionID}),
		}

		bl.Risk, bl.AtRisk = riskOf(sc.ID)
		out = append(out, bl)
	}

	for _, id := range unusable {
		out = append(out, listing.BrokerLease{ID: id, State: "unknown", Risk: "its sidecar cannot be used by this nitipan, so its state is not checked"})
	}

	return out
}

// noteNoticed remembers the notices a command delivered, so a late refusal's notice can release the
// anchor its claim took.
func (a *app) noteNoticed(target string) {
	a.noticed = append(a.noticed, target)
}

// releaseOrphans releases the anchors of claims whose late refusal this command just delivered, by the
// same rules a release uses.
func (a *app) releaseOrphans() {
	if len(a.noticed) == 0 {
		return
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return
	}

	for _, target := range a.noticed {
		o, err := handoff.ReadOrphan(stateDir, target)
		if err != nil {
			continue
		}

		h, err := a.holder()
		if err != nil {
			return
		}

		e, err := a.engine()
		if err != nil {
			a.warn(err)

			return
		}

		a.releaseAnchors(e, h, stateDir, []handoff.Sidecar{{ID: o.ID, Anchor: o.Anchor, Holder: o.Holder}})

		dir, err := state.Open(stateDir, a.deps.StateOptions)
		if err != nil {
			continue
		}

		l, err := dir.Lock()
		if err != nil {
			a.warn(err)

			continue
		}

		if err := handoff.RemoveOrphan(l, o.ID); err != nil {
			a.warn(err)
		}

		_ = l.Unlock()
	}

	a.noticed = nil
}
