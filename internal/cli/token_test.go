package cli

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// tokenOf reads the token printed on a "token:" line of the grant output.
func tokenOf(t *testing.T, out string) uint64 {
	t.Helper()

	_, rest, ok := strings.Cut(out, "token:")
	if !ok {
		t.Fatalf("no token in %q", out)
	}

	token, err := strconv.ParseUint(strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0]), 10, 64)
	if err != nil || token == 0 {
		t.Fatalf("no token in %q: %v", out, err)
	}

	return token
}

func TestGrantPrintsTheTokenAndKeepsItInTheSidecar(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "  token:  ")

	token := tokenOf(t, res.Stdout)

	sc, err := handoff.ReadSidecar(tm.m1.w.state, leaseIDIn(t, res.Stdout))
	if err != nil {
		t.Fatal(err)
	}

	if sc.Token != token || sc.Version != handoff.SidecarVersion {
		t.Fatalf("sidecar = %+v, want token %d at version %v", sc, token, handoff.SidecarVersion)
	}

	if stored := tm.f.store.Data().Counter; stored != token {
		t.Fatalf("the printed token %d is not the stored counter %d", token, stored)
	}
}

func TestRenewPrintsTheToken(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	renew := tm.m1.w.run(tm.c1, "renew")
	wantStatus(t, renew, exitcode.OK)
	wantContains(t, "stdout", renew.Stdout, "renewed "+leaseIDIn(t, res.Stdout), "; token "+strconv.FormatUint(tokenOf(t, res.Stdout), 10))
}

// saveToken stands in for the repair the CLI makes when a node that predates the field wrote the sidecar.
func TestSaveTokenFillsAMissingTokenAndNeverCreatesASidecar(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	id := leaseIDIn(t, res.Stdout)
	token := tokenOf(t, res.Stdout)
	w := tm.m1.w

	sc, err := handoff.ReadSidecar(w.state, id)
	if err != nil {
		t.Fatal(err)
	}

	sc.Token = 0
	rewriteSidecar(t, w, sc)

	a := &app{deps: w.deps(tm.c1, &bytes.Buffer{}, &bytes.Buffer{}).withDefaults()}
	a.saveToken(w.state, id, token)

	if got, err := handoff.ReadSidecar(w.state, id); err != nil || got.Token != token {
		t.Fatalf("sidecar after saveToken = %+v, %v, want token %d", got, err, token)
	}

	a.saveToken(w.state, "01H0000000000000000000000Z", token)

	if _, err := handoff.ReadSidecar(w.state, "01H0000000000000000000000Z"); err == nil {
		t.Fatal("saveToken must not create a sidecar")
	}
}

func rewriteSidecar(t *testing.T, w *world, sc handoff.Sidecar) {
	t.Helper()

	dir, err := state.Open(w.state, w.deps(caller{}, &bytes.Buffer{}, &bytes.Buffer{}).StateOptions)
	if err != nil {
		t.Fatal(err)
	}

	l, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Unlock() }()

	if err := handoff.WriteSidecar(l, sc); err != nil {
		t.Fatal(err)
	}
}

func TestListShowsTheTokenInTextAndJSONAndFromTheSidecarWhenTheNodeIsDown(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	token := tokenOf(t, res.Stdout)
	want := strconv.FormatUint(token, 10)

	text := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stdout", text.Stdout, "broker leases:", "  token:  "+want)

	doc := tm.m1.w.listJSON(tm.c1)

	leases, _ := doc["broker_leases"].([]any)
	if len(leases) != 1 || doc["schema_version"] != "1.6" {
		t.Fatalf("list --json = %v", doc)
	}

	one, _ := leases[0].(map[string]any)
	if got, _ := one["token"].(float64); uint64(got) != token {
		t.Fatalf("json token = %v, want %d", one["token"], token)
	}

	tm.m1.stop()

	offline := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stdout", offline.Stdout, "broker leases:", "unknown", "  token:  "+want)
}

type fenceReportCase struct {
	Name    string
	Offset  func(counter uint64) uint64
	Verdict string
}

var fenceReportCases = []fenceReportCase{
	{"a token the broker issued", func(c uint64) uint64 { return c }, wire.FenceKnown},
	{"a token past the counter", func(c uint64) uint64 { return c + 1000 }, wire.FenceRaised},
	{"a token past the bound", func(uint64) uint64 { return ^uint64(0) - 1 }, wire.FenceUnusable},
}

func TestFenceReportTravelsThroughTheNodeToTheBroker(t *testing.T) {
	for _, test := range fenceReportCases {
		t.Run(test.Name, func(t *testing.T) {
			tm := newTwoMachines(t)
			wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

			counter := tm.f.store.Data().Counter

			a := &app{deps: tm.m1.w.deps(tm.c1, &bytes.Buffer{}, &bytes.Buffer{}).withDefaults()}

			got, err := a.reportFence(test.Offset(counter))
			if err != nil || got.Verdict != test.Verdict {
				t.Fatalf("verdict = %+v, %v, want %s", got, err, test.Verdict)
			}

			if test.Verdict == wire.FenceRaised && tm.f.store.Data().Counter != test.Offset(counter) {
				t.Fatalf("the broker counter = %d", tm.f.store.Data().Counter)
			}
		})
	}
}

func TestFenceReportToABrokerWithoutTheCallSaysTheBrokerIsTooOld(t *testing.T) {
	tm := newTwoMachines(t)

	path := wire.PathFence
	tm.f.missing.Store(&path)

	a := &app{deps: tm.m1.w.deps(tm.c1, &bytes.Buffer{}, &bytes.Buffer{}).withDefaults()}

	if _, err := a.reportFence(5); !errors.Is(err, ErrBrokerTooOld) {
		t.Fatalf("err = %v, want broker too old", err)
	}
}
