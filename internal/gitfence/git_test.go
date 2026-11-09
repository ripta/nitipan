package gitfence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The callers classify some failures by git's English message, so git must never be asked for a
// translation.
func TestGitRunsWithoutATranslation(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"LC_ALL=$LC_ALL LANGUAGE=$LANGUAGE\"\n"

	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	runners := []struct {
		Name string
		Run  Runner
	}{
		{Name: "hook", Run: Exec},
		{Name: "clean", Run: ExecClean},
	}

	for _, test := range runners {
		t.Run(test.Name, func(t *testing.T) {
			out, err := test.Run("", nil, "rev-parse")
			if err != nil {
				t.Fatal(err)
			}

			if got := strings.TrimSpace(string(out)); got != "LC_ALL=C LANGUAGE=C" {
				t.Errorf("git ran with %q", got)
			}
		})
	}
}

// A cleaned environment must not shorten the bound on slow commands. A push wrapper runs ls-remote,
// fetch, and rev-list through a cleaned runner, and a first push of a long branch passes ten seconds.
// The test cannot show that git finishes in time, only which bound each runner passes.
func TestRunnerTimeouts(t *testing.T) {
	var got time.Duration

	prior := runGit
	runGit = func(timeout time.Duration, _ []string, _ string, _ []byte, _ []string) ([]byte, error) {
		got = timeout

		return nil, nil
	}

	t.Cleanup(func() { runGit = prior })

	runners := []struct {
		Name string
		Run  Runner
		Want time.Duration
	}{
		{Name: "hook", Run: Exec, Want: hookTimeout},
		{Name: "with, kept environment", Run: ExecWith(false), Want: hookTimeout},
		{Name: "with, cleaned environment", Run: ExecWith(true), Want: hookTimeout},
		{Name: "clean gate", Run: ExecClean, Want: cleanTimeout},
	}

	for _, test := range runners {
		t.Run(test.Name, func(t *testing.T) {
			got = 0

			if _, err := test.Run("", nil, "ls-remote"); err != nil {
				t.Fatal(err)
			}

			if got != test.Want {
				t.Errorf("timeout = %v, want %v", got, test.Want)
			}
		})
	}
}
