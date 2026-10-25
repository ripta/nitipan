package gitfence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
