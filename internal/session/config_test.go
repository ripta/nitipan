package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/record"
)

const testExe = "/usr/local/bin/nitipan"

type configTest struct {
	Name  string
	TOML  string
	JSON  string
	Delay *int
	End   string
}

func intp(n int) *int { return &n }

var configTests = []configTest{
	{Name: "empty config", Delay: intp(1800), End: CheckAbsent},
	{
		Name:  "table form",
		TOML:  "thread_unload_delay_secs = 60\n\n[[hooks.SessionEnd]]\n[[hooks.SessionEnd.hooks]]\ntype = \"command\"\ncommand = \"" + testExe + " hook session-end\"\ntimeout = 3\n",
		Delay: intp(60),
		End:   CheckPresent,
	},
	{
		Name:  "delay with underscores and a comment",
		TOML:  "thread_unload_delay_secs = 1_0 # ten\n",
		Delay: intp(10),
		End:   CheckAbsent,
	},
	{
		Name:  "delay set after a table is not top level",
		TOML:  "[other]\nthread_unload_delay_secs = 5\n",
		Delay: intp(1800),
		End:   CheckAbsent,
	},
	{Name: "delay as a string", TOML: "thread_unload_delay_secs = \"60\"\n", End: CheckAbsent},
	{Name: "delay as a float", TOML: "thread_unload_delay_secs = 6.5\n", End: CheckAbsent},
	{
		Name:  "inline hooks array under the hooks table",
		TOML:  "[hooks]\nSessionEnd = [{ hooks = [{ type = \"command\", command = \"" + testExe + " hook session-end\" }] }]\n",
		Delay: intp(1800),
		End:   CheckPresent,
	},
	{
		Name:  "multi-line inline array under the hooks table",
		TOML:  "[hooks]\nSessionEnd = [\n  { hooks = [\n    { type = \"command\", command = \"" + testExe + " hook session-end\" },\n  ] },\n]\n",
		Delay: intp(1800),
		End:   CheckPresent,
	},
	{
		Name:  "dotted key",
		TOML:  "hooks.SessionEnd = [{ hooks = [{ type = \"command\", command = \"" + testExe + " hook session-end\" }] }]\n",
		Delay: intp(1800),
		End:   CheckPresent,
	},
	{
		Name:  "quoted executable path",
		TOML:  "[[hooks.SessionEnd.hooks]]\ncommand = \"'/Users/a b/nitipan' hook session-end\"\n",
		Delay: intp(1800),
		End:   CheckUnknown,
	},
	{
		Name:  "another program's session end entry",
		TOML:  "[[hooks.SessionEnd.hooks]]\ncommand = \"/bin/other hook session-end\"\n",
		Delay: intp(1800),
		End:   CheckUnknown,
	},
	{
		Name:  "session start entry only",
		TOML:  "[[hooks.SessionStart.hooks]]\ncommand = \"" + testExe + " hook session-start\"\n",
		Delay: intp(1800),
		End:   CheckAbsent,
	},
	{
		Name:  "hooks json at the top level",
		JSON:  `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"` + testExe + ` hook session-end"}]}]}}`,
		Delay: intp(1800),
		End:   CheckPresent,
	},
	{
		Name:  "hooks json with events at the top level",
		JSON:  `{"SessionEnd":[{"hooks":[{"type":"command","command":"` + testExe + ` hook session-end"}]}]}`,
		Delay: intp(1800),
		End:   CheckPresent,
	},
	{
		Name:  "hooks json that does not parse",
		JSON:  `{"SessionEnd":`,
		Delay: intp(1800),
		End:   CheckUnknown,
	},
}

func TestScanCodexConfig(t *testing.T) {
	hostStart := time.Unix(1_800_000_000, 0)
	old := hostStart.Add(-time.Hour)

	for _, test := range configTests {
		t.Run(test.Name, func(t *testing.T) {
			home := t.TempDir()

			write := func(name, content string) {
				path := filepath.Join(home, name)
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}

				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}

			if test.TOML != "" {
				write("config.toml", test.TOML)
			}

			if test.JSON != "" {
				write("hooks.json", test.JSON)
			}

			got := ScanCodexConfig(home, testExe, hostStart, true)

			if (got.UnloadDelaySecs == nil) != (test.Delay == nil) ||
				(got.UnloadDelaySecs != nil && *got.UnloadDelaySecs != *test.Delay) {
				t.Errorf("delay = %v, want %v", deref(got.UnloadDelaySecs), deref(test.Delay))
			}

			if got.SessionEnd != test.End {
				t.Errorf("session end = %q, want %q", got.SessionEnd, test.End)
			}
		})
	}
}

func deref(p *int) any {
	if p == nil {
		return "unknown"
	}

	return *p
}

func TestScanCodexConfigTrustsOnlyFilesOlderThanTheHost(t *testing.T) {
	hostStart := time.Unix(1_800_000_000, 0)
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")

	if err := os.WriteFile(path, []byte("thread_unload_delay_secs = 60\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, mtime := range map[string]time.Time{
		"newer":     hostStart.Add(time.Second),
		"same time": hostStart,
	} {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}

		got := ScanCodexConfig(home, testExe, hostStart, true)
		if got.UnloadDelaySecs != nil || got.SessionEnd != CheckUnknown {
			t.Errorf("%s: a file not older than the host was trusted: %+v", name, got)
		}
	}
}

func TestScanCodexConfigReadsUnknownWithoutAWallClockStart(t *testing.T) {
	home := t.TempDir()

	got := ScanCodexConfig(home, testExe, time.Time{}, false)
	if got.UnloadDelaySecs != nil || got.SessionEnd != CheckUnknown {
		t.Errorf("checks = %+v", got)
	}

	if _, ok := StartTime(record.ProcessStart{Platform: record.PlatformLinux, Ticks: 5}); ok {
		t.Error("a Linux start was converted to wall-clock time")
	}

	if at, ok := StartTime(record.ProcessStart{Platform: record.PlatformDarwin, Sec: 10, Usec: 5}); !ok ||
		at.UnixMicro() != 10_000_005 {
		t.Errorf("macOS start = %v, %v", at, ok)
	}
}

func TestScanCodexConfigReadsUnknownForARelativeHome(t *testing.T) {
	got := ScanCodexConfig("relative/home", testExe, time.Unix(1, 0), true)
	if got.UnloadDelaySecs != nil || got.SessionEnd != CheckUnknown {
		t.Errorf("checks = %+v", got)
	}
}
