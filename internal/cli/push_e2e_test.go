package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/state"
)

// These tests drive the real hooks and a real bare remote. The rig has no node and no broker, so the
// broker lease an agent holds is planted as a sidecar, the file the node would have written when the
// broker granted it. That is enough for everything that does not ask the broker.

const remoteSetup = `cd "$REPO"
git init -q --bare -b main "$ROOT/origin.git"
git remote add origin "$ROOT/origin.git"
git push -q origin main
git fetch -q origin
`

// rootSHA is the root commit of the rig's repository.
func (r *rig) rootSHA() string {
	r.t.Helper()

	return strings.TrimSpace(r.must(r.user(`cd "$REPO"; git rev-list --max-parents=0 HEAD`)).Stdout)
}

// plantBrokerLease writes the sidecar of a broker lease that the agent holds over a repo path, with the
// token the broker would have issued. The agent must have claimed a machine lease already.
func (r *rig) plantBrokerLease(machineLease string, token uint64, path string) {
	r.t.Helper()

	data, err := os.ReadFile(filepath.Join(r.state, state.LeasesDir, machineLease+".json"))
	if err != nil {
		r.t.Fatal(err)
	}

	rec, err := record.Decode(data)
	if err != nil {
		r.t.Fatal(err)
	}

	dir, err := state.Open(r.state, state.Options{})
	if err != nil {
		r.t.Fatal(err)
	}

	l, err := dir.Lock()
	if err != nil {
		r.t.Fatal(err)
	}

	defer func() { _ = l.Unlock() }()

	sc := handoff.Sidecar{
		ID: state.NewID(), Reach: "user", Anchor: handoff.Anchor{ID: machineLease},
		Entries: []repoid.Name{repoid.New([]string{r.rootSHA()}, path, false, false)},
		Holder:  rec.Envelope, CreatedAt: time.Now(), Token: token,
	}

	if err := handoff.WriteSidecar(l, sc); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) fenceRefs() string {
	r.t.Helper()

	return strings.TrimSpace(r.must(r.user(`git ls-remote "$ROOT/origin.git" 'refs/nitipan/fence/*'`)).Stdout)
}

func (r *rig) fenceName(path string) string {
	return "refs/nitipan/fence/" + r.rootSHA() + "/" + path
}

func TestThePrePushHookRefusesAPlainPushThatLacksTheFenceAndNitipanPushCarriesIt(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	lease := alpha.claim("src")
	r.plantBrokerLease(lease, 5, "src")

	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

	plain := alpha.run(`git push origin main`)
	if plain.Status == 0 || !strings.Contains(plain.Stderr, "nitipan push origin") {
		t.Fatalf("a plain push of a fenced path must be refused and name nitipan push:\n%s", plain.Stderr)
	}

	if got := r.fenceRefs(); got != "" {
		t.Fatalf("a refused push left a fence behind: %s", got)
	}

	wrapped := alpha.run(`nit push origin main`)
	if wrapped.Status != 0 {
		t.Fatalf("nitipan push failed: %d\n%s%s", wrapped.Status, wrapped.Stdout, wrapped.Stderr)
	}

	if got := r.fenceRefs(); !strings.Contains(got, r.fenceName("src")) {
		t.Fatalf("no fence ref on the remote: %q", got)
	}

	body := r.must(r.user(`git --git-dir "$ROOT/origin.git" cat-file blob ` + r.fenceName("src") + `:fence.json`)).Stdout
	if !strings.Contains(body, `"token":5`) || !strings.Contains(body, `"path":"src"`) {
		t.Fatalf("fence.json = %s", body)
	}

	author := r.must(r.user(`git --git-dir "$ROOT/origin.git" log -1 --format='%an <%ae>' ` + r.fenceName("src"))).Stdout
	if strings.TrimSpace(author) != "nitipan <nitipan@invalid>" {
		t.Errorf("the fence names %q", author)
	}

	main := r.refs("main")[0]
	if got := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse main`)).Stdout); got != main {
		t.Errorf("remote main = %s, want %s", got, main)
	}
}

func (a *agentShell) must(t *testing.T, body string) {
	t.Helper()

	a.r.must(a.run(body))
}

func TestTheUserPushesThroughTheHookWithNoFenceAndMirrorIsRefusedWithTheFetchHint(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
	alpha.must(t, `nit push origin main`)

	r.must(r.user(`cd "$REPO"; git checkout -q -b userwork; echo u >> README; git commit -qam u; git push -q origin userwork`))

	r.must(r.user(`cd "$REPO"; git update-ref -d ` + r.fenceName("src")))

	mirror := r.user(`cd "$REPO"; git push --mirror origin`)
	if mirror.Status == 0 {
		t.Fatal("a mirror push from a clone that lacks the fence refs must be refused")
	}

	for _, want := range []string{"fence refs differ from the remote's", "git fetch origin '+refs/nitipan/fence/*:refs/nitipan/fence/*'"} {
		if !strings.Contains(mirror.Stderr, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, mirror.Stderr)
		}
	}

	if got := r.fenceRefs(); !strings.Contains(got, r.fenceName("src")) {
		t.Fatalf("the refused mirror deleted the fence: %q", got)
	}

	r.must(r.user(`cd "$REPO"; git fetch -q origin '+refs/nitipan/fence/*:refs/nitipan/fence/*'; git push -q --mirror origin`))
}

func TestOnlyTheUsersOwnTerminalCanDeleteAFenceRef(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
	alpha.must(t, `nit push origin main`)

	asAgent := alpha.run(`nit fence delete origin ` + r.rootSHA() + `/src`)
	if asAgent.Status != exitcode.Refused || strings.Contains(asAgent.Stderr, "--no-verify") {
		t.Fatalf("an agent's fence delete: status %d\n%s", asAgent.Status, asAgent.Stderr)
	}

	rawDelete := alpha.run(`git push origin :` + r.fenceName("src"))
	if rawDelete.Status == 0 {
		t.Fatal("an agent deleted a fence ref with git push")
	}

	userRaw := r.user(`cd "$REPO"; git push origin :` + r.fenceName("src"))
	if userRaw.Status == 0 {
		t.Fatal("the user deleted a fence ref without fence delete")
	}

	if got := r.fenceRefs(); !strings.Contains(got, r.fenceName("src")) {
		t.Fatalf("the fence is gone: %q", got)
	}

	res := r.user(`cd "$REPO"; nit fence delete origin '` + r.rootSHA() + `/src'`)
	if res.Status != 0 {
		t.Fatalf("fence delete: %d\n%s", res.Status, res.Stderr)
	}

	if got := r.fenceRefs(); got != "" {
		t.Fatalf("the fence is still there: %q", got)
	}
}

type fenceKeyTest struct {
	Name string
	Key  string
	Want string
	Fail bool
}

var fenceKeyTests = []fenceKeyTest{
	{Name: "root and encoded path", Key: rootKey + "/src", Want: "refs/nitipan/fence/" + rootKey + "/src"},
	{Name: "full ref name", Key: "refs/nitipan/fence/" + rootKey + "/src", Want: "refs/nitipan/fence/" + rootKey + "/src"},
	{Name: "repo name", Key: "repo:" + rootKey + "#src/app", Want: "refs/nitipan/fence/" + rootKey + "/src%2Fapp"},
	{Name: "repo name of the whole repo", Key: "repo:" + rootKey + "#", Want: "refs/nitipan/fence/" + rootKey + "/%00"},
	{Name: "not canonical", Key: rootKey + "/Src", Fail: true},
	{Name: "no root", Key: "src", Fail: true},
}

const rootKey = "1111111111111111111111111111111111111111"

func TestAMergedHistoryRepoNameMapsToTheFirstParentRoot(t *testing.T) {
	const second = "2222222222222222222222222222222222222222"

	first := func(typed string) string {
		if typed == second {
			return rootKey
		}

		return typed
	}

	got, err := fenceRefOf("repo:"+second+"#src", first)
	if err != nil || got != "refs/nitipan/fence/"+rootKey+"/src" {
		t.Fatalf("fenceRefOf = %q, %v", got, err)
	}
}

func TestFenceDeleteReadsTheKeyTheUserTypes(t *testing.T) {
	for _, test := range fenceKeyTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := fenceRefOf(test.Key, nil)
			if (err != nil) != test.Fail || got != test.Want && !test.Fail {
				t.Fatalf("fenceRefOf(%q) = %q, %v", test.Key, got, err)
			}
		})
	}
}

// A competing writer moves the fence between nitipan push's check and the push itself. The push loses
// the compare-and-swap, fetches again, and goes through. The chained hook runs after nitipan's own
// check, so it is where the race can be forced without a sleep.
func TestALostCompareAndSwapIsRetried(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
	alpha.must(t, `nit push origin main`)

	alpha.must(t, `echo y >> src/a.txt; git commit -qam c2`)

	racer := `#!/bin/sh
if [ -e "$ROOT/raced" ]; then exit 0; fi
touch "$ROOT/raced"
cd "$ROOT/origin.git"
ref=` + r.fenceName("src") + `
tree=$(git rev-parse "$ref^{tree}")
new=$(GIT_AUTHOR_NAME=n GIT_AUTHOR_EMAIL=n@invalid GIT_COMMITTER_NAME=n GIT_COMMITTER_EMAIL=n@invalid git commit-tree "$tree" -p "$ref" -m racer)
git update-ref "$ref" "$new"
`

	hook := filepath.Join(r.repo, ".git", "hooks", "pre-push.nitipan-orig")
	if err := os.WriteFile(hook, []byte(racer), 0o755); err != nil {
		t.Fatal(err)
	}

	res := alpha.run(`nit push origin main`)
	if res.Status != 0 {
		t.Fatalf("status %d\n%s%s", res.Status, res.Stdout, res.Stderr)
	}

	if _, err := os.Stat(filepath.Join(r.root, "raced")); err != nil {
		t.Fatal("the racing writer never ran, so the test proved nothing")
	}

	count := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-list --count ` + r.fenceName("src"))).Stdout)
	if count != "3" {
		t.Errorf("the fence has %s commits, want 3: first push, the racer, and the retry", count)
	}
}

func TestAHostWithoutAtomicPushGetsALabeledLeaseCheckNotFencing(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))
	r.must(r.user(`git --git-dir "$ROOT/origin.git" config receive.advertiseAtomic false`))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim(""), 5, "")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

	res := alpha.run(`nit push origin main`)
	if res.Status != exitcode.CouldNotVerify {
		t.Fatalf("status %d, want %d (no node can renew the lease)\n%s", res.Status, exitcode.CouldNotVerify, res.Stderr)
	}

	for _, want := range []string{"origin does not support atomic push", "lease check and not fencing", "not a lost lease"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, res.Stderr)
		}
	}

	if reason, label := strings.Index(res.Stderr, "origin does not support atomic push"), strings.Index(res.Stderr, "could not verify"); reason < 0 || label < reason {
		t.Errorf("the error must lead with the host's reason:\n%s", res.Stderr)
	}

	if got := r.fenceRefs(); got != "" {
		t.Errorf("a fence landed on a host without atomic push: %q", got)
	}

	r.must(r.user(`cd "$REPO"; git config nitipan.origin.fence false`))

	plain := alpha.run(`git push origin main`)
	if plain.Status == 0 || !strings.Contains(plain.Stderr, "lease check") || !strings.Contains(plain.Stderr, "nitipan.origin.fence is false") {
		t.Errorf("a plain push to a remote with fence=false: status %d\n%s", plain.Status, plain.Stderr)
	}

	r.must(r.user(`cd "$REPO"; git push -q origin main`))
}

func TestAnAgentWithNoLeaseIsNotAskedForAFenceWhereNoFenceExists(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	beta := r.agent("beta")
	beta.must(t, `echo x >> README; git commit -qam c; git push -q origin main`)

	if got := r.fenceRefs(); got != "" {
		t.Errorf("a push by an agent with no broker lease created a fence: %q", got)
	}
}

// plantFence writes a fence commit straight onto the remote, as another nitipan version would have.
func (r *rig) plantFence(path, object string) string {
	r.t.Helper()

	ref := r.fenceName(path)

	r.must(r.user(`cd "$ROOT/origin.git"
blob=$(printf '%s\n' '` + object + `' | git hash-object -w --stdin)
tree=$(printf '100644 blob %s\tfence.json\n' "$blob" | git mktree)
commit=$(GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@invalid GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@invalid git commit-tree "$tree" -m planted)
git update-ref ` + ref + ` "$commit"`))

	return strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse ` + ref)).Stdout)
}

// One remote holds fence objects of three versions. The binary reads the newer minor, refreshes it
// without dropping a field it does not know, writes its own at its own minor, and refuses the newer
// major with the upgrade message.
func TestOneRemoteHoldsFencesOfThreeVersions(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	r.plantFence("src", `{"version":"1.1","token":3,"path":"src","lease":"X","future":1}`)
	root := r.plantFence("%00", `{"version":"1.1","token":2,"path":"%00","lease":"Y","future":2}`)
	r.plantFence("docs", `{"version":"2.0","token":"seven","path":42}`)

	rootTree := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse ` + r.fenceName("%00") + `^{tree}`)).Stdout)

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 6, "src")

	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
	alpha.must(t, `nit push origin main`)

	own := r.must(r.user(`git --git-dir "$ROOT/origin.git" cat-file blob ` + r.fenceName("src") + `:fence.json`)).Stdout
	if !strings.Contains(own, `"version":"1.0"`) || !strings.Contains(own, `"token":6`) || strings.Contains(own, "future") {
		t.Errorf("the key the pusher holds is rewritten at its own version: %s", own)
	}

	if got := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse ` + r.fenceName("%00") + `^{tree}`)).Stdout); got != rootTree {
		t.Errorf("the ancestor was refreshed with a new tree %s, want the old one %s", got, rootTree)
	}

	if got := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse ` + r.fenceName("%00") + `^`)).Stdout); got != root {
		t.Errorf("the refreshed ancestor's parent = %s, want %s", got, root)
	}

	alpha.must(t, `echo y >> docs/b.txt; git commit -qam d`)

	refused := alpha.run(`nit push origin main`)
	if refused.Status != exitcode.Refused || !strings.Contains(refused.Stderr, "newer nitipan") || !strings.Contains(refused.Stderr, "Upgrade nitipan") {
		t.Fatalf("status %d\n%s", refused.Status, refused.Stderr)
	}
}

func TestAFenceRefNameThatDoesNotDecodeRefusesEveryPushToThatRemote(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	r.must(r.user(`cd "$ROOT/origin.git"; git update-ref refs/nitipan/fence/` + r.rootSHA() + `/Not-Canonical main`))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

	res := alpha.run(`nit push origin main`)
	if res.Status != exitcode.Refused || !strings.Contains(res.Stderr, "Upgrade nitipan") || !strings.Contains(res.Stderr, "Not-Canonical") {
		t.Fatalf("status %d\n%s", res.Status, res.Stderr)
	}
}

// A checkout installed before the pre-push hook existed has only the first hook. The claim says to
// install again.
func TestACheckoutWithOnlyTheFirstHookIsToldToInstallAgain(t *testing.T) {
	r := newRig(t)

	if err := os.Remove(filepath.Join(r.repo, ".git", "hooks", "pre-push")); err != nil {
		t.Fatal(err)
	}

	res := r.agent("alpha").run(`nit acquire --checkout "$REPO" --name alpha`)
	if res.Status == 0 || !strings.Contains(res.Stderr, "there is no pre-push hook") || !strings.Contains(res.Stderr, "nitipan setup git --install") {
		t.Fatalf("status %d\n%s", res.Status, res.Stderr)
	}
}

// git accepts an abbreviated long option, and an alias can hide the flag. The hook does not look for
// the flag. It refuses any push while the clone's fence refs differ from the remote's, and its own fetch
// lets the next push through.
func TestAbbreviatedAndAliasedMirrorPushesAreRefusedUntilTheFenceRefsAreHere(t *testing.T) {
	pushes := []string{
		`git push --mir origin`,
		`git -c alias.p='push --mirror' p origin`,
		`git -c remote.origin.mirror=true push origin`,
	}

	for _, push := range pushes {
		t.Run(push, func(t *testing.T) {
			r := newRig(t)
			r.must(r.user(remoteSetup))

			alpha := r.agent("alpha")
			r.plantBrokerLease(alpha.claim("src"), 5, "src")
			alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
			alpha.must(t, `nit push origin main`)

			r.must(r.user(`cd "$REPO"; git update-ref -d ` + r.fenceName("src")))

			refused := r.user(`cd "$REPO"; ` + push)
			if refused.Status == 0 || !strings.Contains(refused.Stderr, "fetched them just now, so push again") {
				t.Fatalf("status %d\n%s", refused.Status, refused.Stderr)
			}

			if got := r.fenceRefs(); !strings.Contains(got, r.fenceName("src")) {
				t.Fatalf("the refused push deleted the fence: %q", got)
			}

			r.must(r.user(`cd "$REPO"; ` + push))

			if got := r.fenceRefs(); !strings.Contains(got, r.fenceName("src")) {
				t.Fatalf("the second push deleted the fence: %q", got)
			}
		})
	}
}

// An agent could otherwise push a fence ref with a huge token and lock every other holder out. The
// user's own terminal holds no lease and is not held to the plan, so it may create a well-formed ref.
func TestAnAgentCannotPushAFenceRefItsLeasesDoNotCallFor(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")

	extra := `git push origin HEAD:refs/nitipan/fence/` + r.rootSHA() + `/%00`

	refused := alpha.run(extra)
	if refused.Status == 0 || !strings.Contains(refused.Stderr, "fence refs that the leases you hold do not call for") {
		t.Fatalf("status %d\n%s", refused.Status, refused.Stderr)
	}

	if got := r.fenceRefs(); got != "" {
		t.Fatalf("the refused push left a fence behind: %q", got)
	}

	r.must(r.user(`cd "$REPO"; ` + extra))
}
