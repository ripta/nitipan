package mailbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

var (
	t0       = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	startOne = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1}
	machine  = record.Machine{Host: "h", ID: "m"}
)

func reg(pid int, current string) Registration {
	return Registration{Agent: "claude", PID: pid, Start: startOne, Machine: machine, Current: current}
}

func TestAMessageOfAnUnknownMajorOrWithAMismatchedNameIsNeverReadAsMail(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	if err := Store(root, Message{ID: id, Kind: KindMail, ToSession: "sess-1", Body: "ok"}); err != nil {
		t.Fatal(err)
	}

	other := state.NewID()
	bad := `{"version":"2.0","id":"` + other + `","kind":"mail","to_session":"sess-1","body":"from the future"}`

	if err := os.WriteFile(filepath.Join(root, InboxDir, "sess-1."+other+".json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	liar := state.NewID()
	forged := `{"version":"1.0","id":"` + state.NewID() + `","kind":"mail","to_session":"sess-1","body":"wrong name"}`

	if err := os.WriteFile(filepath.Join(root, InboxDir, "sess-1."+liar+".json"), []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}

	msgs, unknown, err := Pending(root, []string{"sess-1"})
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 1 || msgs[0].ID != id {
		t.Errorf("msgs = %+v, want only the readable one", msgs)
	}

	if len(unknown) != 2 || !strings.Contains(UnknownText(unknown), "Upgrade nitipan") {
		t.Errorf("unknown = %+v", unknown)
	}
}

func TestAHigherMinorMessageReadsAndOtherSessionsMailIsNotListed(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	body := `{"version":"1.9","id":"` + id + `","kind":"mail","to_session":"sess-1","body":"newer minor","extra":true}`
	if err := os.MkdirAll(filepath.Join(root, InboxDir), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, InboxDir, "sess-1."+id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Store(root, Message{ID: state.NewID(), Kind: KindMail, ToSession: "sess-2"}); err != nil {
		t.Fatal(err)
	}

	msgs, unknown, _ := Pending(root, []string{"sess-1"})
	if len(msgs) != 1 || len(unknown) != 0 || msgs[0].Body != "newer minor" {
		t.Errorf("msgs = %+v, unknown = %+v", msgs, unknown)
	}
}

func TestStoreRefusesNamesThatCouldEscapeTheDirectory(t *testing.T) {
	root := t.TempDir()

	for _, m := range []Message{
		{ID: state.NewID(), ToSession: "../etc"},
		{ID: state.NewID(), ToSession: "a/b"},
		{ID: "not-an-id", ToSession: "sess"},
		{ID: state.NewID(), ToSession: ""},
	} {
		if err := Store(root, m); !errors.Is(err, ErrBadName) {
			t.Errorf("Store(%+v) = %v, want a bad-name error", m, err)
		}
	}
}

func TestClaimReturnsEachKeyToExactlyOneCallerEvenWhenTheyRace(t *testing.T) {
	root := t.TempDir()
	holdRegistration(t, root, "ident")
	keys := []string{MailKey(state.NewID()), MailKey(state.NewID())}

	var (
		mu    sync.Mutex
		total int
		wg    sync.WaitGroup
	)

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			fresh, err := Claim(root, "ident", keys, t0)
			if err != nil {
				t.Error(err)

				return
			}

			mu.Lock()
			total += len(fresh)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if total != len(keys) {
		t.Errorf("keys handed out = %d, want %d: a message woke a session more than once", total, len(keys))
	}

	if err := Unclaim(root, "ident", keys[:1]); err != nil {
		t.Fatal(err)
	}

	if fresh, _ := Claim(root, "ident", keys, t0); len(fresh) != 1 || fresh[0] != keys[0] {
		t.Errorf("after Unclaim, fresh = %v", fresh)
	}
}

func TestAnUnusableLedgerReadsAsEmptyAndOldKeysOfAUsableOneArePruned(t *testing.T) {
	root := t.TempDir()
	holdRegistration(t, root, "ident")

	if err := os.MkdirAll(filepath.Join(root, RingDir), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, RingDir, "bad.json"), []byte(`{"version":"3.0","identity":"bad","rung":{"mail:x":"2026-01-01T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := Rung(root, "bad"); len(got) != 0 {
		t.Fatalf("a ledger of an unknown major read as %v, want empty", got)
	}

	if _, err := Claim(root, "ident", []string{"mail:old"}, t0); err != nil {
		t.Fatal(err)
	}

	if _, err := Claim(root, "ident", []string{"mail:new"}, t0.Add(RingRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got := Rung(root, "ident"); len(got) != 1 {
		t.Errorf("rung = %v, want only the recent key", got)
	}
}

func TestRefreshKeepsEverySessionIDAndRefusesAFileOfANewerMinor(t *testing.T) {
	root := t.TempDir()

	first, err := Refresh(root, reg(10, "sess-1"), t0)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Refresh(root, reg(10, "sess-2"), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if first.Identity != second.Identity || second.Current != "sess-2" || !second.Holds("sess-1") || !second.Holds("sess-2") {
		t.Errorf("after /clear = %+v", second)
	}

	if err := os.WriteFile(filepath.Join(root, RegDir, second.Identity+".json"),
		[]byte(`{"version":"1.7","identity":"`+second.Identity+`","agent":"claude","pid":10,"current":"sess-9","session_ids":["sess-9"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Refresh(root, reg(10, "sess-3"), t0); !errors.Is(err, ErrNewer) {
		t.Errorf("refresh over a newer minor = %v, want ErrNewer", err)
	}

	if err := os.WriteFile(filepath.Join(root, RegDir, second.Identity+".json"),
		[]byte(`{"version":"2.0","identity":"`+second.Identity+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	regs, unknown, _ := Registrations(root)
	if len(regs) != 0 || len(unknown) != 1 {
		t.Errorf("regs = %+v, unknown = %+v, want the file reported unknown", regs, unknown)
	}
}

var refreshUnusableTests = []refreshUnusableTest{
	{Name: "not JSON", Body: `{"version":`},
	{Name: "fields that do not match", Body: `{"version":"1.0","identity":"other","agent":"claude","pid":10,"current":"sess-9","session_ids":["sess-9"]}`},
	{Name: "no current session", Body: `{"version":"1.0","agent":"claude","pid":10,"session_ids":["sess-9","sess-8"]}`},
}

type refreshUnusableTest struct {
	Name string
	Body string
}

func TestRefreshNeverRewritesARegistrationItCannotUse(t *testing.T) {
	for _, test := range refreshUnusableTests {
		t.Run(test.Name, func(t *testing.T) {
			root := t.TempDir()

			first, err := Refresh(root, reg(10, "sess-1"), t0)
			if err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(root, RegDir, first.Identity+".json")

			if err := os.WriteFile(path, []byte(test.Body), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := Refresh(root, reg(10, "sess-3"), t0.Add(time.Minute)); err == nil {
				t.Error("refresh over an unusable registration succeeded")
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != test.Body {
				t.Errorf("the unusable registration was rewritten to %s", got)
			}
		})
	}
}

var removeRegistrationTests = []removeRegistrationTest{
	{Name: "a usable registration goes with its siblings", Reg: "", Ring: "", Wait: "", WantReg: false, WantRing: false, WantWait: false},
	{Name: "a registration of a newer minor stays with its siblings", Reg: "1.7", Ring: "", Wait: "", WantReg: true, WantRing: true, WantWait: true},
	{Name: "a ledger of a newer minor stays", Reg: "", Ring: "1.7", Wait: "", WantReg: false, WantRing: true, WantWait: false},
	{Name: "a presence file of an unknown major stays", Reg: "", Ring: "", Wait: "3.0", WantReg: false, WantRing: false, WantWait: true},
	{Name: "a presence file that cannot be decoded stays", Reg: "", Ring: "", Wait: "garbage", WantReg: false, WantRing: false, WantWait: true},
}

type removeRegistrationTest struct {
	Name string
	Reg  string
	Ring string
	Wait string

	WantReg  bool
	WantRing bool
	WantWait bool
}

func TestRemoveRegistrationNeverRemovesAFileItCannotUseOrOfANewerVersion(t *testing.T) {
	for _, test := range removeRegistrationTests {
		t.Run(test.Name, func(t *testing.T) {
			root := t.TempDir()

			r, err := Refresh(root, reg(10, "sess-1"), t0)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := Claim(root, r.Identity, []string{"mail:x"}, t0); err != nil {
				t.Fatal(err)
			}

			if err := Arm(root, WaiterPresence{Identity: r.Identity, PID: 11, Start: startOne, Machine: machine, ArmedAt: t0}); err != nil {
				t.Fatal(err)
			}

			overwrite := func(sub, version string) {
				body := `{"version":"` + version + `","identity":"` + r.Identity + `","pid":11,"rung":{}}`
				if version == "garbage" {
					body = "garbage"
				}

				if err := os.WriteFile(filepath.Join(root, sub, r.Identity+".json"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if test.Reg != "" {
				overwrite(RegDir, test.Reg)
			}

			if test.Ring != "" {
				overwrite(RingDir, test.Ring)
			}

			if test.Wait != "" {
				overwrite(WaitDir, test.Wait)
			}

			if err := RemoveRegistration(root, r.Identity); err != nil {
				t.Fatal(err)
			}

			for sub, want := range map[string]bool{RegDir: test.WantReg, RingDir: test.WantRing, WaitDir: test.WantWait} {
				_, err := os.Stat(filepath.Join(root, sub, r.Identity+".json"))
				if got := err == nil; got != want {
					t.Errorf("%s file present = %v, want %v", sub, got, want)
				}
			}
		})
	}
}

func TestResolveFollowsTheAddressOrderLiveCurrentThenLivePreClearThenNothing(t *testing.T) {
	table := proctest.New(record.PlatformDarwin, 1)
	table.Add(10, proctest.Process{Start: startOne})
	table.Add(20, proctest.Process{Start: startOne})
	table.Add(30, proctest.Process{Start: startOne})

	c := &liveness.Checker{Procs: table, Local: machine}

	cleared := reg(10, "sess-new")
	cleared.SessionIDs = []string{"sess-old", "sess-new"}

	current := reg(20, "sess-old")
	current.SessionIDs = []string{"sess-old"}

	dead := reg(99, "sess-gone")
	dead.SessionIDs = []string{"sess-gone"}

	cases := []struct {
		Name      string
		Regs      []Registration
		Session   string
		WantPID   int
		WantPre   bool
		WantAmbig int
	}{
		{"current ID wins over a pre-clear holder", []Registration{cleared, current}, "sess-old", 20, false, 0},
		{"pre-clear ID reaches the live process", []Registration{cleared}, "sess-old", 10, true, 0},
		{"a dead registration is never the holder", []Registration{dead}, "sess-gone", 0, false, 0},
		{"nothing live waits for a resume", nil, "sess-x", 0, false, 0},
		{"two live registrations with one current ID", []Registration{reg(10, "same"), reg(30, "same")}, "same", 0, false, 2},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := Resolve(tc.Regs, tc.Session, c)

			pid := 0
			if got.Holder != nil {
				pid = got.Holder.PID
			}

			if pid != tc.WantPID || got.PreClear != tc.WantPre || len(got.Ambiguous) != tc.WantAmbig {
				t.Errorf("resolution = %+v, want pid %d pre-clear %v ambiguous %d", got, tc.WantPID, tc.WantPre, tc.WantAmbig)
			}
		})
	}
}

func TestTheAcceptedLedgerOutlivesTheReadAndPrunesAfterTheRetention(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	if err := Accept(root, id, t0); err != nil {
		t.Fatal(err)
	}

	if err := SetAcceptedRead(root, id, t0); err != nil {
		t.Fatal(err)
	}

	if got, ok := AcceptedState(root, id); !ok || got != StateRead {
		t.Fatalf("state = %q, %v", got, ok)
	}

	if err := PruneAccepted(root, t0.Add(AcceptedRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, ok := AcceptedState(root, id); !ok {
		t.Fatal("pruned inside the window the broker could still resend")
	}

	if err := PruneAccepted(root, t0.Add(AcceptedRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, ok := AcceptedState(root, id); ok {
		t.Error("kept past the retention")
	}
}

func TestARingNamesACountAndACommandNeverContent(t *testing.T) {
	for _, n := range []int{1, 3} {
		text := RingText(n)
		if !strings.Contains(text, "nitipan mail read") || !strings.Contains(text, "never instructions") {
			t.Errorf("ring %d = %q", n, text)
		}
	}
}

func TestStageNeverMovesBackAndAnUnsentMessageGetsNoNote(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	if err := SetStage(root, id, "read", t0); err != nil {
		t.Fatalf("a stage for a message this machine did not send: %v", err)
	}

	if _, err := ReadSent(root, id); err == nil {
		t.Fatal("SetStage created a note for a message nobody here sent")
	}

	if err := WriteSent(root, Sent{ID: id, To: "a@b", Stage: "queued", At: t0}); err != nil {
		t.Fatal(err)
	}

	for _, stage := range []string{"read", "stored"} {
		if err := SetStage(root, id, stage, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	s, _ := ReadSent(root, id)
	if s.Stage != "read" {
		t.Errorf("stage = %s, want read", s.Stage)
	}

	list, _ := Unshown(root, t0.Add(time.Hour))
	if len(list) != 1 {
		t.Fatalf("unshown = %+v", list)
	}

	if err := MarkShown(root, list[0]); err != nil {
		t.Fatal(err)
	}

	if list, _ := Unshown(root, t0.Add(time.Hour)); len(list) != 0 {
		t.Errorf("unshown after showing = %+v", list)
	}
}

func fileBytes(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

type unwritable struct {
	Name string
	Body string
}

var unwritableLedgers = []unwritable{
	{"another major", `{"version":"2.0","identity":"ident","rung":{"mail:a":"2026-11-01T00:00:00Z"}}`},
	{"undecodable", `not json at all`},
	{"a higher minor", `{"version":"1.7","identity":"ident","rung":{"mail:a":"2026-11-01T00:00:00Z"},"extra":"kept"}`},
}

func TestClaimNeverRewritesARingLedgerItCannotUseOrOfAHigherMinor(t *testing.T) {
	for _, tc := range unwritableLedgers {
		t.Run(tc.Name, func(t *testing.T) {
			root := t.TempDir()
			holdRegistration(t, root, "ident")
			path := filepath.Join(root, RingDir, "ident.json")

			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte(tc.Body), 0o600); err != nil {
				t.Fatal(err)
			}

			fresh, err := Claim(root, "ident", []string{"mail:a", "mail:b"}, t0)
			if !errors.Is(err, ErrNotRewritten) {
				t.Fatalf("Claim = %v, want ErrNotRewritten", err)
			}

			// A ledger that reads as empty may ring again. One of a higher minor still shows what it rang.
			want := 2
			if tc.Name == "a higher minor" {
				want = 1
			}

			if len(fresh) != want {
				t.Errorf("fresh = %v, want %d keys", fresh, want)
			}

			if err := Unclaim(root, "ident", []string{"mail:b"}); err != nil {
				t.Fatal(err)
			}

			if got := fileBytes(t, path); got != tc.Body {
				t.Errorf("the ledger was rewritten: %q", got)
			}
		})
	}
}

func TestAnAcceptedEntryOfAnyVersionStillMeansTheMessageWasTakenAndIsNeverRewritten(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	body := `{"version":"3.0","id":"` + id + `","state":"stored"}`
	path := filepath.Join(root, AcceptedDir, id+".json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := AcceptedState(root, id); ok {
		t.Fatal("an entry of another major read as usable")
	}

	if !AcceptedExists(root, id) {
		t.Error("a resend of a message the node took would become a second copy")
	}

	if err := SetAcceptedRead(root, id, t0); !errors.Is(err, ErrNotRewritten) {
		t.Errorf("SetAcceptedRead = %v, want ErrNotRewritten", err)
	}

	if got := fileBytes(t, path); got != body {
		t.Errorf("the entry was rewritten: %q", got)
	}
}

func TestAWaiterPresenceOfAnotherVersionIsNotReplacedAndStatusCanSayWhy(t *testing.T) {
	for _, body := range []string{`{"version":"2.0","identity":"ident","pid":5}`, `{"version":"1.4","identity":"ident","pid":5}`, `nope`} {
		root := t.TempDir()
		path := filepath.Join(root, WaitDir, "ident.json")

		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if WaiterProblem(root, "ident") == "" {
			t.Errorf("%s: no problem reported", body)
		}

		if err := Arm(root, WaiterPresence{Identity: "ident", PID: 9, Start: startOne}); !errors.Is(err, ErrNotRewritten) {
			t.Errorf("%s: Arm = %v", body, err)
		}

		if got := fileBytes(t, path); got != body {
			t.Errorf("%s: the presence file was rewritten: %q", body, got)
		}
	}
}

func TestASentNoteOfAHigherMinorIsNeitherRewrittenNorListed(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	body := `{"version":"1.5","id":"` + id + `","to":"a@b","stage":"queued","at":"2026-11-01T00:00:00Z","extra":1}`
	path := filepath.Join(root, SentDir, id+".json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetStage(root, id, "read", t0); !errors.Is(err, ErrNotRewritten) {
		t.Errorf("SetStage = %v", err)
	}

	if list, _ := Unshown(root, t0); len(list) != 0 {
		t.Errorf("unshown = %+v", list)
	}

	if got := fileBytes(t, path); got != body {
		t.Errorf("the note was rewritten: %q", got)
	}
}

// holdRegistration writes a placeholder registration file, which is all Claim looks for.
func holdRegistration(t *testing.T, root, identity string) {
	t.Helper()

	dir := filepath.Join(root, RegDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, identity+fileSuffix), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaimAndUnclaimWriteNoLedgerForAnIdentityWithoutARegistration(t *testing.T) {
	root := t.TempDir()

	fresh, err := Claim(root, "gone", []string{"mail:a"}, t0)
	if !errors.Is(err, ErrNoRegistration) || len(fresh) != 0 {
		t.Fatalf("Claim = %v, %v, want ErrNoRegistration and no keys", fresh, err)
	}

	if err := Unclaim(root, "gone", []string{"mail:a"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, RingDir, "gone"+fileSuffix)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a ring ledger exists for an identity with no registration: %v", err)
	}
}

// A shared host must not turn another conversation into a pre-clear alias. Claude's process aliases
// are covered separately; these sessions exist concurrently.
func TestSharedHostMailRegistrationsKeepConcurrentSessionsSeparate(t *testing.T) {
	root := t.TempDir()
	for _, agent := range []string{"codex", "opencode"} {
		one := reg(25, "first")
		one.Agent = agent
		a, err := Refresh(root, one, t0)
		if err != nil {
			t.Fatal(err)
		}
		two := one
		two.Current = "second"
		b, err := Refresh(root, two, t0)
		if err != nil {
			t.Fatal(err)
		}
		if a.Identity == b.Identity || b.Holds("first") {
			t.Fatalf("%s merged concurrent sessions: %+v %+v", agent, a, b)
		}
	}
}

func TestForeignRegistrationWithAnAbsentLocalPIDStaysUnknownWhileALocalDeadHostIsDead(t *testing.T) {
	table := proctest.New(record.PlatformDarwin, 1)
	checker := &liveness.Checker{Procs: table, Local: machine}
	foreign := reg(999, "foreign")
	foreign.Machine = record.Machine{ID: "another-machine", Host: "other"}
	if !Live(foreign, checker) {
		t.Fatal("a foreign machine's missing local PID was classified dead")
	}
	local := reg(999, "local")
	if Live(local, checker) {
		t.Fatal("a genuinely dead local process was classified live")
	}
}
