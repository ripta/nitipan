package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

const derivedRoot = "cccccccccccccccccccccccccccccccccccccccc"

// fakeGit answers the questions a root-set derivation asks, for a repo whose top directory is top.
type fakeGit struct {
	top     string
	common  string
	shallow bool
	delay   time.Duration
	calls   atomic.Int32
}

func (g *fakeGit) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	g.calls.Add(1)

	time.Sleep(g.delay)

	switch strings.Join(args, " ") {
	case "rev-parse --show-toplevel":
		return []byte(g.top), nil
	case "rev-parse --is-shallow-repository":
		return []byte(fmt.Sprint(g.shallow)), nil
	case "rev-parse --verify -q HEAD":
		return []byte("d00d"), nil
	case "rev-parse --path-format=absolute --git-common-dir":
		return []byte(g.common), nil
	case "rev-parse --show-superproject-working-tree", "ls-files --stage":
		return nil, nil
	}

	if args[0] == "rev-list" {
		return []byte(derivedRoot), nil
	}

	return nil, fmt.Errorf("unexpected git %v", args)
}

func newFakeGit(t *testing.T) (*fakeGit, *rootset.Deriver) {
	t.Helper()

	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	g := &fakeGit{top: top, common: filepath.Join(top, ".git")}

	return g, &rootset.Deriver{Run: g.run}
}

func writeDerivedFile(t *testing.T, stateDir string, d handoff.Derived) {
	t.Helper()

	d.Version = handoff.DerivedVersion

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.Write(filepath.Join(stateDir, handoff.DerivedDir), d.ID+".json", data); err != nil {
		t.Fatal(err)
	}
}

func mirrorOfLease(t *testing.T, lr *leaseRig, id string) wire.MirroredLease {
	t.Helper()

	var got wire.MirroredLease

	eventually(t, "the broker to mirror "+id, func() bool {
		v, ok := lr.view()
		if !ok {
			return false
		}

		for _, m := range v.Leases {
			if m.ID == id {
				got = m

				return true
			}
		}

		return false
	})

	return got
}

func TestNodeDerivesTheEntriesOfARecordNoCLIDerived(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	id, _ := writeRecord(t, lr.state, filepath.Join(g.top, "svc"))

	m := mirrorOfLease(t, lr, id)
	if m.Derived == nil || m.Derived.State != wire.DerivedOK || m.Derived.Derivation != rootset.Version ||
		len(m.Derived.Entries) != 1 || m.Derived.Entries[0].Path != "svc" || !m.Derived.Entries[0].HasRoot(derivedRoot) {
		t.Fatalf("derived = %+v", m.Derived)
	}

	side, err := handoff.ReadDerived(lr.state, id)
	if err != nil || side.By != handoff.DerivedByNode {
		t.Fatalf("the node records what it derived: %+v, %v", side, err)
	}

	calls := g.calls.Load()

	time.Sleep(200 * time.Millisecond)

	if g.calls.Load() != calls {
		t.Fatal("a lease with a sidecar for its keys is not derived again on every snapshot")
	}
}

func TestNodeUsesTheDerivedEntriesTheCLIWrote(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	path := filepath.Join(g.top, "svc")
	id, _ := writeRecord(t, lr.state, path)

	writeDerivedFile(t, lr.state, handoff.Derived{
		ID: id, By: handoff.DerivedByCLI, State: wire.DerivedOK, Derivation: 1, Keys: []keys.Key{{Path: path}},
		Entries: derivedNames("from-cli"), At: time.Now(),
	})

	m := mirrorOfLease(t, lr, id)
	if m.Derived == nil || m.Derived.Entries[0].Path != "from-cli" {
		t.Fatalf("derived = %+v", m.Derived)
	}

	if g.calls.Load() != 0 {
		t.Fatal("the node must not run git for a lease the CLI derived")
	}
}

func derivedNames(path string) []repoid.Name {
	return []repoid.Name{repoid.New([]string{derivedRoot}, path, false, false)}
}

func TestNodeDerivesAgainWhenTheKeysChanged(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	path := filepath.Join(g.top, "svc")
	id, _ := writeRecord(t, lr.state, path)

	writeDerivedFile(t, lr.state, handoff.Derived{
		ID: id, By: handoff.DerivedByCLI, State: wire.DerivedOK, Derivation: 1, Keys: []keys.Key{{Path: filepath.Join(g.top, "old")}},
		Entries: derivedNames("stale"), At: time.Now(),
	})

	m := mirrorOfLease(t, lr, id)
	if m.Derived == nil || m.Derived.Entries[0].Path != "svc" {
		t.Fatalf("a sidecar for other keys must not be used: %+v", m.Derived)
	}
}

func TestNodeReadsAFailedDerivationAsUnknownAndDoesNotRetryAtOnce(t *testing.T) {
	g, deriver := newFakeGit(t)
	g.shallow = true

	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	id, _ := writeRecord(t, lr.state, filepath.Join(g.top, "svc"))

	m := mirrorOfLease(t, lr, id)
	if m.Derived == nil || m.Derived.State != wire.DerivedFailed || !strings.Contains(m.Derived.Reason, "shallow") {
		t.Fatalf("derived = %+v", m.Derived)
	}

	calls := g.calls.Load()

	time.Sleep(200 * time.Millisecond)

	if g.calls.Load() != calls {
		t.Fatal("a failure is recorded, so the next snapshot does not run git again")
	}
}

func TestNodeLeavesASidecarOfAHigherMajorAlone(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	id, _ := writeRecord(t, lr.state, filepath.Join(g.top, "svc"))

	future := []byte(`{"version":"2.0","id":"` + id + `","state":"ok"}`)
	if err := atomicfile.Write(filepath.Join(lr.state, handoff.DerivedDir), id+".json", future); err != nil {
		t.Fatal(err)
	}

	m := mirrorOfLease(t, lr, id)
	if m.Derived != nil {
		t.Fatalf("a sidecar the node cannot read reads as unknown, not as derived: %+v", m.Derived)
	}

	got, err := os.ReadFile(filepath.Join(lr.state, handoff.DerivedDir, id+".json"))
	if err != nil || string(got) != string(future) {
		t.Fatalf("the unreadable sidecar was rewritten: %q, %v", got, err)
	}
}

func TestNodeWithoutADeriverLeavesMirrorsUnderived(t *testing.T) {
	lr := newLeaseRig(t, nil)

	id, _ := writeRecord(t, lr.state, "/work/a")

	if m := mirrorOfLease(t, lr, id); m.Derived != nil {
		t.Fatalf("derived = %+v", m.Derived)
	}
}

func TestNodeRemovesDerivedFilesOfEndedLeases(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	id, _ := writeRecord(t, lr.state, filepath.Join(g.top, "svc"))
	mirrorOfLease(t, lr, id)

	if err := os.Remove(filepath.Join(lr.state, state.LeasesDir, id+".json")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the derived file to go with its lease", func() bool {
		_, err := handoff.ReadDerived(lr.state, id)

		return err != nil
	})
}

func TestOverlapNoticesGoOncePerPairToHoldersThatCanReadThem(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	grant := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	cliPath := filepath.Join(g.top, "cli")
	cliID, _ := writeRecord(t, lr.state, cliPath)

	// The node derives the record first, so the CLI's file lands after it and is the one that stays.
	mirrorOfLease(t, lr, cliID)

	writeDerivedFile(t, lr.state, handoff.Derived{
		ID: cliID, By: handoff.DerivedByCLI, State: wire.DerivedOK, Derivation: 1, Keys: []keys.Key{{Path: cliPath}},
		Entries: derivedNames("x"), At: time.Now(),
	})

	v1Path := filepath.Join(g.top, "v1")
	v1ID, _ := writeRecord(t, lr.state, v1Path)
	mirrorOfLease(t, lr, v1ID)

	label := lr.run.n.Label()
	gf := wire.Found{Lease: grant.ID, Reach: wire.ReachUser, Node: label, Qualified: "g@" + label}

	overlaps := []wire.Overlap{
		{ID: grant.ID + "/" + cliID, Grant: gf, Mirror: wire.Found{Lease: cliID, Node: label, Qualified: "c@" + label}},
		{ID: grant.ID + "/" + v1ID, Grant: gf, Mirror: wire.Found{Lease: v1ID, Node: label, Qualified: "v@" + label}},
	}

	noticeCount := func() int {
		entries, _ := os.ReadDir(filepath.Join(lr.state, state.NoticesDir))

		return len(entries)
	}

	lr.run.n.applyOverlaps(overlaps)

	// Two grant-side notices go to the one holder, one per pair. One mirror-side notice goes to the
	// holder whose CLI wrote its sidecar. The V1 holder's lease gets none.
	if got := noticeCount(); got != 3 {
		t.Fatalf("notices = %d, want 3", got)
	}

	lr.run.n.applyOverlaps(overlaps)

	if got := noticeCount(); got != 3 {
		t.Fatalf("a standing overlap must give one notice, not one per snapshot: %d", got)
	}
}

func TestASnapshotBoundsItsDerivationWorkAndFinishesOnLaterSnapshots(t *testing.T) {
	g, deriver := newFakeGit(t)
	g.delay = 20 * time.Millisecond

	r := newRig(t)
	opts := r.options()
	opts.Roots = deriver
	opts.DeriveBudget = 30 * time.Millisecond

	n, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}

	const total = 6

	for i := 0; i < total; i++ {
		writeRecord(t, r.state, filepath.Join(g.top, fmt.Sprintf("svc%d", i)))
	}

	derived := func(snap wire.Snapshot) int {
		count := 0

		for _, m := range snap.Leases {
			if m.Derived != nil {
				count++
			}
		}

		return count
	}

	first, err := n.snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if got := derived(first); got == 0 || got == total {
		t.Fatalf("the first snapshot derived %d of %d leases, want some but not all, so a slow git cannot stall the heartbeat", got, total)
	}

	for range total {
		snap, err := n.snapshot()
		if err != nil {
			t.Fatal(err)
		}

		if derived(snap) == total {
			return
		}
	}

	t.Fatal("later snapshots must finish the derivation")
}

// submitOverlappingClaim hands the node a claim whose names overlap the mirrored lease cliPath, and
// returns once the reply is on disk. It leaves the reply unread, as a CLI that timed out would.
func submitOverlappingClaim(t *testing.T, lr *leaseRig, top string) string {
	t.Helper()

	cliPath := filepath.Join(top, "cli")
	cliID, _ := writeRecord(t, lr.state, cliPath)

	mirrorOfLease(t, lr, cliID)

	writeDerivedFile(t, lr.state, handoff.Derived{
		ID: cliID, By: handoff.DerivedByCLI, State: wire.DerivedOK, Derivation: 1, Keys: []keys.Key{{Path: cliPath}},
		Entries: derivedNames("x"), At: time.Now(),
	})

	forceSnapshot(t, lr, top, "settle")

	plantAnchor(t, lr.state, anchorA, "/work/a")

	body, err := json.Marshal(handoff.ClaimBody{
		Reach: wire.ReachUser, Entries: derivedNames("x/api"), Derivation: 1, Holder: holderEnv(record.LivenessProcess),
		Anchor: handoff.Anchor{ID: anchorA, Keys: []keys.Key{{Path: "/work/a"}}, Covered: []keys.Key{{Path: "/work/a"}}, Created: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	id := state.NewID()
	if err := handoff.Submit(lr.state, handoff.Request{ID: id, Kind: handoff.KindClaim, CreatedAt: time.Now(), Body: body}); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the node to answer the claim", func() bool {
		_, err := handoff.ReadReply(lr.state, id)

		return err == nil
	})

	return id
}

func countNoticesContaining(lr *leaseRig, substr string) int {
	count := 0

	for _, n := range lr.notices() {
		if strings.Contains(n.Detail, substr) {
			count++
		}
	}

	return count
}

// forceSnapshot makes the node send another snapshot, so the broker repeats the standing overlaps.
func forceSnapshot(t *testing.T, lr *leaseRig, top, name string) {
	t.Helper()

	id, _ := writeRecord(t, lr.state, filepath.Join(top, name))
	mirrorOfLease(t, lr, id)
}

func TestAnOverlapTheCLINeverReadStillReachesTheGrantsHolder(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) {
		o.Roots = deriver
		o.ReplyGrace = 200 * time.Millisecond
	})

	submitOverlappingClaim(t, lr, g.top)

	lr.waitNotice("granted after nitipan stopped waiting")
	forceSnapshot(t, lr, g.top, "other")

	eventually(t, "the overlap notice for a CLI that never printed the warning", func() bool {
		return countNoticesContaining(lr, "names repo files") == 1
	})
}

func TestAnOverlapTheCLIPrintedGivesNoSecondNotice(t *testing.T) {
	g, deriver := newFakeGit(t)
	lr := newLeaseRig(t, func(o *Options) { o.Roots = deriver })

	id := submitOverlappingClaim(t, lr, g.top)
	handoff.Consume(lr.state, id)

	for _, name := range []string{"other1", "other2"} {
		forceSnapshot(t, lr, g.top, name)
	}

	time.Sleep(300 * time.Millisecond)

	if got := countNoticesContaining(lr, "names repo files"); got != 0 {
		t.Fatalf("the CLI printed the warning, so the grant's holder needs no notice: %d", got)
	}
}
