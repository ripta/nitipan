package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/wire"
)

// who runs a who command as the caller and decodes its JSON.
func (m *wideMachine) who(t *testing.T, c caller, sub, subject string) whoOutput {
	t.Helper()

	res := m.w.run(c, "who", sub, subject, "--json")
	wantStatus(t, res, exitcode.OK)

	var out whoOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("decoding %q: %v", res.Stdout, err)
	}

	if out.SchemaVersion != WhoSchemaVersion {
		t.Fatalf("schema version = %q", out.SchemaVersion)
	}

	return out
}

// holdersOf polls who holds until the answer has n holders, since a mirror reaches the broker a moment
// after the lease is written.
func (m *wideMachine) holdersOf(t *testing.T, c caller, subject string, n int) whoOutput {
	t.Helper()

	var out whoOutput

	waitFor(t, "who holds "+subject+" to find "+string(rune('0'+n))+" holders", func() bool {
		out = m.who(t, c, "holds", subject)

		return len(out.Found) == n
	})

	return out
}

func foundIDs(out whoOutput) []string {
	ids := make([]string, len(out.Found))
	for i, f := range out.Found {
		ids[i] = f.Lease
	}

	slices.Sort(ids)

	return ids
}

func foundLease(t *testing.T, out whoOutput, id string) wire.Found {
	t.Helper()

	for _, f := range out.Found {
		if f.Lease == id {
			return f
		}
	}

	t.Fatalf("lease %s is not in %v", id, foundIDs(out))

	return wire.Found{}
}

func (tm *twoMachines) name(path string) string { return "repo:" + tm.rootSHA + "#" + path }

func TestWhoHoldsAcrossMachinesForARepoNameAndACheckoutPath(t *testing.T) {
	tm := newTwoMachines(t)

	grant := tm.claim1("--scope", "svc")
	wantStatus(t, grant, exitcode.OK)

	grantID := leaseIDIn(t, grant.Stdout)

	machine := tm.m2.w.run(tm.c2, "acquire", "--checkout", tm.repo2, "--scope", "svc/deeper")
	wantStatus(t, machine, exitcode.OK)

	// The user lease, its anchor on the first machine, and the second machine's overlapping lease.
	byName := tm.m2.holdersOf(t, tm.c2, tm.name("svc"), 3)

	g := foundLease(t, byName, grantID)
	if g.Reach != wire.ReachUser || g.State != wire.StateLive || !strings.HasSuffix(g.Qualified, "@"+g.Node) || len(g.Names) != 1 {
		t.Fatalf("the user lease = %+v", g)
	}

	var mirrored wire.Found

	for _, f := range byName.Found {
		if f.Reach == "machine" && f.Node != g.Node {
			mirrored = f
		}
	}

	if mirrored.State != wire.StateMirrored || len(mirrored.Names) != 1 || mirrored.Names[0].Path != "svc/deeper" ||
		!strings.HasSuffix(mirrored.Qualified, "@"+mirrored.Node) || len(mirrored.Paths) != 1 {
		t.Fatalf("the second machine's lease = %+v", mirrored)
	}

	byPath := tm.m2.who(t, tm.c2, "holds", filepath.Join(tm.repo2, "svc"))
	if !slices.Equal(foundIDs(byPath), foundIDs(byName)) {
		t.Fatalf("a checkout path = %v, the repo name = %v", foundIDs(byPath), foundIDs(byName))
	}

	text := tm.m2.w.run(tm.c2, "who", "holds", tm.name("svc"))
	wantStatus(t, text, exitcode.OK)
	wantContains(t, "stdout", text.Stdout, "user  live  lease "+grantID, "mirrored", "@")

	if got := tm.m1.who(t, tm.c1, "holds", tm.name("other")); len(got.Found) != 0 {
		t.Fatalf("nothing holds the other path: %v", foundIDs(got))
	}
}

func TestWhoHoldsAPathOutsideAnyCheckoutSearchesOnlyThisMachine(t *testing.T) {
	tm := newTwoMachines(t)

	plain1 := tm.m1.w.dir("plain")
	plain2 := tm.m2.w.dir("plain")

	wantStatus(t, tm.m1.w.run(tm.c1, "acquire", "--checkout", plain1), exitcode.OK)

	wantStatus(t, tm.m2.w.run(tm.c2, "acquire", "--checkout", plain2), exitcode.OK)

	var out whoOutput

	waitFor(t, "the first machine's lease to be mirrored", func() bool {
		out = tm.m1.who(t, tm.c1, "holds", plain1)

		return len(out.Found) == 1
	})

	if out.Found[0].Node == "" || !strings.Contains(strings.Join(out.Notes, " "), "outside any checkout") ||
		!strings.Contains(strings.Join(out.Notes, " "), "nitipan who is") {
		t.Fatalf("the answer must say it searched only this machine and suggest the name lookup: %+v", out)
	}

	if len(out.Found[0].Names) != 0 {
		t.Fatalf("a lease outside any checkout names no repo: %+v", out.Found[0])
	}

	// The same path text on the other machine is another machine's path and never meets the mirror.
	if got := tm.m2.who(t, tm.c2, "holds", plain1); len(got.Found) != 0 {
		t.Fatalf("a path on one machine must not match another machine's mirror: %+v", got.Found)
	}
}

func TestWhoHoldsAResourceNameAcrossMachines(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.m1.w.run(tm.c1, "acquire", "--resource", "cpu:heavy"), exitcode.OK)

	out := tm.m2.holdersOf(t, tm.c2, "cpu:heavy", 1)
	if f := out.Found[0]; f.Resource != "cpu:heavy" || f.State != wire.StateMirrored || f.Reach != "machine" || f.Node == "" {
		t.Fatalf("resource holder = %+v", f)
	}
}

func TestWhoHoldsAQualifiedNameAndRefusesAnUnseenLabel(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	out := tm.m2.holdersOf(t, tm.c2, tm.name("svc"), 2)
	g := out.Found[0]

	byAddress := tm.m2.who(t, tm.c2, "holds", g.Qualified)
	if len(byAddress.Found) != 2 {
		t.Fatalf("both leases of %s = %v", g.Qualified, foundIDs(byAddress))
	}

	res := tm.m2.w.run(tm.c2, "who", "holds", "someone@never-seen-label", "--json")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "never seen")
}

func TestWhoIsFindsTheNameOnEveryMachineAndFlagsADuplicate(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.claim1("--scope", "svc", "--name", "guardian"), exitcode.OK)

	wantStatus(t, tm.m2.w.run(tm.c2, "acquire", "--checkout", tm.repo2, "--scope", "other", "--name", "guardian"), exitcode.OK)

	var out whoOutput

	waitFor(t, "two holders named guardian", func() bool {
		out = tm.m1.who(t, tm.c1, "is", "guardian")

		return out.Holders == 2
	})

	if !out.Duplicate {
		t.Fatalf("a name shared by two holders must be flagged: %+v", out)
	}

	text := tm.m1.w.run(tm.c1, "who", "is", "guardian")
	wantStatus(t, text, exitcode.OK)
	wantContains(t, "stdout", text.Stdout, "2 different holders", "Do not pick one")

	handle := ""

	for _, f := range out.Found {
		if f.Reach == wire.ReachUser {
			handle = f.Holder.Handle
		}
	}

	byHandle := tm.m2.who(t, tm.c2, "is", handle)
	if byHandle.Duplicate || byHandle.Holders != 1 || len(byHandle.Found) < 1 {
		t.Fatalf("a handle names one holder: %+v", byHandle)
	}
}

func TestWhoHoldsReadsAV1RecordAndAnUnreadableSidecarAndAQuietNode(t *testing.T) {
	tm := newTwoMachines(t)

	v1 := tm.m2.w.plant(plantSpec{Paths: []string{filepath.Join(tm.repo2, "svc", "a")}, PID: 300, Handle: "v1-handle", Name: "v1"})

	out := tm.m1.holdersOf(t, tm.c1, tm.name("svc"), 1)
	if f := foundLease(t, out, v1); f.State != wire.StateMirrored || len(f.Names) != 1 || f.Holder.Handle != "v1-handle" {
		t.Fatalf("the node derives the entries of a V1 record: %+v", f)
	}

	future := tm.m2.w.plant(plantSpec{Paths: []string{filepath.Join(tm.repo2, "other")}, PID: 301, Handle: "future"})

	err := atomicfile.Write(filepath.Join(tm.m2.w.state, handoff.DerivedDir), future+".json", []byte(`{"version":"2.0","id":"`+future+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the unreadable sidecar to read as unknown", func() bool {
		got := tm.m1.who(t, tm.c1, "holds", tm.name("other"))

		return len(got.Found) == 1 && got.Found[0].Lease == future && got.Found[0].State == wire.StateUnknown
	})

	tm.m2.stop()
	tm.f.clock.Advance(10 * time.Minute)

	waitFor(t, "the quiet node's mirror to read stale", func() bool {
		got := tm.m1.who(t, tm.c1, "holds", tm.name("svc"))

		for _, f := range got.Found {
			if f.Lease == v1 {
				return f.State == wire.StateStale && f.Stale
			}
		}

		return false
	})
}

func noticeFiles(t *testing.T, m *wideMachine) int {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(m.w.state, "notices"))
	if err != nil {
		return 0
	}

	return len(entries)
}

func TestClaimWarnsAboutAMirroredMachineLeaseAndNoticesReachTheNewBinaryHolder(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.m2.w.run(tm.c2, "acquire", "--checkout", tm.repo2, "--scope", "svc"), exitcode.OK)

	// A V1 record on the same machine overlaps too. Its holder cannot read a notice.
	tm.m2.w.plant(plantSpec{Paths: []string{filepath.Join(tm.repo2, "svc", "v1")}, PID: 300, Handle: "v1-handle"})

	tm.m1.holdersOf(t, tm.c1, tm.name("svc"), 2)

	claim := tm.claim1("--scope", "svc")
	wantStatus(t, claim, exitcode.OK)
	wantContains(t, "stderr", claim.Stderr, "nitipan: warning: your user lease", "also covers", "Nothing refuses either one")

	if n := strings.Count(claim.Stderr, "nitipan: warning: your user lease"); n != 2 {
		t.Fatalf("one warning per overlapping machine lease, got %d in %q", n, claim.Stderr)
	}

	waitFor(t, "the machine lease holder's notice", func() bool { return noticeFiles(t, tm.m2) >= 1 })

	list := tm.m2.w.run(tm.c2, "list")
	wantStatus(t, list, exitcode.OK)
	wantContains(t, "stderr", list.Stderr, "nitipan: notice: your machine lease", "user lease")

	time.Sleep(300 * time.Millisecond)

	if got := noticeFiles(t, tm.m2); got != 0 {
		// list delivered and removed the notice. A standing overlap writes no second one, and the V1 record's
		// holder never gets one.
		t.Fatalf("notices left on the second machine after delivery: %d", got)
	}

	if got := noticeFiles(t, tm.m1); got != 0 {
		t.Fatalf("the claimant saw the overlap as a warning, so the snapshot answer writes it no notice: %d", got)
	}
}

func TestOverlapAppearingAfterTheClaimNoticesBothSides(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	// A machine claim never asks the broker, so the overlap is found when the node mirrors it.
	wantStatus(t, tm.m2.w.run(tm.c2, "acquire", "--checkout", tm.repo2, "--scope", "svc"), exitcode.OK)

	waitFor(t, "a notice on each machine", func() bool { return noticeFiles(t, tm.m1) >= 1 && noticeFiles(t, tm.m2) >= 1 })

	first := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stderr", first.Stderr, "nitipan: notice: your user lease", "names repo files")

	second := tm.m2.w.run(tm.c2, "list")
	wantContains(t, "stderr", second.Stderr, "nitipan: notice: your machine lease", "covers repo files")
}

func writeNodeConfig(t *testing.T, w *world) {
	t.Helper()

	dir := config.Dir(filepath.Join(w.root, "userhome"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, config.NodeFile), []byte(`{"version":"1.0","broker":"http://127.0.0.1:1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func derivedOf(t *testing.T, w *world, id string) handoff.Derived {
	t.Helper()

	d, err := handoff.ReadDerived(w.state, id)
	if err != nil {
		t.Fatalf("no derived entries for %s: %v", id, err)
	}

	return d
}

func TestMachineClaimRecordsItsRepoEntriesOnlyWhereANodeIsConfigured(t *testing.T) {
	t.Run("no node config", func(t *testing.T) {
		w := newWorld(t)
		c := w.claude(100, "s1")
		repo := w.repoIn("repo")
		w.installHooks(c, repo)

		wantStatus(t, w.run(c, "acquire", "--checkout", repo), exitcode.OK)

		if _, err := os.Stat(filepath.Join(w.state, handoff.DerivedDir)); err == nil {
			t.Fatal("a user with no node writes no derived entries")
		}
	})

	t.Run("node configured but down", func(t *testing.T) {
		w := newWorld(t)
		writeNodeConfig(t, w)

		c := w.claude(100, "s1")
		repo := w.repoIn("repo")
		sha := gitIn(t, repo, "rev-list", "--max-parents=0", "HEAD")

		w.installHooks(c, repo)

		wantStatus(t, w.run(c, "acquire", "--checkout", repo, "--scope", "svc"), exitcode.OK)

		ids := w.leaseIDs()
		if len(ids) != 1 {
			t.Fatalf("leases = %v", ids)
		}

		d := derivedOf(t, w, ids[0])
		if d.By != handoff.DerivedByCLI || d.State != wire.DerivedOK || len(d.Entries) != 1 || d.Entries[0].Path != "svc" ||
			!d.Entries[0].HasRoot(sha) {
			t.Fatalf("derived = %+v", d)
		}
	})

	t.Run("a failed derivation never fails the claim", func(t *testing.T) {
		w := newWorld(t)
		writeNodeConfig(t, w)

		c := w.claude(100, "s1")
		origin := w.repoIn("o")
		gitIn(t, origin, "commit", "--allow-empty", "-m", "second")

		shallow := filepath.Join(w.root, "shallow")
		gitIn(t, w.root, "clone", "--depth", "1", "file://"+origin, shallow)
		w.installHooks(c, shallow)

		wantStatus(t, w.run(c, "acquire", "--checkout", shallow), exitcode.OK)

		ids := w.leaseIDs()
		if len(ids) != 1 {
			t.Fatalf("leases = %v", ids)
		}

		d := derivedOf(t, w, ids[0])
		if d.State != wire.DerivedFailed || !strings.Contains(d.Reason, "shallow") {
			t.Fatalf("a shallow clone reads as a failed derivation, which discovery shows as unknown: %+v", d)
		}
	})

	t.Run("outside a checkout the lease names no repo", func(t *testing.T) {
		w := newWorld(t)
		writeNodeConfig(t, w)

		c := w.claude(100, "s1")

		wantStatus(t, w.run(c, "acquire", "--checkout", w.dir("plain")), exitcode.OK)

		d := derivedOf(t, w, w.leaseIDs()[0])
		if d.State != wire.DerivedOK || len(d.Entries) != 0 {
			t.Fatalf("derived = %+v", d)
		}
	})
}
