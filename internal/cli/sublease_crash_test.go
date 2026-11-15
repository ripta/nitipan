package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

var errKilled = errors.New("killed")

// crashedGrant is a grant that stopped at one of its hooks. The request is in whatever state that
// point leaves.
type crashedGrant struct {
	tr        *tree
	requestID string
}

func crashGrantAt(t *testing.T, hooks lease.Hooks) *crashedGrant {
	t.Helper()

	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	tr.w.hooks = hooks
	wantStatus(t, tr.w.run(tr.parent, "grant", id), exitcode.Error)
	tr.w.hooks = lease.Hooks{}

	return &crashedGrant{tr: tr, requestID: id}
}

func (g *crashedGrant) requestState(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(g.tr.w.requestPath(g.requestID))
	if err != nil {
		t.Fatalf("the request is gone: %v", err)
	}

	req, err := record.DecodeRequest(data)
	if err != nil {
		t.Fatal(err)
	}

	return req.State
}

func (g *crashedGrant) wantSettled(t *testing.T) {
	t.Helper()

	w := g.tr.w

	if names := w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests = %v, want none pending", names)
	}

	for _, cause := range []string{record.CauseRequestGranted, record.CauseRequestDeclined, record.CauseRequestLapsed} {
		if names := w.noticesOf(cause); len(names) != 0 {
			t.Errorf("notices = %v, want all delivered", names)
		}
	}
}

func TestACrashAfterTheGrantingMarkLeavesALapsedNoticeAndNoLease(t *testing.T) {
	g := crashGrantAt(t, lease.Hooks{AfterGrantMark: func() error { return errKilled }})
	w := g.tr.w

	if got := g.requestState(t); got != record.RequestGranting {
		t.Fatalf("request state = %q, want %q", got, record.RequestGranting)
	}

	if w.leaseByID(g.requestID) != nil {
		t.Fatal("the crash happened after the publish")
	}

	// Another holder's call finishes the repair. It is not the requester, so it learns nothing.
	res := w.run(g.tr.child, "acquire", "--checkout", g.tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "notice")

	if names := w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests = %v, want the granting request repaired away", names)
	}

	res = w.run(g.tr.third, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "your request "+g.requestID, "interrupted", "Request again")
	wantAbsent(t, "stderr", res.Stderr, "declined", "was granted")

	g.wantSettled(t)

	if w.leaseByID(g.requestID) != nil {
		t.Error("the lease ID was reused")
	}

	wantStatus(t, w.run(g.tr.parent, "grant", g.requestID), exitcode.NoSuchLease)
}

func TestACrashAfterTheSubLeaseIsPublishedLeavesOneGrantedNotice(t *testing.T) {
	for _, hooks := range []struct {
		Name  string
		Hooks lease.Hooks
	}{
		{"before its notice", lease.Hooks{AfterGrantPublish: func() error { return errKilled }}},
		{"after its notice", lease.Hooks{AfterGrantNotice: func() error { return errKilled }}},
	} {
		t.Run(hooks.Name, func(t *testing.T) {
			g := crashGrantAt(t, hooks.Hooks)
			w := g.tr.w

			if w.leaseByID(g.requestID) == nil {
				t.Fatal("the sub-lease was not published")
			}

			res := w.run(g.tr.third, "release", "--all")
			wantStatus(t, res, exitcode.OK)

			again := w.run(g.tr.third, "list")
			wantStatus(t, again, exitcode.OK)

			if got := strings.Count(res.Stderr+again.Stderr, "was granted"); got != 1 {
				t.Errorf("the requester was told %d times, want once\n%s\n%s", got, res.Stderr, again.Stderr)
			}

			wantAbsent(t, "stderr", res.Stderr, "declined", "interrupted")
			g.wantSettled(t)
		})
	}
}

func TestACrashAfterThePublishIsRepairedEvenIfTheParentReleasedInBetween(t *testing.T) {
	t.Run("the parent's own release is the next locked call", func(t *testing.T) {
		g := crashGrantAt(t, lease.Hooks{AfterGrantPublish: func() error { return errKilled }})
		w := g.tr.w

		wantStatus(t, w.run(g.tr.parent, "release", g.tr.parentID), exitcode.OK)

		if names := w.noticesOf(record.CauseRequestGranted); len(names) != 1 {
			t.Errorf("notices = %v, want one granted notice", names)
		}

		res := w.run(g.tr.third, "release", "--all")
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stderr", res.Stderr, "was granted")
		wantAbsent(t, "stderr", res.Stderr, "declined", "interrupted")
		g.wantSettled(t)
	})

	t.Run("an older binary removes the parent before any repair", func(t *testing.T) {
		g := crashGrantAt(t, lease.Hooks{AfterGrantPublish: func() error { return errKilled }})
		w := g.tr.w

		if err := os.Remove(filepath.Join(w.leasesDir(), g.tr.parentID+".json")); err != nil {
			t.Fatal(err)
		}

		res := w.run(g.tr.third, "release", "--all")
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stderr", res.Stderr, "was granted")
		wantAbsent(t, "stderr", res.Stderr, "declined", "interrupted")
		g.wantSettled(t)
	})
}

func TestAnOlderBinaryRemovingTheSubLeaseBeforeTheRepairLeavesALapsedNotice(t *testing.T) {
	g := crashGrantAt(t, lease.Hooks{AfterGrantPublish: func() error { return errKilled }})
	w := g.tr.w

	if err := os.Remove(filepath.Join(w.leasesDir(), g.requestID+".json")); err != nil {
		t.Fatal(err)
	}

	res := w.run(g.tr.third, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "interrupted", "Request again")
	wantAbsent(t, "stderr", res.Stderr, "declined", "was granted")
	g.wantSettled(t)

	if w.leaseByID(g.requestID) != nil {
		t.Error("the lease ID was reused")
	}
}

func TestAGrantingRequestIsNeverGrantedTwiceAndNeverReportedAsDeclined(t *testing.T) {
	g := crashGrantAt(t, lease.Hooks{AfterGrantMark: func() error { return errKilled }})
	w := g.tr.w

	// The repair at the start of the parent's own call removes it before grant can look.
	res := w.run(g.tr.parent, "grant", g.requestID)
	wantStatus(t, res, exitcode.NoSuchLease)

	if w.leaseByID(g.requestID) != nil {
		t.Error("an interrupted grant was granted later")
	}

	res = w.run(g.tr.third, "release", "--all")
	wantAbsent(t, "stderr", res.Stderr, "declined")
}

func TestANoticeIsNamedAfterItsTargetAndCause(t *testing.T) {
	g := crashGrantAt(t, lease.Hooks{AfterGrantPublish: func() error { return errKilled }})
	w := g.tr.w

	wantStatus(t, w.run(g.tr.child, "acquire", "--checkout", g.tr.repo, "--scope", "src"), exitcode.OK)

	names := w.noticesOf(record.CauseRequestGranted)
	if len(names) != 1 || names[0] != g.requestID+"-request-granted.json" {
		t.Fatalf("notices = %v, want %s-request-granted.json", names, g.requestID)
	}
}
