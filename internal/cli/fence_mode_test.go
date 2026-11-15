package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
)

// These tests own the three fence modes. The wide fixture has a live broker, so a lease check can pass
// and a fallback push can land. Its git hooks are not installed, so a stub in the clone stands in
// where a test needs to see or refuse what a hook would. The shell rig has the real hook and no broker,
// so it owns the hook's side of the modes.

// configValue reads a key from the repository's own configuration.
func configValue(t *testing.T, dir, key string) (string, bool) {
	t.Helper()

	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return "", false
	}

	return strings.TrimSpace(string(out)), true
}

// claimSvc takes the svc lease for machine 1 and commits a change under it.
func (pw *pushWorld) claimSvc() {
	pw.t.Helper()

	pw.install(1)
	wantStatus(pw.t, pw.claim1("--scope", "svc"), exitcode.OK)
	pw.uninstall(1)

	pw.commitIn(pw.repo1, "svc/a.txt")
}

func (pw *pushWorld) push() result {
	return pw.m1.w.run(pw.c1, "push", "origin", "main")
}

// stubHook puts a pre-push hook in machine 1's clone.
func (pw *pushWorld) stubHook(body string) {
	pw.t.Helper()

	path := filepath.Join(pw.repo1, ".git", "hooks", "pre-push")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		pw.t.Fatal(err)
	}
}

func (pw *pushWorld) setConfig(key, value string) {
	pw.t.Helper()

	gitIn(pw.t, pw.repo1, "config", key, value)
}

type hostRefusalTest struct {
	Name string

	// Refuse makes the host refuse, and Heal makes it accept again.
	Refuse func(t *testing.T, pw *pushWorld)
	Heal   func(t *testing.T, pw *pushWorld)

	// WantReason is what the user must read of the host's refusal.
	WantReason []string
}

var hostRefusalTests = []hostRefusalTest{
	{
		Name: "a host without atomic push",
		Refuse: func(t *testing.T, pw *pushWorld) {
			gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "false")
		},
		Heal: func(t *testing.T, pw *pushWorld) {
			gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "true")
		},
		WantReason: []string{"origin does not support atomic push"},
	},
	{
		Name: "a host that refuses the fence ref only",
		Refuse: func(t *testing.T, pw *pushWorld) {
			hook := filepath.Join(pw.bare, "hooks", "update")
			body := "#!/bin/sh\ncase \"$1\" in refs/nitipan/fence/*) echo 'no fence here' >&2; exit 1;; esac\n"

			if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		Heal: func(t *testing.T, pw *pushWorld) {
			if err := os.Remove(filepath.Join(pw.bare, "hooks", "update")); err != nil {
				t.Fatal(err)
			}
		},
		WantReason: []string{"origin refused the fence ref refs/nitipan/fence/", "hook declined"},
	},
}

// With the key unset, a host refusal gets one lease-check push and a warning, and records nothing. The
// next push tries the fence again, and lands it once the host accepts.
func TestAnUnsetFenceModeFallsBackForOnePushAndTriesTheFenceOnTheNext(t *testing.T) {
	for _, test := range hostRefusalTests {
		t.Run(test.Name, func(t *testing.T) {
			pw := newPushWorld(t)
			test.Refuse(t, pw)

			pw.claimSvc()

			first := pw.push()
			wantStatus(t, first, exitcode.OK)
			wantContains(t, "stderr", first.Stderr, test.WantReason...)
			wantContains(t, "stderr", first.Stderr, "lease check and not fencing", "nitipan.origin.fence", "to false", "to true")

			if got, want := pw.remoteMain(), gitIn(t, pw.repo1, "rev-parse", "main"); got != want {
				t.Errorf("remote main = %s, want %s", got, want)
			}

			if got := pw.remoteFences(); got != "" {
				t.Errorf("the fallback landed a fence: %q", got)
			}

			for _, key := range []string{"fence", "atomic"} {
				if _, set := configValue(t, pw.repo1, "nitipan.origin."+key); set {
					t.Errorf("nitipan.origin.%s is set, and nothing may be recorded", key)
				}
			}

			test.Heal(t, pw)
			pw.commitIn(pw.repo1, "svc/b.txt")

			second := pw.push()
			wantStatus(t, second, exitcode.OK)
			wantAbsent(t, "stderr", second.Stderr, "lease check")

			if got := pw.remoteFences(); !strings.Contains(got, "refs/nitipan/fence/"+pw.rootSHA+"/svc") {
				t.Errorf("the next push did not try the fence again: %q", got)
			}
		})
	}
}

// With true, the same refusals fail the push with an error that names the key. Nothing lands. A refused
// push leaves the world as it found it, and each row heals its host after it, so the rows share one world.
func TestAFenceModeOfTrueMakesAHostRefusalAnError(t *testing.T) {
	pw := newPushWorld(t)
	pw.setConfig("nitipan.origin.fence", "true")
	pw.claimSvc()

	for _, test := range hostRefusalTests {
		t.Run(test.Name, func(t *testing.T) {
			test.Refuse(t, pw)
			defer test.Heal(t, pw)

			before := pw.remoteMain()
			res := pw.push()

			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, test.WantReason...)
			wantContains(t, "stderr", res.Stderr, "nitipan.origin.fence is true")
			wantAbsent(t, "stderr", res.Stderr, "lease check and not fencing")

			if got := pw.remoteMain(); got != before {
				t.Errorf("remote main moved to %s from %s", got, before)
			}

			if got := pw.remoteFences(); got != "" {
				t.Errorf("a refused push landed a fence: %q", got)
			}
		})
	}
}

// A false in the user's ~/.gitconfig is honored as a false in the clone is. A successful push prints
// nothing about it. A lease check that cannot run names the key.
func TestAFenceModeOfFalseInTheGlobalConfigSkipsTheFenceAndNamesTheKeyOnlyWhenItMatters(t *testing.T) {
	pw := newPushWorld(t)

	global := filepath.Join(t.TempDir(), ".gitconfig")
	writeFile(t, global, "[nitipan \"origin\"]\n\tfence = false\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	pw.claimSvc()

	ok := pw.push()
	wantStatus(t, ok, exitcode.OK)
	wantAbsent(t, "stderr", ok.Stderr, "nitipan.origin", "lease check", "fence")

	if got := pw.remoteFences(); got != "" {
		t.Errorf("a push under false wrote a fence: %q", got)
	}

	if got, want := pw.remoteMain(), gitIn(t, pw.repo1, "rev-parse", "main"); got != want {
		t.Errorf("remote main = %s, want %s", got, want)
	}

	pw.m1.stop()
	pw.commitIn(pw.repo1, "svc/b.txt")

	unverified := pw.push()
	wantStatus(t, unverified, exitcode.CouldNotVerify)
	wantContains(t, "stderr", unverified.Stderr, "nitipan.origin.fence is false", "lease check and not fencing", "not a lost lease")
}

type legacyTest struct {
	Name string

	// Config is key and value pairs for the clone.
	Config map[string]string

	// Fenced is set when the push must land its fence.
	Fenced bool

	// WantNotice is what the stderr line about the old key must say. It is empty when there is no line.
	WantNotice []string
}

var legacyTests = []legacyTest{
	{
		Name:   "the old key alone acts as false",
		Config: map[string]string{"nitipan.origin.atomic": "false"},
		WantNotice: []string{
			"nitipan.origin.atomic is false in", ".git/config", "acts as nitipan.origin.fence=false",
			"nitipan setup git --install", "renamed to nitipan.origin.fence=false or unset",
		},
	},
	{
		Name:       "a fence beside a leftover old key wins and the notice still fires",
		Config:     map[string]string{"nitipan.origin.atomic": "false", "nitipan.origin.fence": "true"},
		Fenced:     true,
		WantNotice: []string{"nitipan.origin.atomic is false in", ".git/config", "nitipan.origin.fence wins", "nitipan setup git --install"},
	},
	{
		Name:       "a fence of false beside a leftover old false is decided by the fence",
		Config:     map[string]string{"nitipan.origin.atomic": "false", "nitipan.origin.fence": "false"},
		WantNotice: []string{"nitipan.origin.atomic is false in", ".git/config", "nitipan.origin.fence wins", "nitipan setup git --install"},
	},
	{
		Name:   "an old key of true means nothing",
		Config: map[string]string{"nitipan.origin.atomic": "true"},
		Fenced: true,
	},
}

func TestTheOldAtomicKeyActsAsFalseWhenFenceIsUnsetAndIsNeverRewritten(t *testing.T) {
	for _, test := range legacyTests {
		t.Run(test.Name, func(t *testing.T) {
			pw := newPushWorld(t)

			for key, value := range test.Config {
				pw.setConfig(key, value)
			}

			pw.claimSvc()
			pw.realHook(false)

			res := pw.push()
			wantStatus(t, res, exitcode.OK)

			if len(test.WantNotice) > 0 {
				wantContains(t, "stderr", res.Stderr, test.WantNotice...)
			} else {
				wantAbsent(t, "stderr", res.Stderr, "nitipan.origin.atomic")
			}

			if got := strings.Count(res.Stderr, "nitipan.origin.atomic is"); len(test.WantNotice) > 0 && got != 1 {
				t.Errorf("the notice printed %d times, want one line:\n%s", got, res.Stderr)
			}

			if landed := pw.remoteFences() != ""; landed != test.Fenced {
				t.Errorf("fence landed = %v, want %v", landed, test.Fenced)
			}

			for key, want := range test.Config {
				if got, _ := configValue(t, pw.repo1, key); got != want {
					t.Errorf("%s = %q after the push, want %q", key, got, want)
				}
			}
		})
	}
}

type invalidValueTest struct {
	Name   string
	Config map[string]string

	// WantErr is what the error must name. It is empty when the push must succeed.
	WantErr []string
}

var invalidValueTests = []invalidValueTest{
	{
		Name:    "an invalid fence value",
		Config:  map[string]string{"nitipan.origin.fence": "maybe"},
		WantErr: []string{"nitipan.origin.fence", `"maybe"`, ".git/config", "not a boolean"},
	},
	{
		Name:    "an invalid old value with fence unset",
		Config:  map[string]string{"nitipan.origin.atomic": "perhaps"},
		WantErr: []string{"nitipan.origin.atomic", `"perhaps"`, ".git/config", "not a boolean"},
	},
	{
		Name:   "an invalid old value beside a set fence is not consulted",
		Config: map[string]string{"nitipan.origin.atomic": "perhaps", "nitipan.origin.fence": "true"},
	},
}

func TestAnInvalidFenceOrOldValueFailsThePusherByNamingTheKeyAndFile(t *testing.T) {
	for _, test := range invalidValueTests {
		t.Run(test.Name, func(t *testing.T) {
			pw := newPushWorld(t)

			for key, value := range test.Config {
				pw.setConfig(key, value)
			}

			pw.claimSvc()

			before := pw.remoteMain()
			res := pw.push()

			if len(test.WantErr) == 0 {
				wantStatus(t, res, exitcode.OK)

				return
			}

			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, test.WantErr...)

			if got := pw.remoteMain(); got != before {
				t.Errorf("remote main moved to %s from %s", got, before)
			}
		})
	}
}

// Only the fallback git call carries the variable. A caller that exports it does not make the fenced
// attempt, or a push under false, look sanctioned. The stub hook logs what each push's hook saw.
func TestOnlyTheFallbackPushCarriesTheFallbackVariableEvenWhenTheCallerExportsIt(t *testing.T) {
	pw := newPushWorld(t)
	log := filepath.Join(t.TempDir(), "seen")

	pw.stubHook(`cat > /dev/null; echo "[$NITIPAN_FENCE_FALLBACK]" >> '` + log + `'`)
	t.Setenv(fenceFallbackEnv, "origin")

	gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "false")
	pw.claimSvc()
	wantStatus(t, pw.push(), exitcode.OK)

	gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "true")
	pw.commitIn(pw.repo1, "svc/b.txt")
	wantStatus(t, pw.push(), exitcode.OK)

	pw.setConfig("nitipan.origin.fence", "false")
	pw.commitIn(pw.repo1, "svc/c.txt")
	wantStatus(t, pw.push(), exitcode.OK)

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	want := "[]\n[origin]\n[]\n[]\n"
	if got := string(data); got != want {
		t.Errorf("the hook saw %q, want %q: the fenced attempt, the fallback, a fenced push, and a push under false", got, want)
	}
}

// realHook puts the real hook in machine 1's clone, as a process of its own. It runs the test binary as
// nitipan under the agent identity of the fixture, so it tells the agent from the user and finds the
// agent's leases. An old hook is the same binary with two differences, which are the only ones that
// matter to a new pusher. It never sees NITIPAN_FENCE_FALLBACK, and it reads no fence key from the user's
// global configuration.
func (pw *pushWorld) realHook(old bool) {
	pw.t.Helper()

	exe, err := os.Executable()
	if err != nil {
		pw.t.Fatal(err)
	}

	words := []string{"exec", "env"}
	if old {
		words = append(words, "-u", fenceFallbackEnv, "GIT_CONFIG_GLOBAL=/dev/null")
	}

	for _, kv := range []string{
		asNitipanEnv + "=1", asStateEnv + "=" + pw.m1.w.state, asAgentPIDEnv + "=100", asMachineEnv + "=m1",
		"CLAUDE_PID=100", "CLAUDE_CODE_SESSION_ID=s1",
	} {
		words = append(words, strconv.Quote(kv))
	}

	words = append(words, strconv.Quote(exe), "hook", "pre-push", `"$@"`)

	pw.stubHook(strings.Join(words, " "))
}

type oldHookTest struct {
	Name string

	// Global is written to the user's global configuration, which only the new pusher sees.
	Global string

	// Refuse makes the host refuse atomic push.
	Refuse bool

	Want []string
}

var oldHookTests = []oldHookTest{
	{
		Name:   "the fallback after a host refusal",
		Refuse: true,
		Want:   []string{"origin does not support atomic push", "the lease-check push that followed failed too"},
	},
	{
		Name:   "a push under the user's false",
		Global: "[nitipan \"origin\"]\n\tfence = false\n",
	},
}

// A hook installed by an older nitipan does not know the fallback variable and reads no fence key. It
// refuses any push of a fenced path that carries no fence refs. The pusher must fail closed and name the
// reinstall, both for the fallback it sanctions and for a push under the user's false.
func TestAPusherMeetingAnOldHookFailsClosedAndNamesTheReinstall(t *testing.T) {
	for _, test := range oldHookTests {
		t.Run(test.Name, func(t *testing.T) {
			pw := newPushWorld(t)

			if test.Global != "" {
				global := filepath.Join(t.TempDir(), ".gitconfig")
				writeFile(t, global, test.Global)
				t.Setenv("GIT_CONFIG_GLOBAL", global)
			}

			if test.Refuse {
				gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "false")
			}

			pw.claimSvc()
			pw.realHook(true)

			before := pw.remoteMain()
			res := pw.push()

			wantStatus(t, res, exitcode.Refused)
			wantContains(t, "stderr", res.Stderr, "nitipan setup git --install", "lacks the fence refs")
			wantContains(t, "stderr", res.Stderr, test.Want...)

			if got := pw.remoteMain(); got != before {
				t.Errorf("remote main moved to %s from %s", got, before)
			}
		})
	}
}

// The same hook, current and with the variable it is given, accepts the fallback and runs its own lease
// check on it. This is the other half of the skew test, and it proves the hook binary of this fixture
// does recognize the agent.
func TestTheCurrentHookAcceptsTheSanctionedFallbackAndRunsItsOwnLeaseCheck(t *testing.T) {
	pw := newPushWorld(t)
	gitIn(t, pw.bare, "config", "receive.advertiseAtomic", "false")

	pw.claimSvc()
	pw.realHook(false)

	res := pw.push()
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "origin does not support atomic push", "lease check and not fencing")

	if got, want := pw.remoteMain(), gitIn(t, pw.repo1, "rev-parse", "main"); got != want {
		t.Errorf("remote main = %s, want %s", got, want)
	}
}

type hookModeTest struct {
	Name string

	// Setup is the shell the user runs, in the repository, to set the modes.
	Setup string

	// Push is how the agent pushes. It can set the fallback variable or point git at a global file.
	Push string

	// Want is what stderr must say, and the push must fail. The rig has no broker, so a lease check ends
	// at the broker and a refusal ends at the missing fence refs.
	Want []string

	// Once is a line that stderr must carry exactly once.
	Once string
}

const (
	hookLeaseCheck = "so the push gets a lease check and not fencing"
	hookRefuses    = "lacks the fence refs"
	hookPush       = "git push origin main"
)

var hookModeTests = []hookModeTest{
	{
		Name: "unset with no variable refuses the fence-less push",
		Push: hookPush,
		Want: []string{hookRefuses},
	},
	{
		Name: "unset with the variable naming the remote runs the lease check",
		Push: "NITIPAN_FENCE_FALLBACK=origin " + hookPush,
		Want: []string{hookLeaseCheck, "nitipan push chose a lease check for this push after origin refused the fence"},
	},
	{
		Name: "unset with the variable naming another remote refuses",
		Push: "NITIPAN_FENCE_FALLBACK=upstream " + hookPush,
		Want: []string{hookRefuses},
	},
	{
		Name:  "true refuses even with the variable",
		Setup: `git config nitipan.origin.fence true`,
		Push:  "NITIPAN_FENCE_FALLBACK=origin " + hookPush,
		Want:  []string{hookRefuses},
	},
	{
		Name:  "false runs the lease check and names the key",
		Setup: `git config nitipan.origin.fence false`,
		Push:  hookPush,
		Want:  []string{hookLeaseCheck, "nitipan.origin.fence is false"},
	},
	{
		Name:  "false in the global config runs the lease check",
		Setup: `printf '[nitipan "origin"]\n\tfence = false\n' > "$HOME/.gitconfig"`,
		Push:  `GIT_CONFIG_GLOBAL="$HOME/.gitconfig" ` + hookPush,
		Want:  []string{hookLeaseCheck, "nitipan.origin.fence is false"},
	},
	{
		Name:  "the old key acts as false",
		Setup: `git config nitipan.origin.atomic false`,
		Push:  hookPush,
		Want:  []string{hookLeaseCheck, "nitipan.origin.atomic is false, which acts as nitipan.origin.fence=false", "nitipan setup git --install"},
		Once:  "nitipan.origin.atomic is false in",
	},
	{
		Name:  "a fence of true beats a leftover old key",
		Setup: `git config nitipan.origin.atomic false; git config nitipan.origin.fence true`,
		Push:  hookPush,
		Want:  []string{hookRefuses, "nitipan.origin.fence wins"},
		Once:  "nitipan.origin.atomic is false in",
	},
	{
		Name:  "a remote named with a slash keeps its own mode",
		Setup: `git config nitipan.team/origin.fence false`,
		Push:  "git push team/origin main",
		Want:  []string{hookLeaseCheck, "nitipan.team/origin.fence is false"},
	},
	{
		Name:  "an invalid fence value fails the hook and names the key and file",
		Setup: `git config nitipan.origin.fence maybe`,
		Push:  hookPush,
		Want:  []string{"nitipan.origin.fence", `"maybe"`, ".git/config", "not a boolean"},
	},
	{
		Name:  "an invalid old value fails the hook and names the key and file",
		Setup: `git config nitipan.origin.atomic perhaps`,
		Push:  hookPush,
		Want:  []string{"nitipan.origin.atomic", `"perhaps"`, ".git/config", "not a boolean"},
	},
}

// The rig is slow to build, so every row shares one repository and an agent, and starts from no mode keys.
func TestThePrePushHookReadsTheFenceModeAndAcceptsOnlyTheSanctionedFallback(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))
	r.must(r.user(`cd "$REPO"; git remote add team/origin "$ROOT/origin.git"`))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

	for _, test := range hookModeTests {
		t.Run(test.Name, func(t *testing.T) {
			r.must(r.user(`cd "$REPO"
for key in nitipan.origin.fence nitipan.origin.atomic nitipan.team/origin.fence; do git config --unset-all "$key"; done
rm -f "$HOME/.gitconfig"
` + test.Setup + "\ntrue\n"))

			res := alpha.run(test.Push)
			if res.Status == 0 {
				t.Fatalf("the push must fail\n%s%s", res.Stdout, res.Stderr)
			}

			for _, want := range test.Want {
				if !strings.Contains(res.Stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, res.Stderr)
				}
			}

			if test.Once != "" && strings.Count(res.Stderr, test.Once) != 1 {
				t.Errorf("stderr has %q %d times, want once:\n%s", test.Once, strings.Count(res.Stderr, test.Once), res.Stderr)
			}

			if got := r.fenceRefs(); got != "" {
				t.Errorf("a refused push left a fence behind: %q", got)
			}
		})
	}

	t.Run("the user's own push is never held to an invalid value or the notice", func(t *testing.T) {
		user := r.must(r.user(`cd "$REPO"
git config nitipan.origin.fence maybe
git config nitipan.origin.atomic false
echo y >> README
git commit -qam u
git push origin main 2>&1
`))

		if strings.Contains(user.Stdout+user.Stderr, "nitipan.origin") {
			t.Errorf("the user's own push printed about the keys:\n%s%s", user.Stdout, user.Stderr)
		}
	})
}

type slashRemoteTest struct {
	Name string

	// Value is what the remote's fence key is set to.
	Value string

	Want fenceMode
}

var slashRemoteTests = []slashRemoteTest{
	{Name: "true", Value: "true", Want: fenceTrue},
	{Name: "false", Value: "false", Want: fenceFalse},
}

// A remote name with a slash is a name git can hold a key for. Its mode must be read like any other. A
// reader that treats a name holding any of ':/@ ' as unset would fail this, and the pusher and the hook
// would then fence a remote whose mode says otherwise.
func TestTheModeReaderHonorsARemoteNamedWithASlash(t *testing.T) {
	for _, test := range slashRemoteTests {
		t.Run(test.Name, func(t *testing.T) {
			dir := t.TempDir()
			gitIn(t, dir, "init")
			gitIn(t, dir, "config", "nitipan.team/origin.fence", test.Value)

			g := (&app{deps: Deps{}.withDefaults()}).newPushGit(true, dir)

			got, err := readFenceSetting(g, "team/origin")
			if err != nil {
				t.Fatal(err)
			}

			if got.Mode != test.Want {
				t.Errorf("mode = %v, want %v", got.Mode, test.Want)
			}
		})
	}
}
