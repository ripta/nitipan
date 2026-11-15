package node

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const (
	recallParent = "01H00000000000000000000P01"
	recallChild  = "01H00000000000000000000C01"
)

var recallEpoch = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// recallClock is the wall clock the node reads. The test moves it, so no test waits for a deadline.
type recallClock struct{ ns atomic.Int64 }

func newRecallClock(t time.Time) *recallClock {
	c := &recallClock{}
	c.set(t)

	return c
}

func (c *recallClock) set(t time.Time) { c.ns.Store(t.UnixNano()) }
func (c *recallClock) now() time.Time  { return time.Unix(0, c.ns.Load()).UTC() }

func plantLease(t *testing.T, stateDir, id, parent, path string, pid int) {
	t.Helper()

	env := holderEnvPID(pid, record.LivenessProcess)
	env.ID = id
	env.Footprint = record.PathFootprint([]keys.Key{{Path: path}})
	env.ClearWhenDead = true
	env.Version = record.PathVersion

	rec := record.Record{
		Envelope: env, Reach: record.ReachMachine, Parent: parent,
		Domain:     record.Domain{Kind: record.KindPath, Checkout: path, Keys: []keys.Key{{Path: path}}},
		ActingFor:  "tester",
		AcquiredAt: recallEpoch,
	}

	if parent != "" {
		rec.Version = record.ParentVersion
	}

	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	writeStateFile(t, stateDir, state.LeasesDir, id, data)
}

func plantRecall(t *testing.T, stateDir, status string, deadline time.Time) {
	t.Helper()

	env := holderEnvPID(4242, record.LivenessProcess)
	env.Version = record.RecallVersion
	env.ID = recallChild
	env.Footprint = record.NoneFootprint()
	env.ClearWhenDead = true

	rc := record.Recall{
		Envelope: env, Parent: recallParent, RecalledAt: recallEpoch, Deadline: deadline.UTC(), State: status,
	}

	data, err := rc.Encode()
	if err != nil {
		t.Fatal(err)
	}

	writeStateFile(t, stateDir, state.RecallsDir, recallChild, data)
}

func writeStateFile(t *testing.T, stateDir, sub, id string, data []byte) {
	t.Helper()

	dir := filepath.Join(stateDir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func plantFamily(t *testing.T, stateDir string, childPID int) {
	t.Helper()

	plantLease(t, stateDir, recallParent, "", "/work/svc", 4242)
	plantLease(t, stateDir, recallChild, recallParent, "/work/svc/api", childPID)
}

func mirrorIDs(t *testing.T, lr *leaseRig) []string {
	t.Helper()

	snap, err := lr.run.n.snapshot()
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, m := range snap.Leases {
		ids = append(ids, m.ID)
	}

	sort.Strings(ids)

	return ids
}

func brokerIDs(lr *leaseRig) []string {
	v, ok := lr.view()
	if !ok || !v.HasSnapshot {
		return nil
	}

	var ids []string
	for _, m := range v.Leases {
		ids = append(ids, m.ID)
	}

	sort.Strings(ids)

	return ids
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

// newRecallRig starts a node on the given wall clock with a silent watcher and no heartbeat to speak of.
// Only a poke on a watched directory, or the deadline check, can resend the mirror after the first snapshot.
func newRecallRig(t *testing.T, r *rig, clock *recallClock, w *pokeWatch) *leaseRig {
	t.Helper()

	return newLeaseRigOn(t, r, func(o *Options) {
		o.Now = clock.now
		o.Heartbeat = time.Hour
		o.Watch = w.watch
	})
}

// pokeWatch is a watcher that reports a change only when the test says so.
type pokeWatch struct {
	mu  sync.Mutex
	chs map[string]chan struct{}
}

func (w *pokeWatch) watch(ctx context.Context, dir string) (<-chan struct{}, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.chs == nil {
		w.chs = map[string]chan struct{}{}
	}

	ch := make(chan struct{}, 1)
	w.chs[filepath.Base(dir)] = ch

	go func() { <-ctx.Done() }()

	return ch, nil
}

func (w *pokeWatch) poke(t *testing.T, base string) {
	t.Helper()

	eventually(t, "the node to watch "+base, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()

		ch, ok := w.chs[base]
		if ok {
			select {
			case ch <- struct{}{}:
			default:
			}
		}

		return ok
	})
}

type recallMirrorTest struct {
	Name     string
	ChildPID int
	Status   string
	Deadline time.Duration
	Want     []string
}

var recallMirrorTests = []recallMirrorTest{
	{"keeps the sub-lease before the deadline", 4242, record.RecallAnnounced, time.Minute, []string{recallChild, recallParent}},
	{"drops the sub-lease at the inclusive deadline", 4242, record.RecallAnnounced, 0, []string{recallParent}},
	{"drops the sub-lease past the deadline of an announcing recall", 4242, record.RecallAnnouncing, -time.Minute, []string{recallParent}},
	{"keeps a sub-lease past the deadline of a cancelling recall", 4242, record.RecallCancelling, -time.Minute, []string{recallChild, recallParent}},
	{"keeps a sub-lease whose own holder is dead for the clearing that removes it", 9999, record.RecallAnnounced, -time.Minute, []string{recallChild, recallParent}},
}

// The mirror is what discovery answers from, so a sub-lease it still carries past its deadline is
// reported as the owner of keys it no longer holds.
func TestNodeMirrorEndsASubLeaseOnlyWhileItsRecallIsInForceAndPastTheDeadline(t *testing.T) {
	for _, test := range recallMirrorTests {
		t.Run(test.Name, func(t *testing.T) {
			r := newRig(t)
			clock := newRecallClock(recallEpoch)

			plantFamily(t, r.state, test.ChildPID)
			plantRecall(t, r.state, test.Status, recallEpoch.Add(test.Deadline))

			lr := newRecallRig(t, r, clock, &pokeWatch{})

			if got := mirrorIDs(t, lr); !sameIDs(got, test.Want...) {
				t.Fatalf("mirror = %v, want %v", got, test.Want)
			}

			if _, err := os.Stat(filepath.Join(r.state, state.LeasesDir, recallChild+".json")); err != nil {
				t.Fatalf("the node must not remove lease state: %v", err)
			}
		})
	}
}

// A recall this node cannot use, and a recall whose parent it cannot use, must never end a child. The
// warning appears once, not on every heartbeat.
func TestNodeMirrorKeepsASubLeaseWhenTheRecallOrItsParentIsUnusable(t *testing.T) {
	t.Run("recall record", func(t *testing.T) {
		r := newRig(t)
		plantFamily(t, r.state, 4242)
		writeStateFile(t, r.state, state.RecallsDir, recallChild, []byte(`{"not":"a recall"}`))

		var warned atomic.Int32

		lr := newLeaseRigOn(t, r, func(o *Options) {
			o.Now = newRecallClock(recallEpoch).now
			o.Warn = func(msg string) {
				if strings.Contains(msg, "recall") {
					warned.Add(1)
				}
			}
		})

		for range 3 {
			if got := mirrorIDs(t, lr); !sameIDs(got, recallChild, recallParent) {
				t.Fatalf("mirror = %v", got)
			}
		}

		if warned.Load() != 1 {
			t.Fatalf("warnings = %d, want 1", warned.Load())
		}
	})

	t.Run("parent record", func(t *testing.T) {
		r := newRig(t)
		plantFamily(t, r.state, 4242)
		writeStateFile(t, r.state, state.LeasesDir, recallParent, []byte(`{"version":"9.0"}`))
		plantRecall(t, r.state, record.RecallAnnounced, recallEpoch.Add(-time.Minute))

		lr := newLeaseRigOn(t, r, func(o *Options) { o.Now = newRecallClock(recallEpoch).now })

		if got := mirrorIDs(t, lr); !sameIDs(got, recallChild, recallParent) {
			t.Fatalf("mirror = %v", got)
		}
	})
}

// A deadline passing changes no file, so only the node's own deadline check can resend the mirror. The test
// moves the clock and never writes a file after the first snapshot. It cannot show that the resend
// happens at the deadline to the second, only that it comes without any file event.
func TestNodeResendsTheMirrorWhenARecallDeadlinePassesWithNoFileChange(t *testing.T) {
	r := newRig(t)
	clock := newRecallClock(recallEpoch)

	plantFamily(t, r.state, 4242)
	plantRecall(t, r.state, record.RecallAnnounced, recallEpoch.Add(time.Minute))

	lr := newRecallRig(t, r, clock, &pokeWatch{})

	eventually(t, "both leases mirrored before the deadline", func() bool { return sameIDs(brokerIDs(lr), recallChild, recallParent) })

	clock.set(recallEpoch.Add(time.Minute))

	eventually(t, "the broker to hold only the parent at the deadline", func() bool { return sameIDs(brokerIDs(lr), recallParent) })

	if _, err := os.Stat(filepath.Join(r.state, state.LeasesDir, recallChild+".json")); err != nil {
		t.Fatalf("the deadline check must not end the sub-lease: %v", err)
	}
}

// A new recall, and the cancel of one, change what the mirror says without touching leases/.
func TestNodeResendsTheMirrorWhenARecallIsWrittenAndWhenItIsCancelled(t *testing.T) {
	r := newRig(t)
	clock := newRecallClock(recallEpoch)
	w := &pokeWatch{}

	plantFamily(t, r.state, 4242)

	lr := newRecallRig(t, r, clock, w)

	eventually(t, "both leases mirrored", func() bool { return sameIDs(brokerIDs(lr), recallChild, recallParent) })

	plantRecall(t, r.state, record.RecallAnnounced, recallEpoch.Add(-time.Second))
	w.poke(t, state.RecallsDir)

	eventually(t, "the broker to hold only the parent after the recall", func() bool { return sameIDs(brokerIDs(lr), recallParent) })

	plantRecall(t, r.state, record.RecallCancelling, recallEpoch.Add(-time.Second))
	w.poke(t, state.RecallsDir)

	eventually(t, "the broker to hold both again after the cancel", func() bool { return sameIDs(brokerIDs(lr), recallChild, recallParent) })
}

// A recall side file the node cannot read must not cost the mirror of every lease. A snapshot error ends
// the session, and the restart would hit the same error again.
func TestNodeMirrorStillSendsEveryLeaseWhenTheRecallsDirectoryCannotBeRead(t *testing.T) {
	r := newRig(t)

	plantFamily(t, r.state, 4242)
	if err := os.WriteFile(filepath.Join(r.state, state.RecallsDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	var warned atomic.Int32

	lr := newLeaseRigOn(t, r, func(o *Options) {
		o.Now = newRecallClock(recallEpoch).now
		o.Warn = func(msg string) {
			if strings.Contains(msg, "recall") {
				warned.Add(1)
			}
		}
	})

	for range 2 {
		if got := mirrorIDs(t, lr); !sameIDs(got, recallChild, recallParent) {
			t.Fatalf("mirror = %v", got)
		}
	}

	if warned.Load() != 1 {
		t.Fatalf("warnings = %d, want 1", warned.Load())
	}
}
