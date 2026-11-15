package cli

import (
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
)

const gpuSlots2 = `{"version": 1, "slots": {"gpu:main": 2}}`

func TestSlotAdmitsNHoldersAndRefusesTheNextWithTheHoldersNamed(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main", "--name", "alpha")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main", "--name", "beta")

	res := w.run(w.claude(400, "s3"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "gpu:main has 2 of 2 slots taken", "alpha", "beta", "pid 200", "pid 300",
		"ask the user to raise the count for gpu:main")
	wantAbsent(t, "stderr", res.Stderr, "slots.json")

	if len(w.leases()) != 2 {
		t.Errorf("%d records, want the 2 that were there", len(w.leases()))
	}

	for _, rec := range w.leases() {
		if rec.Domain.Slots != 2 {
			t.Errorf("record %s was admitted under %d, want 2", rec.ID, rec.Domain.Slots)
		}
	}
}

func TestNameWithNoEntryStaysExclusive(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "cpu:heavy has 1 of 1 slots taken")
}

func TestDeadHoldersSlotIsFreedForTheNextClaim(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")
	w.kill(200)

	res := w.run(w.claude(400, "s3"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "cleared")

	if len(w.leases()) != 2 {
		t.Errorf("%d records, want the live holder and the new one", len(w.leases()))
	}
}

func TestRaisingTheCountAdmitsNoOneUntilLowerCountHoldersRelease(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	a, b := w.claude(200, "s1"), w.claude(300, "s2")
	w.acquireOK(a, "--resource", "gpu:main")
	w.acquireOK(b, "--resource", "gpu:main")

	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 3}}`)

	c, d := w.claude(400, "s3"), w.claude(500, "s4")

	res := w.run(c, "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "2 of 2 slots taken", "The configured count is 3",
		"admitted under a count of 2")

	wantStatus(t, w.run(a, "release", "--resource", "gpu:main"), exitcode.OK)

	cID := w.acquireOK(c, "--resource", "gpu:main")
	if got := w.leaseByID(cID).Domain.Slots; got != 3 {
		t.Errorf("the new record copied %d, want the configured 3", got)
	}

	res = w.run(d, "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "2 of 2 slots taken")

	wantStatus(t, w.run(b, "release", "--resource", "gpu:main"), exitcode.OK)
	w.acquireOK(d, "--resource", "gpu:main")

	if len(w.leases()) != 2 {
		t.Errorf("%d records, want the holders admitted under 3", len(w.leases()))
	}
}

func TestLoweringTheCountTakesEffectAtOnce(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 3}}`)

	a := w.claude(200, "s1")
	w.acquireOK(a, "--resource", "gpu:main")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")
	w.acquireOK(w.claude(400, "s3"), "--resource", "gpu:main")

	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 2}}`)

	d := w.claude(500, "s4")

	res := w.run(d, "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "3 of 2 slots taken")

	wantStatus(t, w.run(a, "release", "--resource", "gpu:main"), exitcode.OK)

	res = w.run(d, "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "2 of 2 slots taken")
}

func TestRetryWithTheSameSlotsAfterTheConfigChangedIsAReacquire(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	c := w.claude(200, "s1")
	id := w.acquireOK(c, "--resource", "gpu:main", "--slots", "2")

	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 3}}`)

	res := w.run(c, "acquire", "--resource", "gpu:main", "--slots", "2")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "already held; nothing changed")

	res = w.run(c, "acquire", "--resource", "gpu:main", "--slots", "3")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "--slots 3", "count 2")

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != id {
		t.Errorf("records = %v, want only %s", ids, id)
	}
}

func TestSlotsThatDiffersFromTheConfigIsRefusedNamingTheConfiguredCount(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main", "--slots", "3")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "--slots 3", "count 2")

	if len(w.leases()) != 0 {
		t.Error("a mismatched --slots published a record")
	}

	wantStatus(t, w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main", "--slots", "2"), exitcode.OK)
}

func TestReacquireOverTheCountRefreshesAndKeepsItsCount(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	a := w.claude(200, "s1")
	id := w.acquireOK(a, "--resource", "gpu:main")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")

	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 1}}`)

	res := w.run(a, "acquire", "--resource", "gpu:main", "--description", "still training")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "advertisement updated")

	rec := w.leaseByID(id)
	if rec.Domain.Slots != 2 || rec.Description != "still training" {
		t.Errorf("record = slots %d, description %q, want the old count and the new description", rec.Domain.Slots,
			rec.Description)
	}

	res = w.run(a, "acquire", "--resource", "gpu:main", "--slots", "1")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "count 2")
}

func TestRemoteRecordsCountTowardNeitherHoldersNorMinimum(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	for i, pid := range []int{71, 72, 73} {
		w.table.Add(pid, proctest.Process{Start: startOf(pid)})
		w.plant(plantSpec{PID: pid, Resource: "gpu:main", Machine: otherMachine, Slots: 1, Name: string(rune('a' + i))})
	}

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")
	w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")

	res := w.run(w.claude(400, "s3"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "2 of 2 slots taken")
}

func TestListShowsEffectiveCountAndLiveHolders(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.table.Add(77, proctest.Process{Start: startOf(77)})
	remote := w.plant(plantSpec{PID: 77, Resource: "gpu:main", Machine: otherMachine})

	first := w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")
	second := w.acquireOK(w.claude(300, "s2"), "--resource", "gpu:main")

	var seen int

	for _, lease := range leasesOf(t, w.listJSON(w.terminal(900))) {
		if lease["kind"] != "resource" {
			continue
		}

		if lease["id"] == remote {
			if _, ok := lease["effective_slots"]; ok {
				t.Errorf("a remote lease carries an effective count: %v", lease)
			}

			if _, ok := lease["slot_holders"]; ok {
				t.Errorf("a remote lease carries holders: %v", lease)
			}

			continue
		}

		seen++

		holders, _ := lease["slot_holders"].([]any)
		if lease["effective_slots"] != float64(2) || len(holders) != 2 || holders[0] != first || holders[1] != second {
			t.Errorf("lease = %v, want 2 slots held by %s and %s", lease, first, second)
		}
	}

	if seen != 2 {
		t.Errorf("%d local resource leases listed, want 2", seen)
	}

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "slots: 2 of 2 in use, held by "+first+", "+second,
		"slot counts are configured in "+w.state+"/slots.json")
}

func TestListEffectiveCountIsTheAdmissionMinimum(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")
	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 5}}`)

	for _, lease := range leasesOf(t, w.listJSON(w.terminal(900))) {
		if lease["effective_slots"] != float64(2) {
			t.Errorf("lease = %v, want the minimum 2", lease)
		}
	}
}

func TestListAlwaysPrintsTheConfigPath(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "no leases", "slot counts are configured in "+w.state+"/slots.json")

	w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo"))

	res = w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "slot counts are configured in "+w.state+"/slots.json")
}

func TestHolderWithUnknownLivenessTakesASlot(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.plant(plantSpec{PID: 71, Resource: "gpu:main", Liveness: "future", Slots: 2})
	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "2 of 2 slots taken", "counts as alive")
}

func TestDeadHolderThatIsNotClearableStillBlocks(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.plant(plantSpec{PID: 71, Resource: "gpu:main", KeepAfterDeath: true})

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "has died", "newer nitipan")

	if len(w.leases()) != 1 {
		t.Errorf("%d records, want only the planted one", len(w.leases()))
	}
}

func TestLiveRecordWithAnUnreadableBodyBlocksAsExclusive(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	w.table.Add(71, proctest.Process{Start: startOf(71)})
	w.plant(plantSpec{PID: 71, Resource: "gpu:main", Kind: "future"})

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "the claim overlaps")

	if len(w.leases()) != 1 {
		t.Errorf("%d records, want only the planted one", len(w.leases()))
	}
}

type unusableSlotConfigTest struct {
	Name   string
	Config string
	Want   []string
}

var unusableSlotConfigTests = []unusableSlotConfigTest{
	{Name: "a count of zero", Config: `{"version": 1, "slots": {"gpu:main": 0}}`, Want: []string{`"gpu:main"`, "at least 1"}},
	{Name: "a fractional count", Config: `{"version": 1, "slots": {"gpu:main": 1.5}}`, Want: []string{"at least 1"}},
	{Name: "a string count", Config: `{"version": 1, "slots": {"gpu:main": "2"}}`, Want: []string{"at least 1"}},
	{Name: "a bad name", Config: `{"version": 1, "slots": {"GPU:main": 2}}`, Want: []string{`"GPU:main"`, "lowercase"}},
	{Name: "the repo namespace", Config: `{"version": 1, "slots": {"repo:x": 2}}`, Want: []string{`"repo:x"`, "reserved"}},
	{Name: "an unknown version", Config: `{"version": 2, "slots": {}}`, Want: []string{`unknown "version" 2`}},
	{Name: "an unknown field", Config: `{"version": 1, "slots": {}, "extra": true}`, Want: []string{`unknown field "extra"`}},
	{Name: "not json", Config: `{"version": `, Want: []string{"not a JSON object"}},
	{
		Name:   "every error at once",
		Config: `{"version": 3, "slots": {"gpu:main": 0, "repo:x": 1, "BAD": 2}, "extra": 1}`,
		Want:   []string{`"gpu:main"`, `"repo:x"`, `"BAD"`, `unknown field "extra"`, `"version" 3`},
	},
}

func TestUnusableSlotConfigMakesEveryNameExclusiveAndWarns(t *testing.T) {
	for _, test := range unusableSlotConfigTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			w.writeSlots(test.Config)

			path := w.state + "/slots.json"

			first := w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main")
			wantStatus(t, first, exitcode.OK)
			wantContains(t, "stderr", first.Stderr, "warning", path, "Ask the user to fix it")
			wantContains(t, "stderr", first.Stderr, test.Want...)

			second := w.run(w.claude(300, "s2"), "acquire", "--resource", "gpu:main")
			wantStatus(t, second, exitcode.Refused)
			wantContains(t, "stderr", second.Stderr, "1 of 1 slots taken", path)

			res := w.run(w.claude(400, "s3"), "acquire", "--checkout", w.dir("repo"))
			wantStatus(t, res, exitcode.OK)

			for _, args := range [][]string{{"list"}, {"release", "--resource", "gpu:main"}} {
				cmd := w.claude(200, "s1")
				if args[0] == "list" {
					cmd = w.terminal(900)
				}

				res := w.run(cmd, args...)
				wantStatus(t, res, exitcode.OK)
				wantContains(t, "stderr", res.Stderr, path)
			}
		})
	}
}

func TestUsableSlotConfigWarnsNothing(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "gpu:main")
	wantStatus(t, res, exitcode.OK)

	if strings.Contains(res.Stderr, "warning") {
		t.Errorf("stderr = %q, want no warning", res.Stderr)
	}
}

func TestUnusableSlotConfigJudgesSlotsAgainstOne(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(`{"version": 9}`)

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "gpu:main", "--slots", "2")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "count 1")
}
