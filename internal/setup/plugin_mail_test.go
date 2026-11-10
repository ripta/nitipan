package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var pluginMailDiscoveryScenarios = []string{"initial-failure", "later-page"}

// The SDK fixture drives the installed plugin with a manual clock. It proves discovery recovery and
// paging without booting opencode; the live harness owns the real session.prompt wake/read contract.
func TestOpencodeMailDiscoveryRecoversAndReachesLaterPages(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("opencode plugin discovery fixture requires Node with TypeScript stripping")
	}

	dir := t.TempDir()
	if _, err := InstallOpencode(dir, "/fixture/nitipan", filepath.Join(dir, "state"), false); err != nil {
		t.Fatal(err)
	}

	// Reading the fixture here makes Go's test cache track changes that Node alone cannot report.
	script, err := os.ReadFile("testdata/opencode_mail_discovery.mjs")
	if err != nil {
		t.Fatal(err)
	}

	for _, scenario := range pluginMailDiscoveryScenarios {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(node, "--experimental-vm-modules", "--input-type=module", "-", filepath.Join(dir, "nitipan", "index.ts"), scenario)
			cmd.Stdin = bytes.NewReader(script)
			out, err := cmd.CombinedOutput()
			if strings.Contains(string(out), "SKIP: TypeScript stripping unavailable") {
				t.Skip(string(out))
			}

			if err != nil {
				t.Fatalf("production plugin discovery: %v\n%s", err, out)
			}

			t.Log(string(out))
		})
	}
}
