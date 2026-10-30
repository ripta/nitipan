package handoff

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

var machine = record.Machine{Host: "laptop", ID: "m1"}

func start(pid int) record.ProcessStart {
	return record.ProcessStart{Platform: record.PlatformDarwin, Sec: int64(pid)}
}

func checker(pids ...int) *liveness.Checker {
	table := proctest.New(record.PlatformDarwin, 1)

	for _, pid := range pids {
		table.Add(pid, proctest.Process{Start: start(pid)})
	}

	return &liveness.Checker{Procs: table, Local: machine}
}

func presence(pid int) Presence {
	return Presence{PID: pid, Start: start(pid), Machine: machine, NodeVersion: "t", RequestVersion: 1, Kinds: []string{"ping"}}
}

func TestRequireFailsAtOnceWithTheInstallTextWhenNoLiveNodeExists(t *testing.T) {
	const install = "launchctl bootstrap gui/501 /x/dev.nitipan.node.plist\n"

	root := t.TempDir()

	_, err := Require(root, checker(), install)
	if !errors.Is(err, ErrNodeNotRunning) || !strings.Contains(err.Error(), "node not running") || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("no presence record: %v", err)
	}

	if err := WritePresence(root, presence(50)); err != nil {
		t.Fatal(err)
	}

	_, err = Require(root, checker(), install)
	if !errors.Is(err, ErrNodeNotRunning) || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("dead node: %v", err)
	}

	got, err := Require(root, checker(50), install)
	if err != nil || got.PID != 50 {
		t.Errorf("live node: %+v, %v", got, err)
	}
}

func TestRequireTreatsAPidReuseAsDead(t *testing.T) {
	root := t.TempDir()

	if err := WritePresence(root, presence(50)); err != nil {
		t.Fatal(err)
	}

	table := proctest.New(record.PlatformDarwin, 1)
	table.Add(50, proctest.Process{Start: start(999)})

	_, err := Require(root, &liveness.Checker{Procs: table, Local: machine}, "install")
	if !errors.Is(err, ErrNodeNotRunning) {
		t.Errorf("a reused PID read as the node: %v", err)
	}
}

func TestReadPresenceRefusesAVersionItDoesNotKnow(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, BrokerDir), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, BrokerDir, PresenceName), []byte(`{"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadPresence(root); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadPresence = %v", err)
	}
}

type supportCase struct {
	Name    string
	Kind    string
	Version int
	Err     bool
}

var supportCases = []supportCase{
	{"known kind and version", "ping", 1, false},
	{"unknown kind", "grant", 1, true},
	{"newer request version", "ping", 2, true},
}

func TestPresenceSupportsReportsNodeTooOld(t *testing.T) {
	for _, tc := range supportCases {
		t.Run(tc.Name, func(t *testing.T) {
			err := presence(1).Supports(tc.Kind, tc.Version)
			if (err != nil) != tc.Err || (err != nil && !errors.Is(err, ErrNodeTooOld)) {
				t.Errorf("Supports = %v, want node too old: %v", err, tc.Err)
			}
		})
	}
}

func TestExpiredIsMoreThanOneDayOld(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	if Expired(Request{CreatedAt: now.Add(-MaxAge)}, now) {
		t.Error("exactly one day old is expired")
	}

	if !Expired(Request{CreatedAt: now.Add(-MaxAge - time.Second)}, now) {
		t.Error("a request over a day old is not expired")
	}
}

func TestRequestMovesFromPendingToClaimedToAnswered(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	err := Submit(root, Request{ID: id, Kind: "ping", Requester: Requester{PID: 5, Machine: machine}, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	if ids, _ := Pending(root); len(ids) != 1 || ids[0] != id {
		t.Fatalf("pending = %v", ids)
	}

	req, err := Claim(root, id)
	if err != nil || req.Version != RequestVersion || req.Kind != "ping" {
		t.Fatalf("Claim = %+v, %v", req, err)
	}

	if _, err := Claim(root, id); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("second claim = %v, want not exist", err)
	}

	if Withdraw(root, id) {
		t.Error("a claimed request was withdrawn")
	}

	if _, err := ReadReply(root, id); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reply before answer = %v", err)
	}

	if err := Answer(root, Reply{ID: id, Requester: req.Requester}); err != nil {
		t.Fatal(err)
	}

	if rep, err := ReadReply(root, id); err != nil || rep.ID != id {
		t.Errorf("reply = %+v, %v", rep, err)
	}

	if names, _ := state.ListNames(root, ClaimedDir); len(names) != 0 {
		t.Errorf("claimed after answer: %v", names)
	}
}

func TestRequestCanBeWithdrawnWhileItIsPending(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	if err := Submit(root, Request{ID: id, Kind: "ping"}); err != nil {
		t.Fatal(err)
	}

	if !Withdraw(root, id) {
		t.Fatal("a pending request could not be withdrawn")
	}

	if ids, _ := Pending(root); len(ids) != 0 {
		t.Errorf("pending after withdraw = %v", ids)
	}
}

func TestSubmitRefusesAnIDThatCouldEscapeTheDirectory(t *testing.T) {
	if err := Submit(t.TempDir(), Request{ID: "../../etc/passwd", Kind: "ping"}); !errors.Is(err, state.ErrBadID) {
		t.Errorf("Submit = %v", err)
	}
}

func TestV1ScansNeverSeeTheHandoffDirectories(t *testing.T) {
	root := t.TempDir()

	if err := Submit(root, Request{ID: state.NewID(), Kind: "ping"}); err != nil {
		t.Fatal(err)
	}

	if err := WritePresence(root, presence(1)); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{state.LeasesDir, state.TombstonesDir, state.RequestsDir, state.NoticesDir, state.MarksDir} {
		if names, _ := state.ListNames(root, sub); len(names) != 0 {
			t.Errorf("%s holds %v", sub, names)
		}
	}
}

func TestReapRemovesUndecodableFilesOnceTheyPassTheRequestWindow(t *testing.T) {
	root := t.TempDir()

	for _, sub := range []string{PendingDir, ClaimedDir, AnsweredDir} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	oldTime := time.Now().Add(-MaxAge - time.Hour)

	var oldFiles, freshFiles []string

	for _, sub := range []string{PendingDir, ClaimedDir, AnsweredDir} {
		oldPath := filepath.Join(root, sub, state.NewID()+".json")
		freshPath := filepath.Join(root, sub, state.NewID()+".json")

		for _, p := range []string{oldPath, freshPath} {
			if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		oldFiles = append(oldFiles, oldPath)
		freshFiles = append(freshFiles, freshPath)
	}

	dir, err := state.Open(root, state.Options{})
	if err != nil {
		t.Fatal(err)
	}

	l, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Unlock() }()

	if err := Reap(l, checker()); err != nil {
		t.Fatal(err)
	}

	for _, p := range oldFiles {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived: %v", p, err)
		}
	}

	for _, p := range freshFiles {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a recent undecodable file was removed: %v", err)
		}
	}
}

func TestUnusableSidecarAndDeadlineFilesReadAsUnknown(t *testing.T) {
	root := t.TempDir()

	for _, sub := range []string{HeldDir, DeadlinesDir} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(root, HeldDir, "01H0000000000000000000000A.json"), []byte(`{"version":"2.0","id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, HeldDir, "01H0000000000000000000000B.json"), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, DeadlinesDir, "k.json"), []byte(`{"version":"3.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	usable, unknown, err := Sidecars(root)
	if err != nil || len(usable) != 0 || len(unknown) != 2 {
		t.Fatalf("Sidecars = %v, %v, %v", usable, unknown, err)
	}

	if _, err := ReadSidecar(root, "01H0000000000000000000000A"); !errors.Is(err, ErrUnusableFile) {
		t.Fatalf("ReadSidecar err = %v, want ErrUnusableFile", err)
	}

	if _, err := ReadDeadlines(root, "k"); !errors.Is(err, ErrUnusableFile) {
		t.Fatalf("ReadDeadlines err = %v, want ErrUnusableFile", err)
	}
}

func TestHigherMinorSidecarStillReads(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, HeldDir), 0o700); err != nil {
		t.Fatal(err)
	}

	data := `{"version":"1.9","id":"01H0000000000000000000000A","future":true}`
	if err := os.WriteFile(filepath.Join(root, HeldDir, "01H0000000000000000000000A.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadSidecar(root, "01H0000000000000000000000A"); err != nil {
		t.Fatalf("a higher minor must read: %v", err)
	}
}

func plant(t *testing.T, root, sub, name, body string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(root, sub, name+".json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestClaimRefusesABodyWhoseIDDiffersFromTheFileName(t *testing.T) {
	root := t.TempDir()
	victim, forged := state.NewID(), state.NewID()

	body := `{"version":2,"id":"` + victim + `","kind":"ping","requester":{"pid":5}}`
	plant(t, root, PendingDir, forged, body)

	req, err := Claim(root, forged)
	if err == nil {
		t.Fatalf("Claim accepted a body naming %s under the file name %s", req.ID, forged)
	}

	if req.ID != forged {
		t.Errorf("rejected request ID = %q, want the file name %q", req.ID, forged)
	}
}

func TestReapRemovesAMismatchedIDFileOncePastTheRequestWindow(t *testing.T) {
	root := t.TempDir()
	name, other := state.NewID(), state.NewID()

	body := `{"version":2,"id":"` + other + `","kind":"ping","requester":{"pid":5}}`
	oldPath := plant(t, root, ClaimedDir, name, body)
	freshPath := plant(t, root, ClaimedDir, state.NewID(), strings.Replace(body, other, state.NewID(), 1))

	oldTime := time.Now().Add(-MaxAge - time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	dir, err := state.Open(root, state.Options{})
	if err != nil {
		t.Fatal(err)
	}

	l, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Unlock() }()

	if err := Reap(l, checker(5)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(oldPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the old mismatched file survived: %v", err)
	}

	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("a recent file was removed: %v", err)
	}
}
