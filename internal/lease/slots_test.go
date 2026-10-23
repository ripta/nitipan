package lease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type slotConfigTest struct {
	Name     string
	File     string
	Usable   bool
	Counts   map[string]int
	Problems []string
}

var slotConfigTests = []slotConfigTest{
	{Name: "no file", Usable: true, Counts: map[string]int{"gpu:main": 1}},
	{
		Name: "counts", File: `{"version": 1, "slots": {"gpu:main": 2, "db.pg:main": 4}}`, Usable: true,
		Counts: map[string]int{"gpu:main": 2, "db.pg:main": 4, "cpu:heavy": 1},
	},
	{Name: "no slots map", File: `{"version": 1}`, Usable: true, Counts: map[string]int{"gpu:main": 1}},
	{Name: "bad count", File: `{"version": 1, "slots": {"gpu:main": 0}}`, Problems: []string{"at least 1"}},
	{Name: "negative count", File: `{"version": 1, "slots": {"gpu:main": -2}}`, Problems: []string{"at least 1"}},
	{Name: "float count", File: `{"version": 1, "slots": {"gpu:main": 2.0}}`, Problems: []string{"at least 1"}},
	{Name: "bad name", File: `{"version": 1, "slots": {"gpu": 2}}`, Problems: []string{"exactly one colon"}},
	{Name: "repo namespace", File: `{"version": 1, "slots": {"repo:x": 2}}`, Problems: []string{"reserved"}},
	{Name: "no version", File: `{"slots": {}}`, Problems: []string{`missing "version"`}},
	{Name: "version as text", File: `{"version": "1", "slots": {}}`, Problems: []string{`"version" is "1"`}},
	{Name: "unknown version", File: `{"version": 2, "slots": {}}`, Problems: []string{"unknown"}},
	{Name: "unknown field", File: `{"version": 1, "extra": 1}`, Problems: []string{`unknown field "extra"`}},
	{Name: "slots not an object", File: `{"version": 1, "slots": [1]}`, Problems: []string{`"slots" must be an object`}},
	{Name: "trailing value", File: `{"version": 1} {}`, Problems: []string{"more than one"}},
	{Name: "not an object", File: `[1]`, Problems: []string{"not a JSON object"}},
	{
		Name:     "all errors together",
		File:     `{"version": 2, "slots": {"a:b": 0, "repo:x": 1, "Bad": 2}, "z": 1, "y": 1}`,
		Problems: []string{`unknown field "y"`, `unknown field "z"`, "unknown", `"Bad"`, `"a:b"`, `"repo:x"`},
	},
}

func TestLoadSlotConfig(t *testing.T) {
	for _, test := range slotConfigTests {
		t.Run(test.Name, func(t *testing.T) {
			root := t.TempDir()

			if test.File != "" {
				if err := os.WriteFile(filepath.Join(root, SlotConfigFile), []byte(test.File), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cfg := LoadSlotConfig(root)

			if cfg.Usable() != test.Usable {
				t.Fatalf("usable = %v, problems %v", cfg.Usable(), cfg.Problems)
			}

			if cfg.Path != filepath.Join(root, SlotConfigFile) {
				t.Errorf("path = %q", cfg.Path)
			}

			for name, want := range test.Counts {
				if got := cfg.Count(name); got != want {
					t.Errorf("Count(%q) = %d, want %d", name, got, want)
				}
			}

			if len(cfg.Problems) != len(test.Problems) {
				t.Fatalf("problems = %q, want %d of them", cfg.Problems, len(test.Problems))
			}

			for i, want := range test.Problems {
				if !strings.Contains(cfg.Problems[i], want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, cfg.Problems[i], want)
				}
			}

			if test.Usable != (cfg.Warning() == "") {
				t.Errorf("warning = %q for a config with usable = %v", cfg.Warning(), test.Usable)
			}

			if !test.Usable && cfg.Count("gpu:main") != 1 {
				t.Errorf("an unusable config gave a count of %d", cfg.Count("gpu:main"))
			}
		})
	}
}
