package broker

import (
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

func micros(t time.Time) uint64 { return uint64(t.UnixMicro()) }

type tokenFloorTest struct {
	Name string

	// Back moves the wall clock this far before the second grant.
	Back time.Duration

	// Ahead moves the wall clock this far forward before the second grant.
	Ahead time.Duration

	// Want computes the second grant's token from the first.
	Want func(first uint64, now time.Time) uint64
}

var tokenFloorTests = []tokenFloorTest{
	{"the clock is far ahead of the counter", 0, time.Hour, func(_ uint64, now time.Time) uint64 { return micros(now) }},
	{"the clock stands still", 0, 0, func(first uint64, _ time.Time) uint64 { return first + 1 }},
	{"the counter is ahead of the clock", time.Hour, 0, func(first uint64, _ time.Time) uint64 { return first + 1 }},
}

func TestTokenIsTheLargerOfTheNextCounterAndTheClock(t *testing.T) {
	for _, test := range tokenFloorTests {
		t.Run(test.Name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.twoNodes()

			first := h.claim(laptop, "L1", wire.ReachUser, name("a"), "a").Lease.Token
			if first != micros(wallStart) {
				t.Fatalf("the first token = %d, want the clock %d", first, micros(wallStart))
			}

			now := wallStart.Add(test.Ahead - test.Back)
			h.wall.Set(now)

			second := h.claim(laptop, "L2", wire.ReachUser, name("b"), "a").Lease.Token
			if want := test.Want(first, now); second != want {
				t.Fatalf("the second token = %d, want %d", second, want)
			}
		})
	}
}

func TestGrantReplyAndStoredTokenAgree(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	res := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	view := h.b.Leases()
	if len(view) != 1 || view[0].Token != res.Lease.Token || h.store.Data().Counter != res.Lease.Token {
		t.Fatalf("reply %d, view %+v, counter %d", res.Lease.Token, view, h.store.Data().Counter)
	}

	h.restart(nil)

	if got := h.b.Leases(); len(got) != 1 || got[0].Token != res.Lease.Token {
		t.Fatalf("after a restart from the file the grant wrote: %+v", got)
	}
}

func TestRestoredStoreNeverRevivesAndIssuesAboveEarlierTokens(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	backup, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}

	issued := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a").Lease.Token

	h.restart(backup)

	if got := h.renew(laptop, "L1"); got[0].Code != wire.CodeNotFound || got[0].Lease != nil {
		t.Fatalf("a renewal of a lease the restored broker does not know must be lost: %+v", got[0])
	}

	if len(h.b.Leases()) != 0 {
		t.Fatalf("the restore must not revive anything: %+v", h.b.Leases())
	}

	h.wall.Set(wallStart.Add(time.Minute))

	if next := h.claim(laptop, "L2", wire.ReachUser, name("svc"), "a").Lease.Token; next <= issued {
		t.Fatalf("a token after the restore = %d, not above the %d issued before it", next, issued)
	}
}

type fenceReportTest struct {
	Name string

	// Token computes the reported token from the wall clock and the counter.
	Token func(now time.Time, counter uint64) uint64

	Verdict string

	// Raised is whether the counter should become the reported token.
	Raised bool
}

var fenceReportTests = []fenceReportTest{
	{"the counter itself", func(_ time.Time, c uint64) uint64 { return c }, wire.FenceKnown, false},
	{"below the counter", func(_ time.Time, c uint64) uint64 { return c - 1 }, wire.FenceKnown, false},
	{"just above the counter", func(_ time.Time, c uint64) uint64 { return c + 1 }, wire.FenceRaised, true},
	{"an hour past the clock", func(now time.Time, _ uint64) uint64 { return micros(now.Add(time.Hour)) }, wire.FenceRaised, true},
	{"exactly at the bound", func(now time.Time, _ uint64) uint64 { return micros(now.Add(FenceMargin)) }, wire.FenceRaised, true},
	{"one microsecond past the bound", func(now time.Time, _ uint64) uint64 { return micros(now.Add(FenceMargin)) + 1 }, wire.FenceUnusable, false},
	{"a garbage value near the top", func(time.Time, uint64) uint64 { return ^uint64(0) - 1 }, wire.FenceUnusable, false},
}

func (h *harness) fence(token uint64) (verdict wire.FenceVerdict, status int) {
	h.t.Helper()

	status = h.call(laptop, wire.PathFence, wire.FenceReport{Version: wire.Current, Token: token}, &verdict)

	return verdict, status
}

func TestFenceReportVerdicts(t *testing.T) {
	for _, test := range fenceReportTests {
		t.Run(test.Name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.twoNodes()

			counter := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a").Lease.Token

			before, err := os.ReadFile(h.path)
			if err != nil {
				t.Fatal(err)
			}

			token := test.Token(wallStart, counter)

			got, status := h.fence(token)
			if status != http.StatusOK || got.Verdict != test.Verdict {
				t.Fatalf("report of %d: status %d, verdict %+v, want %s", token, status, got, test.Verdict)
			}

			want := counter
			if test.Raised {
				want = token
			}

			if got.Counter != want || h.store.Data().Counter != want {
				t.Fatalf("counter after the report = %d (store %d), want %d", got.Counter, h.store.Data().Counter, want)
			}

			after, err := os.ReadFile(h.path)
			if err != nil {
				t.Fatal(err)
			}

			if !test.Raised && string(before) != string(after) {
				t.Fatal("a report that raises nothing must not write the store")
			}

			if test.Raised {
				h.restart(nil)

				if h.store.Data().Counter != token {
					t.Fatalf("a raised counter must survive a restart: %d", h.store.Data().Counter)
				}
			}

			if next := h.claim(laptop, "L2", wire.ReachUser, name("other"), "a").Lease.Token; next <= want {
				t.Fatalf("the next token %d must exceed the counter %d", next, want)
			}
		})
	}
}

func TestFenceReportWithoutATokenIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	if _, status := h.fence(0); status != http.StatusBadRequest {
		t.Fatalf("status = %d", status)
	}
}

func TestClaimFailsClosedWhenTheCounterCannotAdvance(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	h.b.mu.Lock()
	h.b.g.counter = math.MaxUint64
	h.b.mu.Unlock()

	status := h.call(laptop, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "anchor-L1",
		Holder: holder("a"),
	}, nil)
	if status != http.StatusServiceUnavailable || len(h.b.Leases()) != 0 {
		t.Fatalf("status %d, leases %+v: a claim must not wrap the counter", status, h.b.Leases())
	}
}
