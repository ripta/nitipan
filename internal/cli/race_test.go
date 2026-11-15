package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const (
	childEnv       = "NITIPAN_TEST_RACE_CHILD"
	childStateEnv  = "NITIPAN_TEST_RACE_STATE"
	childCheckEnv  = "NITIPAN_TEST_RACE_CHECKOUT"
	childSlotEnv   = "NITIPAN_TEST_RACE_RESOURCE"
	childWaitEnv   = "NITIPAN_TEST_RACE_WAIT"
	childArgsEnv   = "NITIPAN_TEST_RACE_ARGS"
	childAsEnv     = "NITIPAN_TEST_RACE_AS"
	waitingLine    = "race child: attempt refused, waiting"
	raceChildren   = 8
	raceRounds     = 6
	childAgentBase = 1000
	childShellPID  = 60
)

// TestMain doubles as the entry point of the processes TestAcquireRace starts. The child blocks
// until its stdin closes, so every child begins its acquire at about the same moment.
func TestMain(m *testing.M) {
	if idx := os.Getenv(childEnv); idx != "" {
		os.Exit(runRaceChild(idx))
	}

	os.Exit(m.Run())
}

// runRaceChild runs one acquire as one of several holders. Every child's fake process table holds
// every child's agent as alive, so a child that loses sees the winner as a live holder.
func runRaceChild(idx string) int {
	n, err := strconv.Atoi(idx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad child index", idx)

		return 99
	}

	// Several processes can act as one holder, which a grant race needs.
	if as := os.Getenv(childAsEnv); as != "" {
		if n, err = strconv.Atoi(as); err != nil {
			fmt.Fprintln(os.Stderr, "bad child identity", as)

			return 99
		}

		idx = as
	}

	_, _ = io.Copy(io.Discard, os.Stdin)

	table := proctest.New(record.PlatformDarwin, selfPID)
	table.Add(selfPID, proctest.Process{Start: startOf(selfPID), PPID: childShellPID})

	for j := 0; j < raceChildren; j++ {
		table.Add(childAgentBase+j, proctest.Process{Start: startOf(childAgentBase + j), PPID: 1, Name: "claude"})
	}

	table.Add(childShellPID, proctest.Process{Start: startOf(childShellPID), PPID: childAgentBase + n, Name: "zsh"})

	env := map[string]string{
		"CLAUDE_PID":             strconv.Itoa(childAgentBase + n),
		"CLAUDE_CODE_SESSION_ID": "session-" + idx,
	}

	deps := Deps{
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Getenv:       func(k string) string { return env[k] },
		Procs:        table,
		StateDir:     func() (string, error) { return os.Getenv(childStateEnv), nil },
		Machine:      func() (record.Machine, error) { return testMachine, nil },
		User:         func() (string, error) { return tester, nil },
		StateOptions: state.Options{LockBudget: 20 * time.Second},
	}

	// A wait pauses only after an attempt was refused, so this line is evidence that the waiter
	// tried and is now waiting.
	if os.Getenv(childWaitEnv) != "" {
		deps.Sleep = func(d time.Duration) {
			fmt.Fprintln(os.Stderr, waitingLine)
			time.Sleep(d)
		}
	}

	args := []string{"acquire", "--checkout", os.Getenv(childCheckEnv)}
	if n%2 == 1 {
		args = append(args, "--scope", "sub")
	}

	if resource := os.Getenv(childSlotEnv); resource != "" {
		args = []string{"acquire", "--resource", resource}
	}

	if wait := os.Getenv(childWaitEnv); wait != "" {
		args = append(args, "--wait", wait)
	}

	if explicit := os.Getenv(childArgsEnv); explicit != "" {
		args = strings.Split(explicit, "\x1f")
	}

	return Execute(deps, args)
}

type childResult struct {
	status int
	output string
}

func startRaceChild(t *testing.T, idx int, stateDir, checkout, resource string, release <-chan struct{}) <-chan childResult {
	t.Helper()

	return startRaceProcess(t, release,
		childEnv+"="+strconv.Itoa(idx),
		childStateEnv+"="+stateDir,
		childCheckEnv+"="+checkout,
		childSlotEnv+"="+resource,
	)
}

// startRaceCommand starts one process that runs the given command as holder number as.
func startRaceCommand(t *testing.T, as int, stateDir string, args []string, release <-chan struct{}) <-chan childResult {
	t.Helper()

	return startRaceProcess(t, release,
		childEnv+"="+strconv.Itoa(as),
		childAsEnv+"="+strconv.Itoa(as),
		childStateEnv+"="+stateDir,
		childArgsEnv+"="+strings.Join(args, "\x1f"),
	)
}

func startRaceProcess(t *testing.T, release <-chan struct{}, env ...string) <-chan childResult {
	t.Helper()

	done := make(chan childResult, 1)

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a race process: %v", err)
	}

	go func() {
		<-release
		_ = stdin.Close()

		status := 0

		if err := cmd.Wait(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				status = -1
			} else {
				status = exit.ExitCode()
			}
		}

		done <- childResult{status: status, output: out.String()}
	}()

	return done
}

func TestAcquireRaceNeverLetsTwoOverlappingClaimsSucceed(t *testing.T) {
	for round := 0; round < raceRounds; round++ {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		checkout := filepath.Join(root, "repo")
		if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}

		stateDir := filepath.Join(root, "state")
		release := make(chan struct{})

		var results []<-chan childResult
		for i := 0; i < raceChildren; i++ {
			results = append(results, startRaceChild(t, i, stateDir, checkout, "", release))
		}

		close(release)

		var (
			winners int
			refused int
			dump    strings.Builder
		)

		for i, ch := range results {
			res := <-ch
			fmt.Fprintf(&dump, "child %d: status %d\n%s\n", i, res.status, res.output)

			switch res.status {
			case exitcode.OK:
				winners++
			case exitcode.Refused:
				refused++
			}
		}

		if winners != 1 || refused != raceChildren-1 {
			t.Fatalf("round %d: %d winners and %d refusals among %d overlapping claims\n%s",
				round, winners, refused, raceChildren, dump.String())
		}

		entries, err := os.ReadDir(filepath.Join(stateDir, state.LeasesDir))
		if err != nil {
			t.Fatal(err)
		}

		published := 0

		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".tmp-") {
				published++
			}
		}

		if published != 1 {
			t.Errorf("round %d: %d records published, want 1", round, published)
		}
	}
}

func TestAcquireRaceNeverExceedsTheSlotCount(t *testing.T) {
	const slots = 2

	for round := 0; round < raceRounds; round++ {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		stateDir := filepath.Join(root, "state")
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}

		config := fmt.Sprintf(`{"version": 1, "slots": {"gpu:main": %d}}`, slots)
		if err := os.WriteFile(filepath.Join(stateDir, "slots.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}

		release := make(chan struct{})

		var results []<-chan childResult
		for i := 0; i < raceChildren; i++ {
			results = append(results, startRaceChild(t, i, stateDir, "", "gpu:main", release))
		}

		close(release)

		var (
			winners int
			refused int
			dump    strings.Builder
		)

		for i, ch := range results {
			res := <-ch
			fmt.Fprintf(&dump, "child %d: status %d\n%s\n", i, res.status, res.output)

			switch res.status {
			case exitcode.OK:
				winners++
			case exitcode.Refused:
				refused++
			}
		}

		if winners != slots || refused != raceChildren-slots {
			t.Fatalf("round %d: %d winners and %d refusals among %d claims on a slot of %d\n%s",
				round, winners, refused, raceChildren, slots, dump.String())
		}

		entries, err := os.ReadDir(filepath.Join(stateDir, state.LeasesDir))
		if err != nil {
			t.Fatal(err)
		}

		published := 0

		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".tmp-") {
				published++
			}
		}

		if published != slots {
			t.Errorf("round %d: %d records published, want %d", round, published, slots)
		}
	}
}

// raceOnce runs one command as one holder, alone, and returns how it ended.
func raceOnce(t *testing.T, as int, stateDir string, args ...string) childResult {
	t.Helper()

	release := make(chan struct{})
	done := startRaceCommand(t, as, stateDir, args, release)

	close(release)

	return <-done
}

// TestSubLeaseRaceNeverLetsTwoOverlappingGrantsOrClaimsSucceed races grants of overlapping requests,
// all run by the parent's holder, against claims by other holders on the same keys. Exactly one grant
// may win, and no claim may.
func TestSubLeaseRaceNeverLetsTwoOverlappingGrantsOrClaimsSucceed(t *testing.T) {
	const (
		requesters = 4
		claimers   = 3
	)

	for round := 0; round < raceRounds/2; round++ {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		checkout := filepath.Join(root, "repo")
		if err := os.MkdirAll(filepath.Join(checkout, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}

		stateDir := filepath.Join(root, "state")

		if res := raceOnce(t, 0, stateDir, "acquire", "--checkout", checkout); res.status != exitcode.OK {
			t.Fatalf("round %d: the parent's acquire exited %d\n%s", round, res.status, res.output)
		}

		parentID := onlyLeaseID(t, stateDir)

		var requests []string

		for i := 1; i <= requesters; i++ {
			res := raceOnce(t, i, stateDir, "request", parentID, "--checkout", checkout, "--scope", "sub")
			if res.status != exitcode.OK {
				t.Fatalf("round %d: request %d exited %d\n%s", round, i, res.status, res.output)
			}

			first, _, _ := strings.Cut(res.output, "\n")
			requests = append(requests, strings.TrimPrefix(first, "requested "))
		}

		release := make(chan struct{})

		var (
			grants []<-chan childResult
			claims []<-chan childResult
		)

		for _, id := range requests {
			grants = append(grants, startRaceCommand(t, 0, stateDir, []string{"grant", id}, release))
		}

		for i := 0; i < claimers; i++ {
			args := []string{"acquire", "--checkout", checkout, "--scope", "sub"}
			claims = append(claims, startRaceCommand(t, requesters+1+i, stateDir, args, release))
		}

		close(release)

		var (
			granted int
			dump    strings.Builder
		)

		for i, ch := range grants {
			res := <-ch
			fmt.Fprintf(&dump, "grant %d: status %d\n%s\n", i, res.status, res.output)

			switch res.status {
			case exitcode.OK:
				granted++
			case exitcode.Refused:
			default:
				t.Errorf("round %d: grant %d exited %d, want ok or refused\n%s", round, i, res.status, res.output)
			}
		}

		for i, ch := range claims {
			res := <-ch
			fmt.Fprintf(&dump, "claim %d: status %d\n%s\n", i, res.status, res.output)

			if res.status != exitcode.Refused {
				t.Errorf("round %d: a claim on a sub-leased part exited %d, want refused\n%s", round, res.status, res.output)
			}
		}

		if granted != 1 {
			t.Fatalf("round %d: %d grants of %d overlapping requests succeeded, want 1\n%s", round, granted, requesters, dump.String())
		}

		published := 0

		entries, err := os.ReadDir(filepath.Join(stateDir, state.LeasesDir))
		if err != nil {
			t.Fatal(err)
		}

		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".tmp-") {
				published++
			}
		}

		if published != 2 {
			t.Errorf("round %d: %d records published, want the parent and one sub-lease\n%s", round, published, dump.String())
		}
	}
}

// onlyLeaseID returns the ID of the single lease in the state directory.
func onlyLeaseID(t *testing.T, stateDir string) string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(stateDir, state.LeasesDir))
	if err != nil {
		t.Fatal(err)
	}

	var ids []string

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}

	if len(ids) != 1 {
		t.Fatalf("leases = %v, want exactly one", ids)
	}

	return ids[0]
}

// stateNames lists every path under the state directory, relative to it.
func stateNames(t *testing.T, stateDir string) []string {
	t.Helper()

	var names []string

	err := filepath.WalkDir(stateDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(stateDir, path)
		if err != nil {
			return err
		}

		names = append(names, rel)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return names
}

func TestWaiterKilledMidWaitLeavesNothingInTheStateDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(root, "state")

	release := make(chan struct{})
	holder := startRaceChild(t, 0, stateDir, "", "cpu:heavy", release)

	close(release)

	if res := <-holder; res.status != exitcode.OK {
		t.Fatalf("the holder's acquire exited %d\n%s", res.status, res.output)
	}

	before := stateNames(t, stateDir)

	t.Setenv(childWaitEnv, "60s")

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		childEnv+"=1",
		childStateEnv+"="+stateDir,
		childCheckEnv+"=",
		childSlotEnv+"=cpu:heavy",
	)

	var out strings.Builder

	cmd.Stdout = &out
	cmd.Stdin = strings.NewReader("")

	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	waiting := make(chan struct{})

	go func() {
		scanner := bufio.NewScanner(stderr)
		announced := false

		for scanner.Scan() {
			if !announced && scanner.Text() == waitingLine {
				announced = true

				close(waiting)
			}
		}
	}()

	select {
	case <-waiting:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()

		t.Fatalf("the waiter never reported a refused attempt\n%s", out.String())
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err == nil {
		t.Error("the waiter exited cleanly after a kill")
	}

	after := stateNames(t, stateDir)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("the state directory changed while a waiter ran and died:\nbefore %v\nafter  %v", before, after)
	}
}
