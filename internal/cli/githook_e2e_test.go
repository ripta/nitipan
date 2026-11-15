package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

// These tests install the real hook into real repositories and drive real git. The test binary acts
// as nitipan when the environment asks it to, so the installed script bakes its path. Each agent is
// a long-lived shell whose PID is its CLAUDE_PID, so the process walk finds it above every git
// command it runs, and two agents are two holders.

const (
	asNitipanEnv = "NITIPAN_TEST_AS_NITIPAN"
	asStateEnv   = "NITIPAN_TEST_AS_STATE"
	asHomeEnv    = "NITIPAN_TEST_AS_HOME"

	// asAgentPIDEnv makes the test binary see the process table of the wide fixture: an agent at this PID,
	// its shell above the binary, and the fixture's node. asMachineEnv names the machine the fixture gave
	// that agent. The wide fixture's agents are not real processes, so a hook that runs as a real process
	// needs this table to be told apart from the user.
	asAgentPIDEnv = "NITIPAN_TEST_AS_AGENT_PID"
	asMachineEnv  = "NITIPAN_TEST_AS_MACHINE"

	shellPrelude = "nit() { \"$NITIPAN\" \"$@\"; }\n"
	shellTimeout = 2 * time.Minute
)

// runAsNitipan is the test binary acting as the nitipan command.
func runAsNitipan() int {
	deps := Deps{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Stdin:    os.Stdin,
		Getenv:   os.Getenv,
		Procs:    proc.System(),
		StateDir: func() (string, error) { return os.Getenv(asStateEnv), nil },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}

	if pid, err := strconv.Atoi(os.Getenv(asAgentPIDEnv)); err == nil {
		id := os.Getenv(asMachineEnv)
		self := os.Getpid()

		table := proctest.New(record.PlatformDarwin, self)
		table.Add(pid, proctest.Process{Start: startOf(pid), PPID: 1, Name: "claude"})
		table.Add(pid+1, proctest.Process{Start: startOf(pid + 1), PPID: pid, Name: "zsh"})
		table.Add(self, proctest.Process{Start: startOf(self), PPID: pid + 1})
		table.Add(nodePID, proctest.Process{Start: startOf(nodePID)})

		deps.Procs = table
		deps.User = func() (string, error) { return tester, nil }
		deps.Machine = func() (record.Machine, error) { return record.Machine{Host: "host-" + id, ID: id}, nil }
	}

	if home := os.Getenv(asHomeEnv); home != "" {
		deps.UserHome = func() (string, error) { return home, nil }
	}

	return Execute(deps, os.Args[1:])
}

type shellResult struct {
	Status int
	Stdout string
	Stderr string
}

// rig is a repository with the hook installed and a state directory of its own.
type rig struct {
	t       *testing.T
	root    string
	repo    string
	state   string
	scratch string
	bin     string
	home    string
}

func newRig(t *testing.T) *rig {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	r := &rig{
		t:       t,
		root:    root,
		repo:    filepath.Join(root, "repo"),
		state:   filepath.Join(root, "state"),
		scratch: filepath.Join(root, "scratch"),
		bin:     bin,
		home:    filepath.Join(root, "home"),
	}

	for _, dir := range []string{r.scratch, r.home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r.must(r.user(`
set -e
git init -q -b main "$REPO"
cd "$REPO"
mkdir src docs
echo a > src/a.txt
echo b > docs/b.txt
echo r > README
git add -A
git commit -qm base
nit setup git --install "$REPO" > /dev/null
`))

	return r
}

// env builds a shell's environment from nothing, so no agent variable of the machine running the
// tests leaks in.
func (r *rig) env(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.home,
		"ROOT=" + r.root,
		"REPO=" + r.repo,
		"SCRATCH=" + r.scratch,
		"NITIPAN=" + r.bin,
		asNitipanEnv + "=1",
		asStateEnv + "=" + r.state,
		asHomeEnv + "=" + r.home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=protocol.file.allow",
		"GIT_CONFIG_VALUE_0=always",
	}

	return append(env, extra...)
}

func (r *rig) script(name, body, cd string) string {
	r.t.Helper()

	file := filepath.Join(r.scratch, name+".sh")
	if err := os.WriteFile(file, []byte(shellPrelude+"cd "+cd+"\n"+body), 0o755); err != nil {
		r.t.Fatal(err)
	}

	return file
}

var scriptSeq int

func nextSeq() int {
	scriptSeq++

	return scriptSeq
}

// user runs a script the way the user's own terminal would: no agent above it.
func (r *rig) user(body string) shellResult {
	r.t.Helper()

	file := r.script(fmt.Sprintf("user-%d", nextSeq()), body, `"$ROOT"`)

	cmd := exec.Command("sh", file)
	cmd.Env = r.env()

	var stdout, stderr strings.Builder

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := shellResult{Stdout: stdout.String(), Stderr: stderr.String()}

	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			r.t.Fatalf("running a script: %v", err)
		}

		res.Status = exit.ExitCode()
	}

	return res
}

func (r *rig) must(res shellResult) shellResult {
	r.t.Helper()

	if res.Status != 0 {
		r.t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.Status, res.Stdout, res.Stderr)
	}

	return res
}

// agentShell is one agent session: a shell that exports its own PID as CLAUDE_PID and runs the
// scripts it is given, one at a time.
type agentShell struct {
	r    *rig
	name string
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	pid  int
	mu   sync.Mutex
}

func (r *rig) agent(name string) *agentShell {
	r.t.Helper()

	cmd := exec.Command("sh", "-c", `export CLAUDE_PID=$$; echo "pid $$"; while IFS= read -r line; do eval "$line"; done`)
	cmd.Env = r.env("CLAUDE_CODE_SESSION_ID=" + name)
	cmd.Dir = r.root

	in, err := cmd.StdinPipe()
	if err != nil {
		r.t.Fatal(err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		r.t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}

	a := &agentShell{r: r, name: name, cmd: cmd, in: in, out: bufio.NewReader(out)}

	r.t.Cleanup(a.stop)

	line, err := a.out.ReadString('\n')
	if err != nil {
		r.t.Fatalf("starting agent %s: %v", name, err)
	}

	if a.pid, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid "))); err != nil {
		r.t.Fatalf("agent %s said %q", name, line)
	}

	return a
}

func (a *agentShell) stop() {
	_ = a.in.Close()
	_ = a.cmd.Process.Kill()
	_ = a.cmd.Wait()
}

// run runs a script under the agent, in the repository.
func (a *agentShell) run(body string) shellResult {
	a.r.t.Helper()

	a.mu.Lock()
	defer a.mu.Unlock()

	file := a.r.script(fmt.Sprintf("%s-%d", a.name, nextSeq()), body, `"$REPO"`)
	outFile, errFile := file+".out", file+".err"

	fmt.Fprintf(a.in, "sh '%s' >'%s' 2>'%s'; echo \"done $?\"\n", file, outFile, errFile)

	done := make(chan string, 1)

	go func() {
		for {
			line, err := a.out.ReadString('\n')
			if err != nil {
				done <- "failed " + err.Error()

				return
			}

			if strings.HasPrefix(line, "done ") {
				done <- strings.TrimSpace(line)

				return
			}
		}
	}()

	var line string

	select {
	case line = <-done:
	case <-time.After(shellTimeout):
		a.r.t.Fatalf("agent %s did not finish %s", a.name, file)
	}

	status, err := strconv.Atoi(strings.TrimPrefix(line, "done "))
	if err != nil {
		a.r.t.Fatalf("agent %s: %s", a.name, line)
	}

	stdout, _ := os.ReadFile(outFile)
	stderr, _ := os.ReadFile(errFile)

	return shellResult{Status: status, Stdout: string(stdout), Stderr: string(stderr)}
}

var leaseIDPattern = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)

// claim acquires a scope of the repository, or the whole checkout, and returns the lease ID.
func (a *agentShell) claim(scope string) string {
	a.r.t.Helper()

	cmd := `nit acquire --checkout "$REPO" --name ` + a.name
	if scope != "" {
		cmd += " --scope " + scope
	}

	res := a.r.must(a.run(cmd))

	id := leaseIDPattern.FindString(res.Stdout)
	if id == "" {
		a.r.t.Fatalf("acquire printed no lease ID:\n%s", res.Stdout)
	}

	return id
}

// refs returns the commit each named ref holds, or "none".
func (r *rig) refs(names ...string) []string {
	r.t.Helper()

	var out []string

	for _, n := range names {
		res := r.user(`cd "$REPO"; git rev-parse --verify -q ` + n + ` || echo none`)
		out = append(out, strings.TrimSpace(res.Stdout))
	}

	return out
}

type channelTest struct {
	Name string

	// Setup runs as the user, who is never refused. SetupAlpha runs as alpha, after it.
	Setup      string
	SetupAlpha string

	// Run is what beta does, and the hook must refuse it.
	Run string

	// Want is what the refusal says.
	Want []string

	// Unchanged names the refs that must hold the same commit afterwards.
	Unchanged []string
}

const sideBranch = `
git checkout -q -b side
echo s >> src/a.txt
git commit -qam side
git checkout -q main
`

const siblingWorktree = sideBranch + `
git branch feat main
git worktree add -q "$ROOT/wt2" feat
`

var channelTests = []channelTest{
	{
		Name: "a commit",
		Run:  `echo x >> src/a.txt; git commit -qam c`,
		Want: []string{"git refused to update refs/heads/main", "src/a.txt", "is held by", "agent name \"alpha\""},

		Unchanged: []string{"main"},
	},
	{
		Name:      "reset --hard over a commit that touches another holder's keys",
		Setup:     `cd "$REPO"; echo more >> src/a.txt; git commit -qam usercommit`,
		Run:       `git reset -q --hard HEAD~1`,
		Want:      []string{"git refused to update refs/heads/main", "src/a.txt"},
		Unchanged: []string{"main"},
	},
	{
		Name:       "update-ref on a branch checked out in a sibling worktree",
		Setup:      `cd "$REPO"; ` + siblingWorktree,
		SetupAlpha: `nit acquire --checkout "$ROOT/wt2" --name alpha`,
		Run:        `git update-ref refs/heads/feat side`,
		Want:       []string{"git refused to update refs/heads/feat", "wt2/src/a.txt"},
		Unchanged:  []string{"feat"},
	},
	{
		Name: "branch -f off a branch's unique commits",
		Setup: `cd "$REPO"
git checkout -q -b feat
echo f >> src/a.txt
git commit -qam f
git checkout -q main`,
		Run:       `git branch -f feat main`,
		Want:      []string{"git refused to update refs/heads/feat", "src/a.txt"},
		Unchanged: []string{"feat"},
	},
	{
		Name:      "update-ref in the same worktree",
		Setup:     `cd "$REPO"; ` + sideBranch,
		Run:       `git update-ref refs/heads/main side`,
		Want:      []string{"git refused to update refs/heads/main", "src/a.txt"},
		Unchanged: []string{"main"},
	},
	{
		Name:      "reset --soft in the same worktree",
		Setup:     `cd "$REPO"; ` + sideBranch,
		Run:       `git reset -q --soft side`,
		Want:      []string{"git refused to update refs/heads/main", "src/a.txt"},
		Unchanged: []string{"main"},
	},
	{
		Name:      "a commit on a detached HEAD",
		Run:       `git checkout -q --detach; echo x >> src/a.txt; git commit -qam d`,
		Want:      []string{"git refused to move HEAD", "src/a.txt"},
		Unchanged: []string{"HEAD", "main"},
	},
	{
		Name: "a branch deletion",
		Setup: `cd "$REPO"
git checkout -q -b gone
echo g >> src/a.txt
git commit -qam g
git checkout -q main`,
		Run:       `git branch -D gone`,
		Want:      []string{"git refused to update refs/heads/gone", "src/a.txt"},
		Unchanged: []string{"gone"},
	},
	{
		Name: "a commit in a linked worktree",
		Setup: `cd "$REPO"
git worktree add -q "$ROOT/wt" -b wtb`,
		SetupAlpha: `nit acquire --checkout "$ROOT/wt" --name alpha`,
		Run:        `cd "$ROOT/wt"; echo x >> src/a.txt; git commit -qam c`,
		Want:       []string{"git refused to update refs/heads/wtb", "wt/src/a.txt"},
		Unchanged:  []string{"wtb"},
	},
}

func newPairedRig(t *testing.T) (r *rig, alpha, beta *agentShell, alphaID, betaID string) {
	t.Helper()

	r = newRig(t)
	alpha, beta = r.agent("alpha"), r.agent("beta")
	alphaID, betaID = alpha.claim("src"), beta.claim("docs")

	return r, alpha, beta, alphaID, betaID
}

func TestTheHookRefusesEveryChannelToTouchAnotherHoldersKeys(t *testing.T) {
	for _, test := range channelTests {
		t.Run(test.Name, func(t *testing.T) {
			r, alpha, beta, alphaID, _ := newPairedRig(t)

			if test.Setup != "" {
				r.must(r.user(test.Setup))
			}

			if test.SetupAlpha != "" {
				r.must(alpha.run(test.SetupAlpha))
			}

			before := r.refs(test.Unchanged...)

			res := beta.run(test.Run)
			if res.Status == 0 {
				t.Fatalf("beta got through\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
			}

			wantContains(t, "stderr", res.Stderr, test.Want...)

			if test.Name == "a commit" {
				wantContains(t, "stderr", res.Stderr, alphaID, "Coordinate with the holder or ask the user")
			}

			if after := r.refs(test.Unchanged...); strings.Join(after, " ") != strings.Join(before, " ") {
				t.Errorf("refs %v moved from %v to %v", test.Unchanged, before, after)
			}
		})
	}
}

func TestAFinishedRebaseIsRefusedAtItsFinalBranchUpdate(t *testing.T) {
	r, _, beta, _, betaID := newPairedRig(t)

	r.must(r.user(`cd "$REPO"
git checkout -q -b feat main
echo f >> docs/b.txt
git commit -qam f
git checkout -q main
echo m >> README
git commit -qam m`))

	tip := r.refs("feat")[0]

	r.must(r.user(`cat > "$SCRATCH/seq.sh" <<'EOF'
printf 'pick %s\nexec sh "$SCRATCH/wait.sh"\n' "$(git rev-parse feat)" > "$1"
EOF
cat > "$SCRATCH/wait.sh" <<'EOF'
touch "$SCRATCH/ready"
while [ ! -f "$SCRATCH/go" ]; do sleep 0.05; done
EOF`))

	results := make(chan shellResult, 1)

	go func() {
		results <- beta.run(`git checkout -q feat
GIT_SEQUENCE_EDITOR="sh $SCRATCH/seq.sh" git rebase -i main`)
	}()

	waitForFile(t, filepath.Join(r.scratch, "ready"))

	// Every replayed commit passed, since docs is beta's. Now the user breaks beta's lease, so only
	// the final branch update is left to judge.
	r.must(r.user(`nit break --force ` + betaID + `; touch "$SCRATCH/go"`))

	res := <-results
	if res.Status == 0 {
		t.Fatalf("the rebase finished\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	}

	wantContains(t, "stderr", res.Stderr, "git refused to update refs/heads/feat", "fence mark", "Report this to the user")

	if got := r.refs("feat")[0]; got != tip {
		t.Errorf("feat moved from %s to %s", tip, got)
	}

	if _, err := os.Stat(filepath.Join(r.repo, ".git", "rebase-merge")); err != nil {
		t.Errorf("the rebase is not left in progress: %v", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(shellTimeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s never appeared", path)
}

func TestAPinnedSalvageCommitLandedOnABranchIsStillChecked(t *testing.T) {
	r, _, beta, _, _ := newPairedRig(t)

	// Some git versions put HEAD in the transaction beside the branch, so the refusal can carry
	// one salvage hint per ref. They all name the same commit, and one pin is enough here.
	res := beta.run(`echo x >> src/a.txt
git commit -qam c 2> "$SCRATCH/err" && exit 90
cmd=$(sed -n 's/.*run: \(git update-ref refs\/nitipan\/salvage\/[A-Z0-9]* [0-9a-f]*\).*/\1/p' "$SCRATCH/err" | head -n 1)
test -n "$cmd" || { cat "$SCRATCH/err"; exit 91; }
$cmd || exit 92
set -- $cmd
git branch landed "$4"`)

	if res.Status == 0 || res.Status >= 90 && res.Status <= 92 {
		t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.Status, res.Stdout, res.Stderr)
	}

	wantContains(t, "stderr", res.Stderr, "git refused to update refs/heads/landed", "src/a.txt")

	if got := r.refs("landed")[0]; got != "none" {
		t.Errorf("landed holds %s", got)
	}

	pinned := r.must(r.user(`cd "$REPO"; git for-each-ref refs/nitipan/salvage`)).Stdout
	if !strings.Contains(pinned, "refs/nitipan/salvage/") {
		t.Errorf("the pin did not land: %q", pinned)
	}
}

type passTest struct {
	Name  string
	Setup string
	Run   string
}

const upstreamSetup = `cd "$REPO"
git init -q --bare -b main "$ROOT/origin.git"
git remote add origin "$ROOT/origin.git"
git push -q origin main
git fetch -q origin
git branch -q --set-upstream-to=origin/main main
git clone -q "$ROOT/origin.git" "$ROOT/up"
cd "$ROOT/up"
echo u >> src/a.txt
git commit -qam upstream
git push -q origin main
`

var passTests = []passTest{
	{Name: "a commit on beta's own keys", Run: `echo x >> docs/b.txt; git commit -qam c`},
	{Name: "a commit on paths no lease covers", Run: `echo x >> README; git commit -qam c`},
	{Name: "branches and tags that touch nothing", Run: `git branch feature; git tag t1; git branch -D feature`},
	{Name: "a checkout between branches", Run: `git checkout -q -b other; git checkout -q main; git branch -D other`},
	{
		Name: "gc and pack-refs over a branch whose commits touch another holder's keys",
		Setup: `cd "$REPO"
git checkout -q -b unique
echo u >> src/a.txt
git commit -qam u
git checkout -q main`,
		Run: `git pack-refs --all --prune; git gc -q; git rev-parse --verify -q unique`,
	},
	{Name: "a pull that brings in commits on another holder's keys", Setup: upstreamSetup, Run: `git pull -q --ff-only origin main`},
	{
		Name:  "a rebase onto upstream commits on another holder's keys",
		Setup: upstreamSetup,
		Run: `echo l >> docs/b.txt
git commit -qam local
git fetch -q origin
git rebase -q origin/main`,
	},
	{
		Name:  "a checkout of origin/main",
		Setup: upstreamSetup,
		Run:   `git fetch -q origin; git checkout -q origin/main; git checkout -q main`,
	},
}

func TestTheHookPassesWhatDoesNotCollide(t *testing.T) {
	for _, test := range passTests {
		t.Run(test.Name, func(t *testing.T) {
			r, _, beta, _, _ := newPairedRig(t)

			if test.Setup != "" {
				r.must(r.user(test.Setup))
			}

			res := beta.run(test.Run)
			if res.Status != 0 {
				t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.Status, res.Stdout, res.Stderr)
			}
		})
	}
}

func TestTheUsersOwnTerminalIsNeverRefused(t *testing.T) {
	r, _, _, _, _ := newPairedRig(t)

	r.must(r.user(`cd "$REPO"; echo x >> src/a.txt; git commit -qam c; git reset -q --hard HEAD~1`))
}

func TestARefusalNamesHowTheLeaseEndedAndNoCommand(t *testing.T) {
	r, alpha, _, alphaID, _ := newPairedRig(t)

	r.must(r.user(`nit break --force ` + alphaID))

	commit := `echo x >> src/a.txt; git commit -qam c`

	t.Run("with a fence mark", func(t *testing.T) {
		res := alpha.run(commit)
		if res.Status == 0 {
			t.Fatal("alpha got through")
		}

		wantContains(t, "stderr", res.Stderr, "was broken by an anonymous process", "A fence mark still guards",
			"Report this to the user")
		wantAbsent(t, "stderr", res.Stderr, "nitipan acquire", "nitipan request", "nitipan break", "--force")
	})

	t.Run("with only the tombstone, as an older binary leaves it", func(t *testing.T) {
		r.must(r.user(`rm "` + r.state + `"/marks/*.json`))

		res := alpha.run(`git checkout -q -- .; ` + commit)
		if res.Status == 0 {
			t.Fatal("alpha got through")
		}

		wantContains(t, "stderr", res.Stderr, "was broken by an anonymous process", "Your lease no longer exists",
			"Report this to the user")
		wantAbsent(t, "stderr", res.Stderr, "fence mark", "nitipan acquire", "nitipan request", "nitipan break")
	})
}

func TestARefusedCommitPrintsAPinThatWorks(t *testing.T) {
	r, _, beta, _, _ := newPairedRig(t)

	res := beta.run(`echo x >> src/a.txt; git commit -qam c`)
	if res.Status == 0 {
		t.Fatal("beta got through")
	}

	match := regexp.MustCompile(`git update-ref (refs/nitipan/salvage/[A-Z0-9]+) ([0-9a-f]{40})`).FindStringSubmatch(res.Stderr)
	if match == nil {
		t.Fatalf("no pin command in %q", res.Stderr)
	}

	pinned := beta.run(match[0])
	if pinned.Status != 0 {
		t.Fatalf("the pin failed: %s", pinned.Stderr)
	}

	got := r.must(r.user(`cd "$REPO"; git rev-parse ` + match[1])).Stdout
	if strings.TrimSpace(got) != match[2] {
		t.Errorf("the pin holds %s, want %s", got, match[2])
	}

	if show := r.must(r.user(`cd "$REPO"; git show --stat --format=%s ` + match[2])).Stdout; !strings.Contains(show, "src/a.txt") {
		t.Errorf("the pinned commit is not the refused one:\n%s", show)
	}
}

func TestARealDeleteOfALooseAndPackedBranchIsRefused(t *testing.T) {
	r, _, beta, _, _ := newPairedRig(t)

	r.must(r.user(`cd "$REPO"
git checkout -q -b unique
echo u >> src/a.txt
git commit -qam u
git checkout -q main
git pack-refs --all`))

	before := r.refs("unique")

	res := beta.run(`git branch -D unique`)
	if res.Status == 0 {
		t.Fatalf("beta deleted the branch\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	}

	wantContains(t, "stderr", res.Stderr, "git refused to update refs/heads/unique", "src/a.txt")

	if after := r.refs("unique"); after[0] != before[0] {
		t.Errorf("unique moved from %v to %v", before, after)
	}
}

func TestTheHookCreatesNothingInTheStateDirectory(t *testing.T) {
	r := newRig(t)
	gamma := r.agent("gamma")

	res := gamma.run(`echo x >> README; git commit -qam c`)
	if res.Status != 0 {
		t.Fatalf("status %d\nstdout: %s\nstderr: %s", res.Status, res.Stdout, res.Stderr)
	}

	if _, err := os.Stat(r.state); !os.IsNotExist(err) {
		t.Errorf("the hook created the state directory: %v", err)
	}
}
