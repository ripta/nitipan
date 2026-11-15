package node

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	anchorA = "01H0000000000000000000000A"
	anchorB = "01H0000000000000000000000B"
	anchorM = "01H0000000000000000000000M"
	anchorZ = "01H0000000000000000000000Z"
)

const testRoot = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var holderStart = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1}

func holderEnv(kind string) record.Envelope { return holderEnvPID(4242, kind) }

func holderEnvPID(pid int, kind string) record.Envelope {
	return record.Envelope{
		Liveness: kind, Machine: testMachine,
		Holder: record.Holder{Agent: "claude", PID: pid, Start: holderStart, SessionID: "s", Handle: "h"},
	}
}

// plantAnchor writes a V1 path record with a fixed ID, as `--replace` from a V1 binary would rewrite it.
func plantAnchor(t *testing.T, stateDir, id, path string) {
	t.Helper()

	rec := record.Record{
		Envelope: record.Envelope{
			Version: record.PathVersion, ID: id, Footprint: record.PathFootprint([]keys.Key{{Path: path}}), ClearWhenDead: true,
			Liveness: record.LivenessProcess, Machine: testMachine, Holder: holderEnv(record.LivenessProcess).Holder,
		},
		Reach: record.ReachMachine, Domain: record.Domain{Kind: record.KindPath, Checkout: path, Keys: []keys.Key{{Path: path}}},
		ActingFor: "tester", AcquiredAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}

	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(stateDir, state.LeasesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// leaseRig is a rig whose node runs on the broker's fake clock with a process table.
type leaseRig struct {
	*rig
	table *proctest.Table
	run   *running
}

func newLeaseRig(t *testing.T, tweak func(*Options)) *leaseRig {
	t.Helper()

	return newLeaseRigOn(t, newRig(t), tweak)
}

// newLeaseRigOn starts the lease rig's node on a rig the caller prepared, such as one whose intercept
// must already see the node's registration.
func newLeaseRigOn(t *testing.T, r *rig, tweak func(*Options)) *leaseRig {
	t.Helper()

	table := proctest.New(record.PlatformDarwin, 1)
	table.Add(4242, proctest.Process{Start: holderStart})

	opts := r.options()
	opts.Clock = r.clock
	opts.WakeGap = 10 * time.Second
	opts.Checker = &liveness.Checker{Procs: table, Local: testMachine}
	opts.Warn = func(msg string) { t.Log("node warning: " + msg) }

	if tweak != nil {
		tweak(&opts)
	}

	return &leaseRig{rig: r, table: table, run: r.start(opts)}
}

// claim submits a claim for the path and waits for the reply.
func (lr *leaseRig) claim(anchor, path, kind string) (rep handoff.Reply, id string) {
	lr.t.Helper()

	plantAnchor(lr.t, lr.state, anchor, path)

	body, err := json.Marshal(handoff.ClaimBody{
		Reach: wire.ReachUser, Entries: []repoid.Name{repoid.New([]string{testRoot}, strings.TrimPrefix(path, "/"), false, false)},
		Derivation: 1, Anchor: handoff.Anchor{ID: anchor, Keys: []keys.Key{{Path: path}}, Covered: []keys.Key{{Path: path}}, Created: true},
		Holder: holderEnv(kind), ActingFor: "tester",
	})
	if err != nil {
		lr.t.Fatal(err)
	}

	id = state.NewID()

	return lr.submit(handoff.Request{ID: id, Kind: handoff.KindClaim, CreatedAt: time.Now(), Body: body}), id
}

func (lr *leaseRig) submit(req handoff.Request) handoff.Reply {
	lr.t.Helper()

	if err := handoff.Submit(lr.state, req); err != nil {
		lr.t.Fatal(err)
	}

	var rep handoff.Reply

	eventually(lr.t, "the node to answer "+req.Kind, func() bool {
		got, err := handoff.ReadReply(lr.state, req.ID)
		rep = got

		return err == nil
	})

	handoff.Consume(lr.state, req.ID)

	return rep
}

func grantedLease(t *testing.T, rep handoff.Reply) wire.Lease {
	t.Helper()

	if rep.Error != nil {
		t.Fatalf("reply error: %+v", rep.Error)
	}

	var res wire.Result
	if err := json.Unmarshal(rep.Body, &res); err != nil || res.Code != wire.CodeGranted {
		t.Fatalf("claim result = %+v, %v", res, err)
	}

	return *res.Lease
}

func (lr *leaseRig) notices() []*record.Notice {
	lr.t.Helper()

	names, err := state.ListNames(lr.state, state.NoticesDir)
	if err != nil {
		lr.t.Fatal(err)
	}

	var out []*record.Notice

	for _, name := range names {
		data, err := state.ReadName(lr.state, state.NoticesDir, name)
		if err != nil {
			continue
		}

		if n, err := record.DecodeNotice(data); err == nil {
			out = append(out, n)
		}
	}

	return out
}

func (lr *leaseRig) waitNotice(substr string) *record.Notice {
	lr.t.Helper()

	var found *record.Notice

	eventually(lr.t, "a notice containing "+substr, func() bool {
		for _, n := range lr.notices() {
			if strings.Contains(n.Detail, substr) {
				found = n

				return true
			}
		}

		return false
	})

	return found
}

// blockRenewals makes the broker refuse renewals, so the node cannot renew.
func (lr *leaseRig) blockRenewals(block bool) {
	if !block {
		lr.intercept.Store(nil)

		return
	}

	f := func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == wire.PathRenew {
			http.Error(w, "down", http.StatusServiceUnavailable)

			return true
		}

		return false
	}

	lr.intercept.Store(&f)
}

func TestNodeRenewsEveryHoldersLeasesInOneHeartbeat(t *testing.T) {
	var (
		mu    sync.Mutex
		sizes []int
	)

	lr := newLeaseRig(t, nil)

	f := func(_ http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == wire.PathRenew {
			data, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(strings.NewReader(string(data)))

			var rn wire.Renew
			_ = json.Unmarshal(data, &rn)

			mu.Lock()
			sizes = append(sizes, len(rn.Items))
			mu.Unlock()
		}

		return false
	}
	lr.intercept.Store(&f)

	first := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))
	grantedLease(t, mustReply(lr.claim(anchorB, "/work/b", record.LivenessProcess)))

	eventually(t, "a renewal that covers both leases at once", func() bool {
		mu.Lock()
		defer mu.Unlock()

		for _, n := range sizes {
			if n == 2 {
				return true
			}
		}

		return false
	})

	before := lr.b.Leases()[0].ExpiresMillis
	lr.clock.Advance(4 * time.Minute)

	eventually(t, "the renewal to move the expiry", func() bool {
		for _, l := range lr.b.Leases() {
			if l.ID == first.ID && l.ExpiresMillis > before {
				return true
			}
		}

		return false
	})
}

func mustReply(rep handoff.Reply, _ string) handoff.Reply { return rep }

func TestDeadHolderEndsItsLeasesAtOnce(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	if len(lr.b.Leases()) != 1 {
		t.Fatal("the claim must be granted")
	}

	lr.table.Remove(4242)

	eventually(t, "the broker to end the dead holder's lease", func() bool { return len(lr.b.Leases()) == 0 })

	eventually(t, "the sidecar to go with the lease", func() bool {
		_, err := handoff.ReadSidecar(lr.state, granted.ID)

		return err != nil
	})
}

func TestUnknownLivenessKeepsRenewingAndShowsUnknown(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", "mystery-kind")))

	lr.table.Remove(4242)

	eventually(t, "the broker to show the holder as unknown", func() bool {
		ls := lr.b.Leases()

		return len(ls) == 1 && ls[0].Liveness == wire.LiveUnknown
	})

	lr.clock.Advance(4 * time.Minute)
	time.Sleep(150 * time.Millisecond)

	if got := lr.b.Leases(); len(got) != 1 {
		t.Fatalf("an unknown holder keeps its lease: %+v", got)
	}
}

func TestLapsedDuringSleepIsRevivedWithOneNoticePerLapse(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	for round := 1; round <= 2; round++ {
		lr.blockRenewals(true)
		lr.clock.Sleep(2 * time.Minute)
		lr.clock.Advance(4 * time.Minute)
		lr.b.Sweep()

		if got := lr.b.Leases(); len(got) != 1 || got[0].State != wire.StateLapsed {
			t.Fatalf("round %d: the lease must lapse: %+v", round, got)
		}

		lr.blockRenewals(false)

		eventually(t, "the lease to be revived", func() bool {
			ls := lr.b.Leases()

			return len(ls) == 1 && ls[0].State == wire.StateLive
		})
	}

	revived := 0

	eventually(t, "two revival notices, one per lapse", func() bool {
		revived = 0

		for _, n := range lr.notices() {
			if strings.Contains(n.Detail, "unprotected") && strings.Contains(n.Detail, "nobody claimed") {
				revived++
			}
		}

		return revived == 2
	})
}

func TestStaleLapseEventDoesNotOverrideARevival(t *testing.T) {
	bk := newBook()

	bk.apply(wire.Lease{ID: "L", State: wire.StateLive, Revision: 3}, 5, true)

	if _, kept := bk.apply(wire.Lease{ID: "L", State: wire.StateLapsed, Revision: 2}, 0, false); kept {
		t.Fatal("an older revision must not replace a newer one")
	}

	if st, _ := bk.state("L"); st != wire.StateLive {
		t.Fatalf("state = %s", st)
	}

	if prev, kept := bk.apply(wire.Lease{ID: "L", State: wire.StateLapsed, Revision: 4}, 0, false); !kept || prev != wire.StateLive {
		t.Fatalf("a newer revision replaces: prev %q kept %v", prev, kept)
	}
}

func TestLostToAnotherHolderWritesANotice(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	lr.blockRenewals(true)
	lr.clock.Advance(7 * time.Minute)
	lr.b.Sweep()

	// A second user claim on the same name takes the lapsed lease.
	other := newOtherNode(t, lr)
	grantedLease(t, mustReply(other.claim(anchorZ, "/work/a", record.LivenessProcess)))

	lr.blockRenewals(false)

	n := lr.waitNotice("lost to")
	if !strings.Contains(n.Detail, anchorA) && !strings.Contains(n.Detail, "your broker lease") {
		t.Fatalf("notice = %q", n.Detail)
	}
}

func newOtherNode(t *testing.T, lr *leaseRig) *leaseRig {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	opts := lr.options()
	opts.StateDir = filepath.Join(dir, "state")
	opts.Machine = record.Machine{Host: "studio", ID: "machine-2"}
	opts.Clock = lr.clock
	opts.Checker = &liveness.Checker{Procs: lr.table, Local: opts.Machine}

	other := &rig{t: t, b: lr.b, store: lr.store, clock: lr.clock, srv: lr.srv, state: opts.StateDir}

	return &leaseRig{rig: other, table: lr.table, run: other.start(opts)}
}

func TestNotFoundRenewalWritesALossNotice(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	// The broker loses the lease: a release by another channel, as a wiped store would.
	lr.blockRenewals(true)

	var res wire.Result

	id := lr.b.Leases()[0].ID
	forceRelease(t, lr, id, &res)

	lr.blockRenewals(false)

	lr.waitNotice("the broker had no record")
}

func forceRelease(t *testing.T, lr *leaseRig, id string, res *wire.Result) {
	t.Helper()

	sc, err := handoff.ReadSidecar(lr.state, id)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(wire.Release{
		Version: wire.Current, RequestID: "rel-" + id, ID: id, Holder: wireHolder(sc.Holder), Cause: wire.CauseHolderDead,
	})

	req, _ := http.NewRequest(http.MethodPost, lr.srv.URL+wire.PathRelease, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+lr.run.n.cl.key)
	req.Header.Set(wire.HeaderMachine, testMachine.ID)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(os.Getuid()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	_ = json.NewDecoder(resp.Body).Decode(res)
}

type anchorTest struct {
	Name   string
	Mutate func(t *testing.T, stateDir string)
}

var anchorTests = []anchorTest{
	{"released", func(t *testing.T, dir string) {
		if err := os.Remove(filepath.Join(dir, state.LeasesDir, anchorA+".json")); err != nil {
			t.Fatal(err)
		}
	}},
	{"narrowed by a V1 replace", func(t *testing.T, dir string) {
		plantAnchor(t, dir, anchorA, "/work/elsewhere")
	}},
}

func TestAnchorEndedOrNarrowedEndsTheLeaseAndItIsNeverRevived(t *testing.T) {
	for _, test := range anchorTests {
		t.Run(test.Name, func(t *testing.T) {
			lr := newLeaseRig(t, nil)
			grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

			test.Mutate(t, lr.state)

			eventually(t, "the broker to end the lease", func() bool { return len(lr.b.Leases()) == 0 })
			lr.waitNotice("ended because")

			lr.clock.Advance(20 * time.Minute)
			lr.b.Sweep()

			if len(lr.b.Leases()) != 0 {
				t.Fatal("an ended lease is never revived")
			}
		})
	}
}

func TestAnchorFollowedThroughAbsorbs(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	// A replace rewrote another lease to cover the anchor's paths and absorbed the anchor.
	plantAnchor(t, lr.state, anchorM, "/work")
	absorbing(t, lr.state, anchorM, anchorA)

	if err := os.Remove(filepath.Join(lr.state, state.LeasesDir, anchorA+".json")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	if len(lr.b.Leases()) != 1 {
		t.Fatal("an anchor absorbed into a lease that still covers the paths keeps the lease")
	}
}

func absorbing(t *testing.T, stateDir, id, absorbed string) {
	t.Helper()

	path := filepath.Join(stateDir, state.LeasesDir, id+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	raw["absorbs"] = []string{absorbed}

	out, _ := json.Marshal(raw)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSynchronousRenewReturnsTheNewExpiry(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	lr.clock.Advance(3 * time.Minute)

	body, _ := json.Marshal(handoff.RenewBody{IDs: []string{granted.ID}, Holder: holderEnv(record.LivenessProcess)})
	rep := lr.submit(handoff.Request{ID: state.NewID(), Kind: handoff.KindRenew, CreatedAt: time.Now(), Body: body})

	var out wire.Renewed
	if err := json.Unmarshal(rep.Body, &out); err != nil || len(out.Results) != 1 || out.Results[0].Code != wire.CodeRenewed {
		t.Fatalf("renew reply = %+v, %v", out, err)
	}

	if out.Results[0].Lease.ExpiresMillis <= granted.ExpiresMillis {
		t.Fatalf("expiry %d did not move past %d", out.Results[0].Lease.ExpiresMillis, granted.ExpiresMillis)
	}
}

func TestUnreadReplyBecomesANoticeAndARetryReturnsTheSameAnswer(t *testing.T) {
	lr := newLeaseRig(t, func(o *Options) { o.ReplyGrace = 50 * time.Millisecond })

	plantAnchor(t, lr.state, anchorA, "/work/a")

	body, _ := json.Marshal(handoff.ClaimBody{
		Reach: wire.ReachUser, Entries: []repoid.Name{repoid.New([]string{testRoot}, "work/a", false, false)}, Derivation: 1,
		Anchor: handoff.Anchor{ID: anchorA, Created: true}, Holder: holderEnv(record.LivenessProcess),
	})

	id := state.NewID()
	req := handoff.Request{ID: id, Kind: handoff.KindClaim, CreatedAt: time.Now(), Body: body}

	if err := handoff.Submit(lr.state, req); err != nil {
		t.Fatal(err)
	}

	lr.waitNotice("granted after nitipan stopped waiting")

	eventually(t, "the delivered reply to be consumed", func() bool {
		_, err := handoff.ReadReply(lr.state, id)

		return err != nil
	})

	before := lr.store.Data().Counter

	again := grantedLease(t, mustReply(lr.submit(req), id))

	if again.ID != id || lr.store.Data().Counter != before {
		t.Fatalf("a retry of the same request must replay the outcome: counter %d -> %d", before, lr.store.Data().Counter)
	}
}

func TestRequestsNeedTheRightPeerVersions(t *testing.T) {
	lr := newLeaseRig(t, nil)

	t.Run("old CLI with new node", func(t *testing.T) {
		req := handoff.Request{ID: state.NewID(), Kind: KindPing, CreatedAt: time.Now()}
		if err := handoff.SubmitVersion(lr.state, req, 1); err != nil {
			t.Fatal(err)
		}

		eventually(t, "the node to answer the old request", func() bool {
			rep, err := handoff.ReadReply(lr.state, req.ID)

			return err == nil && rep.Error == nil
		})
	})

	t.Run("new CLI with old node", func(t *testing.T) {
		old := handoff.Presence{RequestVersion: 1, Kinds: []string{KindPing}}

		for _, kind := range []string{handoff.KindClaim, handoff.KindRenew} {
			if err := old.Supports(kind, handoff.RequestVersion); err == nil || !strings.Contains(err.Error(), "node too old") {
				t.Fatalf("Supports(%s) = %v, want node too old", kind, err)
			}
		}
	})

	t.Run("old CLI cannot have written a discovery request", func(t *testing.T) {
		body, _ := json.Marshal(wire.WhoIs{Name: "guardian"})
		req := handoff.Request{ID: state.NewID(), Kind: handoff.KindWhoIs, CreatedAt: time.Now(), Body: body}

		if err := handoff.SubmitVersion(lr.state, req, 2); err != nil {
			t.Fatal(err)
		}

		eventually(t, "the node to refuse the request", func() bool {
			rep, err := handoff.ReadReply(lr.state, req.ID)

			return err == nil && rep.Error != nil && rep.Error.Code == handoff.CodeTooOld
		})
	})

	t.Run("new CLI with a node that predates discovery", func(t *testing.T) {
		old := handoff.Presence{RequestVersion: 2, Kinds: []string{KindPing, handoff.KindClaim, handoff.KindList}}

		for _, kind := range []string{handoff.KindWhoHolds, handoff.KindWhoIs} {
			if err := old.Supports(kind, handoff.RequestVersion); err == nil || !strings.Contains(err.Error(), "node too old") {
				t.Fatalf("Supports(%s) = %v, want node too old", kind, err)
			}
		}
	})

	t.Run("new node with a broker that predates discovery", func(t *testing.T) {
		f := func(w http.ResponseWriter, req *http.Request) bool {
			if req.URL.Path == wire.PathWhoHolds {
				http.NotFound(w, req)

				return true
			}

			return false
		}
		lr.intercept.Store(&f)

		defer lr.intercept.Store(nil)

		body, _ := json.Marshal(wire.WhoHolds{Resource: "cpu:heavy"})

		rep := lr.submit(handoff.Request{ID: state.NewID(), Kind: handoff.KindWhoHolds, CreatedAt: time.Now(), Body: body})
		if rep.Error == nil || rep.Error.Code != handoff.CodeBrokerTooOld {
			t.Fatalf("reply error = %+v, want broker too old", rep.Error)
		}
	})

	t.Run("new node with old broker", func(t *testing.T) {
		var count atomic.Int32

		f := func(w http.ResponseWriter, req *http.Request) bool {
			if req.URL.Path == wire.PathClaim {
				count.Add(1)
				http.NotFound(w, req)

				return true
			}

			return false
		}
		lr.intercept.Store(&f)

		plantAnchor(t, lr.state, anchorA, "/work/a")

		body, _ := json.Marshal(handoff.ClaimBody{Reach: wire.ReachUser, Entries: []repoid.Name{repoid.New([]string{testRoot}, "x", false, false)},
			Derivation: 1, Anchor: handoff.Anchor{ID: anchorA}, Holder: holderEnv(record.LivenessProcess)})

		rep := lr.submit(handoff.Request{ID: state.NewID(), Kind: handoff.KindClaim, CreatedAt: time.Now(), Body: body})
		if rep.Error == nil || rep.Error.Code != handoff.CodeBrokerTooOld {
			t.Fatalf("reply error = %+v, want broker too old", rep.Error)
		}
	})
}

func TestDeadlineFileSaysAtRiskThenLapsed(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	hk := handoff.HolderKey(holderEnv(record.LivenessProcess).Holder, record.LivenessProcess)

	var d handoff.Deadlines

	eventually(t, "the deadline file", func() bool {
		got, err := handoff.ReadDeadlines(lr.state, hk)
		d = got

		return err == nil && len(got.Leases) == 1
	})

	if lines := d.Risk(time.Now()); len(lines) != 0 {
		t.Fatalf("a connected node with time left reports no risk: %v", lines)
	}

	d.Connected = false

	at := d.Leases[0].LapsesAt

	if lines := d.Risk(at.Add(-time.Minute)); len(lines) != 1 || !strings.Contains(lines[0], "at risk") {
		t.Fatalf("disconnected before the deadline: %v", lines)
	}

	if lines := d.Risk(at.Add(time.Second)); len(lines) != 1 || !strings.Contains(lines[0], "lapsed") {
		t.Fatalf("past the deadline: %v", lines)
	}
}

func TestDeadlineFileWrittenDisconnected(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	hk := handoff.HolderKey(holderEnv(record.LivenessProcess).Holder, record.LivenessProcess)

	lr.srv.Close()

	eventually(t, "a disconnected deadline file", func() bool {
		d, err := handoff.ReadDeadlines(lr.state, hk)

		return err == nil && !d.Connected && len(d.Leases) == 1
	})
}

func TestReleaseOfAnotherSessionsLeaseLeavesItAlone(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	body, _ := json.Marshal(handoff.ReleaseBody{ID: granted.ID, Holder: holderEnvPID(4243, record.LivenessProcess)})
	rep := lr.submit(handoff.Request{ID: state.NewID(), Kind: handoff.KindRelease, CreatedAt: time.Now(), Body: body})

	var out handoff.ReleaseReply
	if err := json.Unmarshal(rep.Body, &out); err != nil || out.Result.Code != wire.CodeNotFound || len(out.Sidecars) != 0 {
		t.Fatalf("reply = %+v, %v", out, err)
	}

	if _, err := handoff.ReadSidecar(lr.state, granted.ID); err != nil {
		t.Fatalf("another session's release must not delete the sidecar: %v", err)
	}

	before := lr.b.Leases()[0].ExpiresMillis
	lr.clock.Advance(4 * time.Minute)

	eventually(t, "the lease to keep renewing", func() bool {
		ls := lr.b.Leases()

		return len(ls) == 1 && ls[0].ExpiresMillis > before
	})
}

func TestRepeatClaimReturnsTheSameLeaseWithoutAGhostSidecar(t *testing.T) {
	lr := newLeaseRig(t, nil)
	first := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))
	second := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	if second.ID != first.ID {
		t.Fatalf("an exact repeat returns the held lease: %s vs %s", second.ID, first.ID)
	}

	time.Sleep(300 * time.Millisecond)

	scs, _, err := handoff.Sidecars(lr.state)
	if err != nil || len(scs) != 1 || scs[0].ID != first.ID {
		t.Fatalf("sidecars = %+v, %v", scs, err)
	}

	if ns := lr.notices(); len(ns) != 0 {
		t.Fatalf("a repeat claim sends no notice: %+v", ns[0])
	}
}

func TestWrittenOffLeaseWhoseRenewalReachedTheBrokerIsRestored(t *testing.T) {
	lr := newLeaseRig(t, nil)
	grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	// The broker applies each renewal, and the node never hears the answer.
	f := func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != wire.PathRenew {
			return false
		}

		lr.b.Handler().ServeHTTP(httptest.NewRecorder(), req)
		http.Error(w, "lost", http.StatusServiceUnavailable)

		return true
	}
	lr.intercept.Store(&f)

	lr.clock.Advance(4 * time.Minute)
	time.Sleep(200 * time.Millisecond)
	lr.clock.Advance(2 * time.Minute)

	hk := handoff.HolderKey(holderEnv(record.LivenessProcess).Holder, record.LivenessProcess)

	eventually(t, "the node to write the lease off", func() bool {
		d, err := handoff.ReadDeadlines(lr.state, hk)

		return err == nil && len(d.Leases) == 1 && !d.Leases[0].LapsesAt.After(time.Now())
	})

	lr.blockRenewals(false)

	lr.waitNotice("restored")
}

func TestVersionOneRequestsOfLaterKindsAreRefused(t *testing.T) {
	lr := newLeaseRig(t, nil)

	req := handoff.Request{ID: state.NewID(), Kind: handoff.KindList, CreatedAt: time.Now()}
	if err := handoff.SubmitVersion(lr.state, req, 1); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the node to refuse the v1 list", func() bool {
		rep, err := handoff.ReadReply(lr.state, req.ID)

		return err == nil && rep.Error != nil && rep.Error.Code == handoff.CodeTooOld
	})
}
