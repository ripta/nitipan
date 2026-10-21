package cli

import (
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

	args := []string{"acquire", "--checkout", os.Getenv(childCheckEnv)}
	if n%2 == 1 {
		args = append(args, "--scope", "sub")
	}

	return Execute(deps, args)
}

type childResult struct {
	status int
	output string
}

func startRaceChild(t *testing.T, idx int, stateDir, checkout string, release <-chan struct{}) <-chan childResult {
	t.Helper()

	done := make(chan childResult, 1)

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		childEnv+"="+strconv.Itoa(idx),
		childStateEnv+"="+stateDir,
		childCheckEnv+"="+checkout,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting child %d: %v", idx, err)
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
			results = append(results, startRaceChild(t, i, stateDir, checkout, release))
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
