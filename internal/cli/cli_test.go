package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

func testDeps() (deps Deps, stdout, stderr *bytes.Buffer) {
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}

	deps = Deps{
		Stdout:   stdout,
		Stderr:   stderr,
		Getenv:   func(string) string { return "" },
		Procs:    proctest.New(record.PlatformDarwin, 1),
		StateDir: func() (string, error) { return "", errors.New("test has no state directory") },
	}

	return deps, stdout, stderr
}

type executeTest struct {
	Name       string
	Args       []string
	WantStatus int
	WantErr    string
}

var executeTests = []executeTest{
	{Name: "version", Args: []string{"--version"}, WantStatus: exitcode.OK},
	{Name: "help", Args: []string{"--help"}, WantStatus: exitcode.OK},
	{Name: "unknown flag", Args: []string{"list", "--nope"}, WantStatus: exitcode.Usage, WantErr: "unknown flag"},
}

func TestExecute(t *testing.T) {
	for _, test := range executeTests {
		t.Run(test.Name, func(t *testing.T) {
			deps, _, stderr := testDeps()

			if got := Execute(deps, test.Args); got != test.WantStatus {
				t.Errorf("status = %d, want %d (stderr %q)", got, test.WantStatus, stderr)
			}

			if !strings.Contains(stderr.String(), test.WantErr) {
				t.Errorf("stderr %q does not contain %q", stderr, test.WantErr)
			}
		})
	}
}

var hookMistakes = [][]string{
	{"hook"},
	{"hook", "bogus"},
	{"hook", "session-start", "--nope"},
	{"hook", "session-start"},
	{"hook", "presence"},
	{"hook", "reference-transaction"},
	{"hook", "reference-transaction", "prepared", "extra"},
}

func TestHookMistakesDoNotNameHook(t *testing.T) {
	for _, args := range hookMistakes {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			deps, _, stderr := testDeps()

			if got := Execute(deps, args); got == exitcode.OK {
				t.Fatalf("status = %d", got)
			}

			if strings.Contains(stderr.String(), "hook") {
				t.Errorf("error %q names the hook command", stderr)
			}
		})
	}
}

func TestHelpListsUserCommandsAndHidesHook(t *testing.T) {
	deps, stdout, _ := testDeps()

	if got := Execute(deps, []string{"--help"}); got != exitcode.OK {
		t.Fatalf("status = %d", got)
	}

	for _, name := range []string{"acquire", "release", "list", "break", "advertise", "request", "grant", "decline", "revoke", "setup"} {
		if !strings.Contains(stdout.String(), name) {
			t.Errorf("help does not list %s:\n%s", name, stdout)
		}
	}

	if strings.Contains(stdout.String(), "hook") {
		t.Errorf("help names the hook command:\n%s", stdout)
	}
}

func TestEverySubcommandIsRegistered(t *testing.T) {
	deps, _, _ := testDeps()
	root := NewRoot(deps)

	for _, name := range []string{"acquire", "release", "list", "break", "advertise", "request", "grant", "decline", "revoke", "setup", "hook"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("Find(%q) = %v, %v", name, cmd, err)
		}
	}
}
