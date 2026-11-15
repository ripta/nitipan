package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/state"
)

func TestSetupNodePrintsTheLaunchAgentAndWritesNothing(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "setup", "node")
	wantStatus(t, res, exitcode.OK)

	home := filepath.Join(w.root, "userhome")

	wantContains(t, "stdout", res.Stdout,
		"<string>dev.nitipan.node</string>",
		"<string>/usr/local/bin/nitipan</string>",
		"<string>node</string>",
		"<key>KeepAlive</key>",
		"launchctl bootstrap gui/"+strconv.Itoa(os.Getuid())+" "+filepath.Join(home, "Library", "LaunchAgents", "dev.nitipan.node.plist"),
		filepath.Join(home, ".config", "nitipan", "node.json"))
	wantAbsent(t, "stdout", res.Stdout, "$HOME", "~/.local/state")

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Error("setup node wrote into the home directory")
	}

	if _, err := os.Stat(w.state); !os.IsNotExist(err) {
		t.Error("setup node created the state directory")
	}
}

func TestSetupBrokerPrintsItsOwnLaunchAgent(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "setup", "broker")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout,
		"<string>dev.nitipan.broker</string>", "<string>broker</string>", "dev.nitipan.broker.plist",
		"launchctl bootstrap gui/"+strconv.Itoa(os.Getuid()), "broker.json", "refuses to listen on a public or wildcard address")
}

func TestSetupPlistPrefersTheStableExecutableOnPath(t *testing.T) {
	w := newWorld(t)
	w.exe = filepath.Join(w.root, "cellar", "1.2.3", "nitipan")
	stable := filepath.Join(w.root, "bin", "nitipan")

	for _, dir := range []string{filepath.Dir(w.exe), filepath.Dir(stable)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(w.exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(w.exe, stable); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	deps := w.deps(w.terminal(900), &out, &errOut)
	deps.LookPath = func(string) (string, error) { return stable, nil }

	if status := Execute(deps, []string{"setup", "node"}); status != exitcode.OK {
		t.Fatalf("status %d: %s", status, errOut.String())
	}

	wantContains(t, "stdout", out.String(), "<string>"+stable+"</string>")
	wantAbsent(t, "stdout", out.String(), w.exe)
}

func TestNodeCommandIsHiddenFromHelp(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "--help")
	wantAbsent(t, "stdout", res.Stdout, "  node ")
	wantContains(t, "stdout", res.Stdout, "broker")
}

func TestNodeCommandFailsWithoutAConfigFile(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.terminal(900), "node")
	if res.Status == exitcode.OK || !strings.Contains(res.Stderr, "node.json") {
		t.Errorf("node without config: %+v", res)
	}
}

func TestBrokerCommandRefusesAWildcardAddressWithoutACertificate(t *testing.T) {
	w := newWorld(t)
	cfgDir := filepath.Join(w.root, "userhome", ".config", "nitipan")

	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cfgDir, "broker.json"), []byte(`{"version":"1.0","listen":"0.0.0.0:8443"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	res := w.run(w.terminal(900), "broker")
	if res.Status == exitcode.OK || !strings.Contains(res.Stderr, "no certificate") {
		t.Errorf("broker on a wildcard address: %+v", res)
	}
}

func TestNodeNotRunningFailsAtOnceWithTheInstallText(t *testing.T) {
	w := newWorld(t)
	a := &app{deps: w.deps(w.terminal(900), &bytes.Buffer{}, &bytes.Buffer{}).withDefaults()}

	_, err := a.requireNode()
	if !errors.Is(err, handoff.ErrNodeNotRunning) {
		t.Fatalf("requireNode = %v", err)
	}

	wantContains(t, "error", err.Error(), "node not running", "launchctl bootstrap gui/", "dev.nitipan.node.plist")

	presence := handoff.Presence{PID: 4242, Start: startOf(4242), Machine: testMachine, RequestVersion: handoff.RequestVersion}
	if err := handoff.WritePresence(w.state, presence); err != nil {
		t.Fatal(err)
	}

	if _, err := a.requireNode(); !errors.Is(err, handoff.ErrNodeNotRunning) {
		t.Errorf("a presence record for a dead process: %v", err)
	}

	w.table.Add(4242, proctest.Process{Start: startOf(4242)})

	got, err := a.requireNode()
	if err != nil || got.PID != 4242 {
		t.Errorf("requireNode with a live node = %+v, %v", got, err)
	}
}

func TestALockHolderReapsBrokerRequestsAndRepliesOfADeadRequester(t *testing.T) {
	w := newWorld(t)
	w.terminal(700)
	w.terminal(800)
	w.kill(800)

	mk := func(pid int) handoff.Requester {
		return handoff.Requester{PID: pid, Start: startOf(pid), Machine: testMachine}
	}

	ids := map[string]string{}

	for _, spec := range []struct {
		name string
		pid  int
	}{{"dead pending", 800}, {"live pending", 700}} {
		id := state.NewID()
		ids[spec.name] = id

		req := handoff.Request{ID: id, Kind: "ping", Requester: mk(spec.pid), CreatedAt: w.now}
		if err := handoff.Submit(w.state, req); err != nil {
			t.Fatal(err)
		}
	}

	deadClaimed, liveClaimed := state.NewID(), state.NewID()

	for id, pid := range map[string]int{deadClaimed: 800, liveClaimed: 700} {
		if err := handoff.Submit(w.state, handoff.Request{ID: id, Kind: "ping", Requester: mk(pid), CreatedAt: w.now}); err != nil {
			t.Fatal(err)
		}

		if _, err := handoff.Claim(w.state, id); err != nil {
			t.Fatal(err)
		}
	}

	deadReply, liveReply := state.NewID(), state.NewID()

	for id, pid := range map[string]int{deadReply: 800, liveReply: 700} {
		if err := handoff.Answer(w.state, handoff.Reply{ID: id, Requester: mk(pid)}); err != nil {
			t.Fatal(err)
		}
	}

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)

	exists := func(sub, id string) bool {
		_, err := os.Stat(filepath.Join(w.state, sub, id+".json"))

		return err == nil
	}

	cases := []struct {
		sub, id string
		want    bool
	}{
		{handoff.PendingDir, ids["dead pending"], false},
		{handoff.PendingDir, ids["live pending"], true},
		{handoff.ClaimedDir, deadClaimed, false},
		{handoff.ClaimedDir, liveClaimed, true},
		{handoff.AnsweredDir, deadReply, false},
		{handoff.AnsweredDir, liveReply, true},
	}

	for _, tc := range cases {
		if got := exists(tc.sub, tc.id); got != tc.want {
			t.Errorf("%s/%s exists = %v, want %v", tc.sub, tc.id, got, tc.want)
		}
	}
}
