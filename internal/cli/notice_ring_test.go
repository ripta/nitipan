package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// plantNotice publishes a notice addressed to a holder, as a writer of another proposal would.
func (w *world) plantNotice(cause, target, detail, kind string, holder record.Holder) {
	w.t.Helper()

	n := &record.Notice{
		Envelope: record.Envelope{
			Version:       record.ParentVersion,
			ID:            record.NoticeID(target, cause),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      kind,
			Machine:       w.machineOrDefault(),
			Holder:        holder,
		},
		Cause:  cause,
		Target: target,
		Detail: detail,
		At:     w.now.UTC(),
	}

	data, err := n.Encode()
	if err != nil {
		w.t.Fatal(err)
	}

	dir := filepath.Join(w.state, state.NoticesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		w.t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, n.ID+".json"), data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// ringContext runs the Claude turn hook and returns the context it adds, or the empty string when it
// stays quiet.
func (w *world) ringContext(c caller) string {
	w.t.Helper()

	res := w.run(c, "hook", "mail-ring", "--event", "UserPromptSubmit")
	wantStatus(w.t, res, exitcode.OK)

	if strings.TrimSpace(res.Stdout) == "" {
		return ""
	}

	var out struct {
		Specific struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}

	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		w.t.Fatalf("hook output %q: %v", res.Stdout, err)
	}

	return out.Specific.Context
}

func (tr *tree) childHolder() record.Holder { return tr.w.leaseByID(tr.childID).Holder }

type noticeRingTest struct {
	Name  string
	Setup func(*tree)
	Rings bool

	// Delivers is text that a locked `list` by the sub-lessee must then print. It proves that delivery
	// would have delivered a notice the ring leaves quiet.
	Delivers string
}

var noticeRingTests = []noticeRingTest{
	{
		Name:  "a recalled notice whose recall binds the sub-lease",
		Setup: func(tr *tree) { tr.recallOK("30m") },
		Rings: true,
	},
	{
		Name: "a notice delivery would deliver, whatever its cause",
		Setup: func(tr *tree) {
			tr.w.plantNotice(record.CauseRequestGranted, tr.childID, "granted", record.LivenessProcess, tr.childHolder())
		},
		Rings: true,
	},
	{
		Name: "a recall-cancelled notice, once the recall is off",
		Setup: func(tr *tree) {
			tr.recallOK("30m")
			wantStatus(tr.w.t, tr.w.run(tr.parent, "recall", "--cancel", tr.childID), exitcode.OK)
		},
		Rings: true,
	},
	{
		Name: "a parent-ended notice that waits on its repair is kept, so it stays quiet",
		Setup: func(tr *tree) {
			tr.w.plantNotice(record.CauseParentEnded, tr.childID, "parent ended", record.LivenessProcess, tr.childHolder())
		},
	},
	{
		Name: "a notice delivery would drop, such as a parent-ended notice for a sub-lease that is gone",
		Setup: func(tr *tree) {
			tr.w.plantNotice(record.CauseParentEnded, missingID, "parent ended", record.LivenessProcess, tr.childHolder())
		},
	},
	{
		Name: "a recalled notice whose recall is gone is dropped, so it stays quiet",
		Setup: func(tr *tree) {
			tr.w.plantNotice(record.CauseRecalled, tr.childID, "recalled", record.LivenessProcess, tr.childHolder())
		},
	},
	{
		Name: "a refused-claim notice that delivery would deliver never rings",
		Setup: func(tr *tree) {
			tr.w.plantNotice(record.CauseSharedClaimRefused, tr.childID, "a coworker's claim was refused", record.LivenessProcess,
				tr.childHolder())
		},
		Delivers: "a coworker's claim was refused",
	},
	{
		Name: "a recalled notice for a sub-lease past its deadline, which the unlocked read counts as ended",
		Setup: func(tr *tree) {
			tr.recallOK("30m")
			tr.w.pass(31 * time.Minute)
		},
	},
}

// The Claude turn hook is the path with no ledger and no waking, so it shows the whole verdict. A
// notice rings exactly when a locked call would deliver it, except the refused-claim notice. The control
// reads the refused-claim notice through `list`, which proves the exclusion is the ring's own.
func TestTheTurnHookRingsOnlyForNoticesDeliveryWouldDeliverAndNeverForARefusedClaim(t *testing.T) {
	for _, test := range noticeRingTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			test.Setup(tr)

			got := tr.w.ringContext(tr.child)

			if test.Rings {
				wantContains(t, "ring", got, "lease notice", "nitipan list")
			} else if got != "" {
				t.Errorf("the hook rang %q, want quiet", got)
			}

			if test.Delivers != "" {
				wantContains(t, "stderr", tr.w.run(tr.child, "list").Stderr, test.Delivers)
			}
		})
	}
}

func TestARingNamesListAndLeavesTheNoticeAndTheLedgerAlone(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	for range 2 {
		wantContains(t, "ring", tr.w.ringContext(tr.child), "1 lease notice", "nitipan list")
	}

	if tr.w.noticeOf(record.CauseRecalled) == nil {
		t.Fatal("a ring consumed the notice")
	}

	heard := tr.w.run(tr.child, "list")
	wantContains(t, "stderr", heard.Stderr, "was recalled")

	if got := tr.w.ringContext(tr.child); got != "" {
		t.Errorf("the hook still rings after the notice was read: %q", got)
	}
}

func TestAnIdentifiedHolderCallRingsForAPendingRequestUntilTheRequestIsAnswered(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	for range 2 {
		res := tr.w.poke(tr.parent)
		wantContains(t, "stderr", res.Stderr, "1 request for a lease you hold", "nitipan list")
	}

	wantStatus(t, tr.w.run(tr.parent, "grant", id), exitcode.OK)

	wantAbsent(t, "stderr", tr.w.poke(tr.parent).Stderr, "request for a lease you hold")
}

func TestAPendingRequestRingsTheHolderOfTheLeaseItTargetsAndNobodyElse(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	wantContains(t, "ring", tr.w.ringContext(tr.parent), "1 request for a lease you hold", "nitipan list")

	for name, c := range map[string]caller{"the requester": tr.third, "a sub-lessee": tr.child} {
		if got := tr.w.ringContext(c); got != "" {
			t.Errorf("%s was rung for a request on someone else's lease: %q", name, got)
		}
	}

	if !tr.w.hasRequest(id) {
		t.Fatal("a ring consumed the request")
	}

	tr.w.killAgent(tr.third)

	if got := tr.w.ringContext(tr.parent); got != "" {
		t.Errorf("a request from a dead requester rang: %q", got)
	}
}

// killAgent ends the agent process of a caller. claude() places the agent one PID below its shell.
func (w *world) killAgent(c caller) { w.kill(c.shellPID - 1) }

func (w *world) hasRequest(id string) bool {
	for _, name := range w.fileNames(state.RequestsDir) {
		if name == id+".json" {
			return true
		}
	}

	return false
}

// waiter starts a waiter for a session of the tree.
func (tr *tree) waiter(c caller, self int, ctx context.Context) *waiterRun {
	return startWaiter(tr.w, c, self, ctx)
}

// The one waiter run shows that the loop exits with the wake status and prints the ring. The ledger
// contract is proved at ringUnrung, where it needs no timers.
func TestAWaiterExitsWithTheWakeStatusAndTheRingForANotice(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	run := tr.waiter(tr.child, 8001, ctx)
	if !run.finished(5 * time.Second) {
		t.Fatal("the waiter never rang for the recalled notice")
	}

	if run.status != wakeStatus {
		t.Errorf("waiter status = %d, want %d", run.status, wakeStatus)
	}

	wantContains(t, "stderr", run.stderr.String(), "lease notice", "nitipan list")
	wantAbsent(t, "stderr", run.stderr.String(), "Finish, commit")

	if tr.w.noticeOf(record.CauseRecalled) == nil {
		t.Error("the waiter consumed the notice")
	}
}

// ringUnrung is what every waiter tick runs, so calling it is the waiter's ring with the timers left
// out. The registration is the one a waiter would have made.
func TestRingUnrungRingsOncePerNoticeKeyWithNoNodeAndAgainForAReplacedNotice(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	wantStatus(t, tr.w.run(tr.child, "mail", "status"), exitcode.OK)

	regs, _, err := mailbox.Registrations(tr.w.state)
	if err != nil || len(regs) != 1 {
		t.Fatalf("registrations = %v, %v", regs, err)
	}

	var stdout, stderr bytes.Buffer

	a := &app{deps: tr.w.deps(tr.child, &stdout, &stderr).withDefaults()}

	ring := func() bool {
		t.Helper()

		stderr.Reset()

		rung, err := a.ringUnrung(tr.w.state, regs[0])
		if err != nil {
			t.Fatal(err)
		}

		return rung
	}

	if !ring() {
		t.Fatal("no ring for a deliverable notice")
	}

	wantContains(t, "stderr", stderr.String(), "1 lease notice", "nitipan list")

	n := tr.w.noticeOf(record.CauseRecalled)
	key := lease.Ring{Kind: lease.RingNotice, ID: n.ID, At: n.At}.Key()

	if _, ok := mailbox.Rung(tr.w.state, regs[0].Identity)[key]; !ok {
		t.Errorf("ring ledger = %v, want the key %s", mailbox.Rung(tr.w.state, regs[0].Identity), key)
	}

	if ring() {
		t.Error("a re-armed waiter rang again for a notice it already rang for")
	}

	tr.w.pass(time.Minute)
	tr.recallOK("45m")

	if !ring() {
		t.Error("a replaced notice did not ring again")
	}

	if ring() {
		t.Error("the replaced notice rang twice")
	}

	if tr.w.noticeOf(record.CauseRecalled) == nil {
		t.Error("a ring consumed the notice")
	}
}

func TestCodexAndOpencodeWakePathsRingForNoticesAndOnlyTheWakingOnesClaim(t *testing.T) {
	t.Run("codex turn hook", func(t *testing.T) {
		w := newWorld(t)
		_, hook := codexSession(t, w)

		w.plantNotice(record.CauseRequestGranted, state.NewID(), "granted", record.LivenessSession,
			record.Holder{Agent: "codex", SessionID: "t1"})

		for range 2 {
			turn := w.hook(hook, "mail-turn", "t1", map[string]any{"hook_event_name": "PostToolUse"})
			wantStatus(t, turn, exitcode.OK)
			wantContains(t, "stdout", turn.Stdout, "PostToolUse", "1 lease notice", "nitipan list")
			wantAbsent(t, "stdout", turn.Stdout, "granted")
		}

		regs, _, _ := mailbox.Registrations(w.state)
		if len(regs) != 1 || len(mailbox.Rung(w.state, regs[0].Identity)) != 0 {
			t.Error("a turn hook claimed a waking ring")
		}
	})

	t.Run("opencode plugin", func(t *testing.T) {
		w := newWorld(t)
		_, tui, server := opencodeSetup(t, w)
		wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)

		id := state.NewID()
		w.plantNotice(record.CauseRequestGranted, id, "granted", record.LivenessSession, record.Holder{Agent: "opencode", SessionID: "ses_1"})

		reminder := w.hook(server, "mail-reminder", "ses_1", nil)
		wantStatus(t, reminder, exitcode.OK)
		wantContains(t, "stdout", reminder.Stdout, "1 lease notice", "nitipan list")

		first := w.hook(server, "mail-poll", "ses_1", nil)
		wantStatus(t, first, exitcode.OK)

		var rings []pluginRing
		if err := json.Unmarshal([]byte(first.Stdout), &rings); err != nil {
			t.Fatal(err)
		}

		if len(rings) != 1 || len(rings[0].Keys) != 1 || !strings.HasPrefix(rings[0].Keys[0], mailbox.NoticeKeyPrefix+id+"-") ||
			!strings.Contains(rings[0].Ring, "nitipan list") {
			t.Fatalf("ring output %q", first.Stdout)
		}

		if second := w.hook(server, "mail-poll", "ses_1", nil); strings.TrimSpace(second.Stdout) != "[]" {
			t.Errorf("a second poll rang again: %q", second.Stdout)
		}

		wantStatus(t, w.hook(server, "mail-retry", "ses_1", map[string]any{"keys": rings[0].Keys}), exitcode.OK)

		retry := w.hook(server, "mail-poll", "ses_1", nil)
		wantContains(t, "stdout", retry.Stdout, rings[0].Keys[0])
	})
}
