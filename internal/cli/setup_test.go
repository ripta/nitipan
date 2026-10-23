package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
)

func TestSetupCodexPrintsTheWritableRootTheHooksAndTheUnloadDelay(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "setup", "codex")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout,
		`writable_roots = ["`+w.state+`"]`,
		`command = "/usr/local/bin/nitipan hook session-start"`,
		`command = "/usr/local/bin/nitipan hook session-end"`,
		"[[hooks.SessionStart]]", "[[hooks.SessionEnd]]", "timeout = 3",
		"thread_unload_delay_secs = 60",
		"a thread's background terminals and MCP servers stop 60 seconds after you switch away",
		"Restart the Codex daemon", "mkdir -p -m 700 "+w.state)
	wantAbsent(t, "stdout", res.Stdout, "~/.local/state")

	if _, err := os.Stat(w.state); !os.IsNotExist(err) {
		t.Error("setup created the state directory")
	}
}

func TestSetupOpencodePrintsInstallStepsAndNeverThePluginSource(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "setup", "opencode")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "--install-plugins", `"action": "shell"`, `"resource": "nitipan *"`,
		`"action": "external_directory"`, w.state)
	wantAbsent(t, "stdout", res.Stdout, "spawn(", "setInterval", "export default")
}

func TestSetupInstallPluginsWritesFilesAndPrintsOnlyWhatItDid(t *testing.T) {
	w := newWorld(t)
	plugins := filepath.Join(w.root, "config", "plugins")

	res := w.run(w.terminal(900), "setup", "opencode", "--install-plugins", plugins)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "wrote "+filepath.Join(plugins, "nitipan", "index.ts"),
		"wrote "+filepath.Join(plugins, "nitipan", "tui.ts"), "Restart the opencode service")
	wantAbsent(t, "stdout", res.Stdout, "spawn(", "export default")

	data, err := os.ReadFile(filepath.Join(plugins, "nitipan", "index.ts"))
	if err != nil || !strings.Contains(string(data), `"/usr/local/bin/nitipan"`) {
		t.Fatalf("installed plugin: %v\n%s", err, data)
	}

	// Only the plugin directory appears. No settings file is written anywhere.
	entries, err := os.ReadDir(plugins)
	if err != nil || len(entries) != 1 {
		t.Errorf("plugins directory = %v, %v", entries, err)
	}

	again := w.run(w.terminal(900), "setup", "opencode", "--install-plugins", plugins)
	wantStatus(t, again, exitcode.OK)
	wantContains(t, "stdout", again.Stdout, "unchanged")

	w.exe = "/somewhere/else/nitipan"

	refused := w.run(w.terminal(900), "setup", "opencode", "--install-plugins", plugins)
	wantStatus(t, refused, exitcode.Error)
	wantContains(t, "stderr", refused.Stderr, "--replace")

	replaced := w.run(w.terminal(900), "setup", "opencode", "--install-plugins", plugins, "--replace")
	wantStatus(t, replaced, exitcode.OK)
	wantContains(t, "stdout", replaced.Stdout, "replaced")
}

func TestSetupInstallPluginsResolvesARelativeDirectoryAgainstTheWorkingDirectory(t *testing.T) {
	w := newWorld(t)

	wantStatus(t, w.run(w.terminal(900), "setup", "opencode", "--install-plugins", "rel/plugins"), exitcode.OK)

	if _, err := os.Stat(filepath.Join(w.root, "rel", "plugins", "nitipan", "tui.ts")); err != nil {
		t.Error(err)
	}
}

func TestSetupInstallPluginsIsOnlyForOpencode(t *testing.T) {
	w := newWorld(t)

	wantStatus(t, w.run(w.terminal(900), "setup", "codex", "--install-plugins", w.dir("p")), exitcode.Usage)
	wantStatus(t, w.run(w.terminal(900), "setup", "opencode", "--replace"), exitcode.Usage)
}
