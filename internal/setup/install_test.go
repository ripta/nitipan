package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testExe   = "/Users/a b/bin/nitipan"
	testState = "/Users/a b/.local/state/nitipan"
)

func readAll(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestInstallOpencodeWritesTheEmbeddedPluginWithTheAbsolutePathBakedIn(t *testing.T) {
	dir := t.TempDir()

	done, err := InstallOpencode(dir, testExe, testState, false)
	if err != nil {
		t.Fatal(err)
	}

	if len(done) != 3 {
		t.Fatalf("installed %d files, want index.ts, tui.ts, and package.json: %+v", len(done), done)
	}

	for _, f := range done {
		if f.Action != "wrote" {
			t.Errorf("%s: %s", f.Path, f.Action)
		}
	}

	for _, name := range []string{"index.ts", "tui.ts"} {
		text := readAll(t, filepath.Join(dir, "nitipan", name))

		if !strings.Contains(text, `const NITIPAN: string = "/Users/a b/bin/nitipan"`) {
			t.Errorf("%s does not carry the baked path:\n%s", name, text)
		}

		if !strings.Contains(text, `const STATE_DIR: string = "/Users/a b/.local/state/nitipan"`) {
			t.Errorf("%s does not carry the state directory", name)
		}

		if strings.Contains(text, "@@") {
			t.Errorf("%s still has a placeholder", name)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "nitipan" {
		t.Errorf("the plugins directory holds %v, want only the nitipan directory (%v)", entries, err)
	}
}

func TestInstallOpencodeIsIdempotentAndRefusesToOverwriteADifferingFile(t *testing.T) {
	dir := t.TempDir()

	if _, err := InstallOpencode(dir, testExe, testState, false); err != nil {
		t.Fatal(err)
	}

	done, err := InstallOpencode(dir, testExe, testState, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range done {
		if f.Action != "unchanged" {
			t.Errorf("%s: %s", f.Path, f.Action)
		}
	}

	edited := filepath.Join(dir, "nitipan", "tui.ts")
	if err := os.WriteFile(edited, []byte("// mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	untouched := readAll(t, filepath.Join(dir, "nitipan", "index.ts"))

	// A new nitipan path makes every file differ. Nothing is written until --replace says so.
	_, err = InstallOpencode(dir, "/elsewhere/nitipan", testState, false)
	if !errors.Is(err, ErrWouldOverwrite) || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("err = %v", err)
	}

	if got := readAll(t, edited); got != "// mine\n" {
		t.Errorf("the edited file was overwritten: %q", got)
	}

	if got := readAll(t, filepath.Join(dir, "nitipan", "index.ts")); got != untouched {
		t.Error("a refused install wrote some files anyway")
	}

	done, err = InstallOpencode(dir, "/elsewhere/nitipan", testState, true)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range done {
		want := "replaced"
		if filepath.Base(f.Path) == "package.json" {
			want = "unchanged"
		}

		if f.Action != want {
			t.Errorf("%s: %s, want %s", f.Path, f.Action, want)
		}
	}

	if !strings.Contains(readAll(t, edited), `"/elsewhere/nitipan"`) {
		t.Error("--replace did not rewrite the file")
	}
}

func TestInstallOpencodeNeedsAbsolutePaths(t *testing.T) {
	if _, err := InstallOpencode("relative", testExe, testState, false); err == nil {
		t.Error("a relative plugins directory was accepted")
	}

	if _, err := InstallOpencode(t.TempDir(), "nitipan", testState, false); err == nil {
		t.Error("a relative nitipan path was accepted")
	}

	if _, err := InstallOpencode(t.TempDir(), testExe, "state", false); err == nil {
		t.Error("a relative state directory was accepted")
	}
}

func TestEmbeddedPluginsCallHookByTheBakedPathAndNeverRemoveMarksOnCleanup(t *testing.T) {
	files, err := render("/x/nitipan", testState)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]string{}
	for _, f := range files {
		byName[f.name] = string(f.data)
	}

	for _, name := range []string{"index.ts", "tui.ts"} {
		text := byName[name]

		if !strings.Contains(text, `spawn(NITIPAN, ["hook", subcommand]`) {
			t.Errorf("%s does not spawn nitipan hook by the baked path", name)
		}

		if strings.Contains(text, "tmpdir") || !strings.Contains(text, "O_NOFOLLOW") || !strings.Contains(text, "0o600") {
			t.Errorf("%s logs somewhere shared or follows symlinks", name)
		}
	}

	// A TUI on the shared service sees every session's status and events, so running state must not
	// mark a session. A headless session would then read as open.
	tui := byName["tui.ts"]
	for _, source := range []string{"session.status", "session.list", "session.execution", "running.add", "=== \"running\""} {
		if strings.Contains(tui, source) {
			t.Errorf("the TUI plugin reads %q, a running-state source", source)
		}
	}

	// Cleanup also runs on a hot reload, so it must not remove a mark.
	cleanup := tui[strings.LastIndex(tui, "return () => {"):]

	if strings.Contains(cleanup, "hook(") {
		t.Errorf("the TUI plugin's cleanup calls nitipan:\n%s", cleanup)
	}
}

func TestEmbeddedServerPluginMarksSessionsThatAreRunningWhenItLoads(t *testing.T) {
	files, err := render("/x/nitipan", testState)
	if err != nil {
		t.Fatal(err)
	}

	var server string

	for _, f := range files {
		if f.name == "index.ts" {
			server = string(f.data)
		}
	}

	// A turn a restarted service resumes raises no event this plugin sees, so the plugin scans on load.
	for _, source := range []string{"ctx.session.list", "ctx.session.wait", "RESCAN_MS", "owner.get(id) !== who", "REPROBES", "signal.aborted", "markedHere", "sessions\", \"executing"} {
		if !strings.Contains(server, source) {
			t.Errorf("the server plugin lacks %q, part of marking sessions already running at load", source)
		}
	}

	if strings.Contains(server, "session.status") {
		t.Errorf("the server plugin reads session.status, which the 2.1.0 plugin API does not offer")
	}
}

func TestTextNamesAbsolutePathsAndQuotesTheExecutable(t *testing.T) {
	in := Input{StateDir: "/home/u/.local/state/nitipan", Executable: testExe}

	codex, err := Text(Codex, in)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`writable_roots = ["/home/u/.local/state/nitipan"]`,
		`command = "'/Users/a b/bin/nitipan' hook session-start"`,
		`command = "'/Users/a b/bin/nitipan' hook session-end"`,
		"timeout = 3",
		"thread_unload_delay_secs = 60",
		"background terminals and MCP servers stop 60 seconds",
		"/hooks",
	} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex text lacks %q:\n%s", want, codex)
		}
	}

	opencode, err := Text(Opencode, in)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--install-plugins",
		`'/Users/a b/bin/nitipan' setup opencode --install-plugins`,
		`{ "action": "shell", "resource": "nitipan *", "effect": "allow" }`,
		`{ "action": "external_directory", "resource": "/home/u/.local/state/nitipan/*", "effect": "allow" }`,
	} {
		if !strings.Contains(opencode, want) {
			t.Errorf("opencode text lacks %q:\n%s", want, opencode)
		}
	}

	if !strings.Contains(codex, "under $CODEX_HOME (default ~/.codex). It trusts") {
		t.Errorf("the closing paragraph of the codex text is malformed:\n%s", codex)
	}

	if strings.Contains(codex+opencode, "~/.local") {
		t.Error("the text names the state directory with ~")
	}
}

// TestSetupTextTellsUserToRunBrokerAsNonAgentOSUser tests that the broker setup text tells
// the user to run the broker as an OS user where no agent session runs.
func TestSetupTextTellsUserToRunBrokerAsNonAgentOSUser(t *testing.T) {
	text := BrokerText(DaemonInput{
		Executable: "/usr/local/bin/nitipan",
		Home:       "/Users/test",
		UID:        501,
	})

	if !strings.Contains(text, "No agent session may run as that OS user") {
		t.Error("broker setup text does not mention that no agent session may run as that OS user")
	}
}
