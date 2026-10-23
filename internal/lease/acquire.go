package lease

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Domain is a checkout plus optional path scopes, as the caller gave them.
type Domain struct {
	Checkout string
	Scopes   []string
}

// AcquireRequest asks to claim a domain. A nil Name, Mode, or Description means the flag was left
// out, and the stored value stays.
type AcquireRequest struct {
	Holder      adapter.Holder
	Domain      Domain
	Name        *string
	Mode        *string
	Description *string

	// Resource names a resource to claim in place of a domain. Domain and Replace stay empty, and the
	// engine refuses a claim that sets either.
	Resource string

	// Slots is the count the caller expects for the resource, or nil when it stated none. A path
	// claim refuses it.
	Slots *int

	// Replace names the caller's leases to rewrite into this claim. The first is rewritten in place
	// and keeps its ID. The rest are merged into it.
	Replace []string
}

// Outcome says what an acquire did.
type Outcome int

// Outcomes of a successful acquire.
const (
	Created Outcome = iota

	// Refreshed means the caller already held exactly these keys, and the advertisement changed.
	Refreshed

	// Unchanged means the caller already held exactly these keys, and nothing needed changing.
	Unchanged

	// Replaced means `--replace` rewrote a lease to the new keys.
	Replaced
)

// AcquireResult describes a successful acquire.
type AcquireResult struct {
	Lease   *record.Record
	Outcome Outcome

	// Cleared lists the dead holders' leases the claim removed. Absorbed lists the leases a merge
	// folded into Lease.
	Cleared  []string
	Absorbed []string

	// Dropped lists the keys a replace released because the new claim does not contain them.
	Dropped []string
}

// Acquire claims a domain under the lock.
//
// The check against other holders always runs first, so a key another live lease holds is refused
// even when the caller's own leases cover it. Only then does the caller's own state decide between
// an idempotent re-acquire, a covered claim, a refused scope change, and a new lease.
func (e *Engine) Acquire(req AcquireRequest) (*AcquireResult, error) {
	h := req.Holder

	if h.MissingSession() {
		return nil, ErrMissingSession
	}

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	// Validation runs after begin so that a request that fails here still delivers the caller's
	// tombstones. A broken holder gets no other signal. The session gate runs here too, under the
	// lock, so what it reads agrees with the upkeep that ran as the lock was taken.
	if err := validMode(h.Adapter, req.Mode); err != nil {
		return nil, err
	}

	if err := e.admit(h); err != nil {
		return nil, err
	}

	if req.Resource != "" {
		return e.acquireResource(c, req)
	}

	if req.Slots != nil {
		return nil, usagef("--slots applies to a resource claim, and this claim names a path")
	}

	domain, err := e.Canon.Domain(req.Domain.Checkout, req.Domain.Scopes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	if err := c.firstUnusable(); err != nil {
		return nil, err
	}

	handle := e.handleFor(h)

	if err := c.checkHandle(handle); err != nil {
		return nil, err
	}

	cleared, err := c.checkOthers(record.PathFootprint(domain.Keys), "")
	if err != nil {
		return nil, err
	}

	cl := &claim{call: c, req: req, domain: domain, handle: handle, cleared: cleared}

	return cl.decide()
}

// acquireResource claims a resource name. Names are flat, so a claim meets a lease only when the
// names are equal. The caller's own lease on the name is a refresh. Another holder's lease on the
// name takes a slot instead of blocking, while the slots last.
func (e *Engine) acquireResource(c *call, req AcquireRequest) (*AcquireResult, error) {
	h := req.Holder
	name := req.Resource

	if err := keys.ValidateResource(name); err != nil {
		return nil, usagef("%v", err)
	}

	if len(req.Replace) > 0 {
		return nil, failf(exitcode.ErrRefused, "--replace does not apply to a resource claim. A different resource name "+
			"is a different lease, so release the old one and acquire the new one")
	}

	cl := &claim{call: c, req: req, resource: name}

	if err := cl.checkSlots(); err != nil {
		return nil, err
	}

	if err := c.firstUnusable(); err != nil {
		return nil, err
	}

	cl.handle = e.handleFor(h)

	if err := c.checkHandle(cl.handle); err != nil {
		return nil, err
	}

	cleared, err := c.checkOthers(record.ResourceFootprint(name), name)
	if err != nil {
		return nil, err
	}

	cl.cleared = cleared

	if equal := cl.equalResource(); equal != nil {
		return cl.refresh(equal)
	}

	if err := cl.admitSlot(); err != nil {
		return nil, err
	}

	return cl.create()
}

// claim is an acquire that has passed the checks against other holders.
type claim struct {
	*call

	req     AcquireRequest
	domain  keys.Domain
	handle  string
	cleared []*entry

	// resource is the name of a resource claim. It is empty for a path claim, which uses domain.
	resource string
}

func (cl *claim) fields() holderFields {
	return holderFields{
		Name:    cl.req.Name,
		Mode:    cl.req.Mode,
		Session: cl.req.Holder.SessionID,
		Host:    cl.hostSync(cl.req.Holder),
	}
}

func (cl *claim) footprint() record.Footprint {
	if cl.resource != "" {
		return record.ResourceFootprint(cl.resource)
	}

	return record.PathFootprint(cl.domain.Keys)
}

// equalResource returns the caller's lease on the claimed name. A record written on another machine
// is never the caller's, since a resource never leaves its machine.
func (cl *claim) equalResource() *entry {
	for _, en := range cl.mine {
		if name, ok := en.resource(); ok && name == cl.resource && liveness.SameMachine(cl.e.Machine, en.Rec.Machine) {
			return en
		}
	}

	return nil
}

// checkSlots compares the caller's stated count with the count that applies. A re-acquire is judged
// against the record's count, and a new claim against the configured one.
func (cl *claim) checkSlots() error {
	stated := cl.req.Slots
	if stated == nil {
		return nil
	}

	if *stated < 1 {
		return usagef("--slots must be an integer of at least 1, got %d", *stated)
	}

	want := cl.slots.Count(cl.resource)
	if en := cl.equalResource(); en != nil {
		want = recordSlots(en.Rec)
	}

	if *stated != want {
		return failf(exitcode.ErrRefused, "--slots %d does not match the count %d that applies to %s", *stated, want, cl.resource)
	}

	return nil
}

func (cl *claim) decide() (*AcquireResult, error) {
	if len(cl.req.Replace) > 0 {
		return cl.replace()
	}

	if equal := cl.equalKeys(); equal != nil {
		return cl.refresh(equal)
	}

	if covering := cl.coveredBy(); covering != nil {
		return nil, cl.coveredErr(covering)
	}

	if overlapped := cl.overlappedMine(); len(overlapped) > 0 {
		return nil, cl.selfOverlapErr(overlapped)
	}

	return cl.create()
}

// checkHandle fails the call when a different live holder derived the same handle. A suffix would
// be reassigned once the other holder released, and a sender who learned the base handle would then
// reach the wrong session.
func (c *call) checkHandle(handle string) error {
	for _, en := range c.others {
		if en.Rec.Holder.Handle == handle && en.Live.TreatAsAlive() {
			return fmt.Errorf("%w: handle %q is already used by %s; ask the user", ErrHandleCollision, handle,
				describeHolder(en.Rec.Holder))
		}
	}

	return nil
}

// checkOthers refuses the claim when another holder's lease overlaps it. A dead holder's lease that
// allows clearing does not block, and is returned to be removed once the claim goes through. A
// resource record from another machine is another machine's resource, so it never overlaps.
//
// A live local record of the resource a claim names, given as slotted, does not block here. The
// claim counts it against the slots instead. Every other overlap blocks as before.
func (c *call) checkOthers(fp record.Footprint, slotted string) (cleared []*entry, err error) {
	var (
		blocks []string
		stuck  bool
	)

	for _, en := range c.others {
		if en.Rec.Domain.Kind == record.KindResource && !liveness.SameMachine(c.e.Machine, en.Rec.Machine) {
			continue
		}

		if !en.Rec.Footprint.Overlaps(fp, c.e.Finder) {
			continue
		}

		switch {
		case en.Live.Status == liveness.Dead && en.Rec.Clearable():
			cleared = append(cleared, en)
		case en.Live.Status == liveness.Dead:
			stuck = true
			blocks = append(blocks, fmt.Sprintf("%s on %s is held by %s, which has died, but its record asks that only a "+
				"newer nitipan clear it (record version %s, this nitipan writes %s)", en.file(), en.domainList(),
				describeHolder(en.Rec.Holder), en.Rec.Version, record.Current))
		case slotted != "" && isResourceNamed(en, slotted):
			// A live holder of the slotted name takes a slot. The claim counts it after this loop.
		default:
			blocks = append(blocks, c.heldBy(en))
		}
	}

	if len(blocks) == 0 {
		return cleared, nil
	}

	next := "Coordinate with the holder or ask the user."
	if stuck {
		next = "Upgrade nitipan, coordinate with the holder, or ask the user."
	}

	return nil, failf(exitcode.ErrRefused, "the claim overlaps %s.\n  %s\n%s",
		Plural(len(blocks), "a lease", "leases"), strings.Join(blocks, "\n  "), next)
}

// isResourceNamed reports whether en is a record of the named resource that this binary understands.
func isResourceNamed(en *entry, name string) bool {
	got, ok := en.resource()

	return ok && got == name
}

// admitSlot refuses a new holder of a slot when the live holders already fill it. The limit is the
// config's count, lowered to the count of every live holder, so raising the config admits no one
// while a holder admitted under the old count remains.
func (cl *claim) admitSlot() error {
	view := cl.slotViews(cl.slots)[cl.resource]
	if view == nil || len(view.Holders) < view.Limit {
		return nil
	}

	lines := make([]string, len(view.Holders))
	for i, en := range view.Holders {
		lines[i] = cl.heldBy(en)
	}

	why := ""
	if configured := cl.slots.Count(cl.resource); view.Limit < configured {
		why = fmt.Sprintf(" The configured count is %d, but a holder admitted under a count of %d keeps the limit "+
			"there until it releases.", configured, view.Limit)
	}

	return failf(exitcode.ErrRefused, "%s has %d of %d slots taken.%s\n  %s\nCoordinate with a holder, or ask the user to "+
		"raise the count for %s.", cl.resource, len(view.Holders), view.Limit, why, strings.Join(lines, "\n  "), cl.resource)
}

// heldBy describes a live or unknown holder that blocks a claim.
func (c *call) heldBy(en *entry) string {
	rec := en.Rec
	msg := fmt.Sprintf("%s on %s is held by %s", en.file(), en.domainList(), describeHolder(rec.Holder))

	if c.sameProcess[en] {
		msg += ", another session in this same process"
	}

	if en.Live.Status != liveness.Alive {
		msg += fmt.Sprintf(" (its liveness is unknown, which counts as alive: %s)", en.Live.Reason)
	}

	if rec.Description != "" {
		msg += fmt.Sprintf(": %s", rec.Description)
	}

	return msg
}

// Plural picks the singular or plural form for a count.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}

// equalKeys returns the caller's lease whose key set equals the claim.
func (cl *claim) equalKeys() *entry {
	for _, en := range cl.mine {
		if ks, ok := en.keysOf(); ok && sameKeySet(ks, cl.domain.Keys) {
			return en
		}
	}

	return nil
}

func sameKeySet(a, b []keys.Key) bool {
	if len(a) != len(b) {
		return false
	}

	for _, k := range a {
		if !slices.ContainsFunc(b, func(o keys.Key) bool { return keys.Equal(k, o) }) {
			return false
		}
	}

	return true
}

// coveredBy returns the caller's leases that cover the claim, or nil when any new key is not covered
// by one held key. Several held keys cover separate new keys, never one new key by union.
func (cl *claim) coveredBy() []*entry {
	var covering []*entry

	for _, k := range cl.domain.Keys {
		var found *entry

		for _, en := range cl.mine {
			if ks, ok := en.keysOf(); ok && keys.Covered(k, ks, cl.e.Finder) {
				found = en

				break
			}
		}

		if found == nil {
			return nil
		}

		if !slices.Contains(covering, found) {
			covering = append(covering, found)
		}
	}

	return covering
}

func (cl *claim) coveredErr(covering []*entry) error {
	ids := make([]string, len(covering))
	for i, en := range covering {
		ids[i] = en.ID
	}

	msg := fmt.Sprintf("already covered by your leases %s; nothing changed", strings.Join(ids, ", "))

	if cl.req.Name != nil || cl.req.Mode != nil || cl.req.Description != nil {
		msg += ". Use advertise to change the name, permission mode, or description"
	}

	return failf(exitcode.ErrAlreadyCovered, "%s", msg)
}

// overlappedMine returns the caller's leases that overlap the claim without being equal to it.
func (cl *claim) overlappedMine() []*entry {
	var out []*entry

	fp := cl.footprint()

	for _, en := range cl.mine {
		if en.Rec.Footprint.Overlaps(fp, cl.e.Finder) {
			out = append(out, en)
		}
	}

	return out
}

// droppedKeys lists the keys of the given leases that no new key contains. Replacing them would
// release those keys. A lease whose body this binary does not understand has keys it cannot list.
func (cl *claim) droppedKeys(leases []*entry) (dropped []keys.Key, known bool) {
	known = true

	for _, en := range leases {
		ks, ok := en.keysOf()
		if !ok {
			known = false

			continue
		}

		for _, held := range ks {
			contained := slices.ContainsFunc(cl.domain.Keys, func(n keys.Key) bool {
				return keys.Contains(n, held, cl.e.Finder)
			})
			if !contained {
				dropped = append(dropped, held)
			}
		}
	}

	return dropped, known
}

// uncoveredClaim spells the claim that adds only what the caller does not already hold.
func (cl *claim) uncoveredClaim() string {
	var held []keys.Key

	for _, en := range cl.mine {
		if ks, ok := en.keysOf(); ok {
			held = append(held, ks...)
		}
	}

	var scopes []string

	for _, k := range cl.domain.Keys {
		if keys.Covered(k, held, cl.e.Finder) {
			continue
		}

		rel, err := filepath.Rel(cl.domain.Checkout.Path, k.Path)
		if err != nil || rel == "." {
			continue
		}

		scopes = append(scopes, rel)
	}

	if len(scopes) == 0 {
		return ""
	}

	parts := []string{"acquire --checkout " + shellQuote(cl.domain.Checkout.Path)}
	for _, s := range scopes {
		parts = append(parts, "--scope "+shellQuote(s))
	}

	return strings.Join(parts, " ")
}

// selfOverlapErr refuses a scope change. It names --replace only when a dry run of the replace
// would pass and release no keys, because an agent follows the command its error names.
func (cl *claim) selfOverlapErr(overlapped []*entry) error {
	ids := make([]string, len(overlapped))
	for i, en := range overlapped {
		ids[i] = en.ID
	}

	head := fmt.Sprintf("the claim overlaps your own %s %s without equaling %s",
		Plural(len(ids), "lease", "leases"), strings.Join(ids, ", "), Plural(len(ids), "it", "any of them"))

	dropped, known := cl.droppedKeys(overlapped)

	switch {
	case !known:
		return failf(exitcode.ErrRefused, "%s. One of them is a record this nitipan cannot rewrite, so --replace is not "+
			"offered. Coordinate or ask the user.", head)
	case len(dropped) > 0:
		paths := make([]string, len(dropped))
		for i, k := range dropped {
			paths[i] = k.Path
		}

		msg := fmt.Sprintf("%s. Replacing would release %s.", head, strings.Join(paths, ", "))
		if alt := cl.uncoveredClaim(); alt != "" {
			msg += fmt.Sprintf(" To add only what you do not already hold, run: %s.", alt)
		}

		return failf(exitcode.ErrRefused, "%s Otherwise coordinate or ask the user.", msg)
	case overlapped[0].Rec.Rewritable() != nil:
		return failf(exitcode.ErrRefused, "%s. The first of them is a record of a newer version that this nitipan "+
			"must not rewrite, so --replace is not offered. Upgrade nitipan or ask the user.", head)
	}

	return failf(exitcode.ErrRefused, "%s. To rewrite %s to the new keys, pass %s.", head, Plural(len(ids), "it", "them"),
		replaceFlags(ids))
}

func replaceFlags(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "--replace " + id
	}

	return strings.Join(parts, " ")
}

// removeCleared deletes the dead holders' leases the claim displaced.
func (cl *claim) removeCleared() ([]string, error) {
	var ids []string

	for _, en := range cl.cleared {
		if err := cl.l.Remove(state.LeasesDir, en.ID); err != nil {
			return ids, fmt.Errorf("clearing the lease of a dead holder: %w", err)
		}

		ids = append(ids, en.ID)
	}

	return ids, nil
}

// newRecord builds a lease for the caller. It inherits the holder-wide name and mode.
func (cl *claim) newRecord() *record.Record {
	h := cl.req.Holder
	name, mode := cl.effective(cl.fields())

	holder := h.Record()
	holder.Handle = cl.handle
	holder.Name = name

	desc := ""
	if cl.req.Description != nil {
		desc = *cl.req.Description
	}

	version, domain := record.PathVersion, record.Domain{
		Kind:     record.KindPath,
		Checkout: cl.req.Domain.Checkout,
		Scopes:   cl.req.Domain.Scopes,
		Keys:     cl.domain.Keys,
	}

	if cl.resource != "" {
		version = record.ResourceVersion
		domain = record.Domain{Kind: record.KindResource, Name: cl.resource, Slots: cl.slots.Count(cl.resource)}
	}

	return &record.Record{
		Envelope: record.Envelope{
			Version:       version,
			ID:            cl.e.NewID(),
			Footprint:     cl.footprint(),
			ClearWhenDead: true,
			Liveness:      h.Adapter.Liveness,
			Machine:       cl.e.Machine,
			Holder:        holder,
		},
		Reach:          record.ReachMachine,
		Domain:         domain,
		ActingFor:      h.User,
		PermissionMode: mode,
		Description:    desc,
		AcquiredAt:     cl.e.Now().UTC(),
		BodyKnown:      true,
	}
}

func (cl *claim) create() (*AcquireResult, error) {
	w := newWrites()
	cl.syncHolder(w, cl.fields())

	if err := w.validate(); err != nil {
		return nil, err
	}

	rec := cl.newRecord()

	data, err := rec.Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding the new lease: %w", err)
	}

	cleared, err := cl.removeCleared()
	if err != nil {
		return nil, err
	}

	if err := cl.l.Publish(state.LeasesDir, rec.ID, data); err != nil {
		return nil, fmt.Errorf("publishing the new lease: %w", err)
	}

	if err := cl.flush(w); err != nil {
		return nil, err
	}

	cl.reportMoves(cl.req.Holder, w)

	return &AcquireResult{Lease: rec, Outcome: Created, Cleared: cleared}, nil
}

// refresh handles the idempotent re-acquire: the caller already holds exactly these keys.
func (cl *claim) refresh(target *entry) (*AcquireResult, error) {
	w := newWrites()
	cl.syncHolder(w, cl.fields())

	if cl.req.Description != nil {
		desc := *cl.req.Description

		w.edit(target, true, func(r *record.Record) bool {
			changed := r.Description != desc
			r.Description = desc

			return changed
		})
	}

	if err := w.validate(); err != nil {
		return nil, err
	}

	cleared, err := cl.removeCleared()
	if err != nil {
		return nil, err
	}

	outcome := Unchanged

	for _, p := range w.byEntry {
		if p.forced {
			outcome = Refreshed
		}
	}

	if err := cl.flush(w); err != nil {
		return nil, err
	}

	cl.reportMoves(cl.req.Holder, w)

	return &AcquireResult{Lease: w.result(target), Outcome: outcome, Cleared: cleared}, nil
}

// replace rewrites the first named lease to the new keys and merges the others into it.
//
// The rewrite is one rename, so a crash leaves the old record or the new one. The new record names
// the merged leases in absorbs, and any later locked call removes them if a crash left them behind.
func (cl *claim) replace() (*AcquireResult, error) {
	targets, err := cl.replaceTargets()
	if err != nil {
		return nil, err
	}

	lead, merged := targets[0], targets[1:]

	dropped, _ := cl.droppedKeys(targets)
	droppedPaths := make([]string, len(dropped))

	for i, k := range dropped {
		droppedPaths[i] = k.Path
	}

	if err := cl.noOtherOverlap(targets); err != nil {
		return nil, err
	}

	w := newWrites()
	cl.syncHolder(w, cl.fields())

	absorbs := slices.Clone(lead.Rec.Absorbs)
	mergedIDs := make([]string, len(merged))

	for i, en := range merged {
		mergedIDs[i] = en.ID
		absorbs = append(absorbs, en.ID)
	}

	w.edit(lead, true, func(r *record.Record) bool {
		r.Footprint = cl.footprint()
		r.Domain = record.Domain{
			Kind:     record.KindPath,
			Checkout: cl.req.Domain.Checkout,
			Scopes:   cl.req.Domain.Scopes,
			Keys:     cl.domain.Keys,
		}
		r.Absorbs = absorbs

		if cl.req.Description != nil {
			r.Description = *cl.req.Description
		}

		return true
	})

	for _, en := range merged {
		delete(w.byEntry, en)
	}

	if err := w.validate(); err != nil {
		return nil, err
	}

	cleared, err := cl.removeCleared()
	if err != nil {
		return nil, err
	}

	if err := cl.writeOne(w.byEntry[lead]); err != nil {
		return nil, err
	}

	if hook := cl.e.Hooks.AfterRewrite; hook != nil {
		if err := hook(); err != nil {
			return nil, err
		}
	}

	for _, en := range merged {
		if err := cl.l.Remove(state.LeasesDir, en.ID); err != nil {
			return nil, fmt.Errorf("merging %s: %w", en.ID, err)
		}
	}

	rest := newWrites()
	for en, p := range w.byEntry {
		if en != lead {
			rest.byEntry[en] = p
		}
	}

	if err := cl.flush(rest); err != nil {
		return nil, err
	}

	cl.reportMoves(cl.req.Holder, w)

	return &AcquireResult{Lease: w.result(lead), Outcome: Replaced, Cleared: cleared, Absorbed: mergedIDs,
		Dropped: droppedPaths}, nil
}

// replaceTargets resolves the IDs named by --replace to the caller's own leases.
func (cl *claim) replaceTargets() ([]*entry, error) {
	var targets []*entry

	for _, id := range cl.req.Replace {
		if !state.ValidID(id) {
			return nil, usagef("%q is not a lease ID", id)
		}

		if en := cl.mineByID(id); en != nil {
			if name, ok := en.resource(); ok {
				return nil, failf(exitcode.ErrRefused, "--replace %s: that lease is on the resource %s, and --replace does not "+
					"apply to resource leases. Release it and claim the path", id, name)
			}

			if !slices.Contains(targets, en) {
				targets = append(targets, en)
			}

			continue
		}

		if en := cl.entryByID(id); en != nil {
			return nil, failf(exitcode.ErrNotYours, "--replace %s: that lease belongs to %s", id, describeHolder(en.Rec.Holder))
		}

		return nil, failf(exitcode.ErrNoSuchLease, "--replace %s: no such lease; %s", id, cl.leaseList())
	}

	return targets, nil
}

// noOtherOverlap refuses a replace that would overlap a lease of the caller's that it does not name.
func (cl *claim) noOtherOverlap(targets []*entry) error {
	var left []string

	fp := cl.footprint()

	for _, en := range cl.mine {
		if !slices.Contains(targets, en) && en.Rec.Footprint.Overlaps(fp, cl.e.Finder) {
			left = append(left, en.ID)
		}
	}

	if len(left) == 0 {
		return nil
	}

	return failf(exitcode.ErrRefused, "the claim also overlaps your lease %s, which --replace does not name. Add it to "+
		"--replace, or ask the user.", strings.Join(left, ", "))
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes a word so a path with spaces or metacharacters survives a paste into a shell.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
