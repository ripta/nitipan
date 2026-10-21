package proc

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/space-armada/nitipan/internal/record"
)

// statLine builds a stat line whose 22nd field is ticks.
func statLine(pid, comm, state, ppid, ticks string) string {
	return pid + " (" + comm + ") " + state + " " + ppid +
		" 1 1 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 " + ticks + " 1000 100 18446744073709551615\n"
}

type parseStatTest struct {
	Name    string
	Line    string
	Want    Stat
	WantErr bool
}

var parseStatTests = []parseStatTest{
	{
		Name: "plain name",
		Line: statLine("42", "bash", "S", "7", "123456"),
		Want: Stat{Comm: "bash", State: 'S', PPID: 7, StartTicks: 123456},
	},
	{
		Name: "name with spaces",
		Line: statLine("42", "my daemon", "R", "7", "99"),
		Want: Stat{Comm: "my daemon", State: 'R', PPID: 7, StartTicks: 99},
	},
	{
		Name: "name with parentheses",
		Line: statLine("42", "a) S 1 (b", "T", "9", "5"),
		Want: Stat{Comm: "a) S 1 (b", State: 'T', PPID: 9, StartTicks: 5},
	},
	{
		Name: "name with spaces and nested parentheses",
		Line: statLine("42", "(sd-pam) x (y)", "S", "1", "777"),
		Want: Stat{Comm: "(sd-pam) x (y)", State: 'S', PPID: 1, StartTicks: 777},
	},
	{
		Name:    "no parentheses",
		Line:    "42 bash S 7\n",
		WantErr: true,
	},
	{
		Name:    "too few fields",
		Line:    "42 (bash) S 7 1\n",
		WantErr: true,
	},
	{
		Name:    "bad starttime",
		Line:    statLine("42", "bash", "S", "7", "soon"),
		WantErr: true,
	},
	{
		Name:    "empty",
		Line:    "",
		WantErr: true,
	},
}

func TestParseStat(t *testing.T) {
	for _, test := range parseStatTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ParseStat([]byte(test.Line))
			if (err != nil) != test.WantErr {
				t.Fatalf("err = %v, want error %v", err, test.WantErr)
			}

			if !test.WantErr && got != test.Want {
				t.Errorf("stat = %+v, want %+v", got, test.Want)
			}
		})
	}
}

type namespaceTest struct {
	Name    string
	Link    string
	Want    uint64
	WantErr bool
}

var namespaceTests = []namespaceTest{
	{Name: "typical", Link: "pid:[4026531836]", Want: 4026531836},
	{Name: "other namespace kind", Link: "net:[4026531840]", WantErr: true},
	{Name: "missing bracket", Link: "pid:[4026531836", WantErr: true},
	{Name: "not a number", Link: "pid:[abc]", WantErr: true},
}

func TestParseNamespaceInode(t *testing.T) {
	for _, test := range namespaceTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ParseNamespaceInode(test.Link)
			if (err != nil) != test.WantErr || got != test.Want {
				t.Errorf("ParseNamespaceInode(%q) = %d, %v", test.Link, got, err)
			}
		})
	}
}

func TestSplitCmdline(t *testing.T) {
	got := SplitCmdline([]byte("codex\x00app-server\x00--managed-daemon\x00"))
	want := []string{"codex", "app-server", "--managed-daemon"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}

	if args := SplitCmdline(nil); args != nil {
		t.Errorf("empty cmdline = %q, want nil", args)
	}

	if args := SplitCmdline([]byte("a\x00\x00b\x00")); !reflect.DeepEqual(args, []string{"a", "", "b"}) {
		t.Errorf("an empty argument was lost: %q", args)
	}
}

func procTree() fstest.MapFS {
	return fstest.MapFS{
		"sys/kernel/random/boot_id": {Data: []byte("ABCDEF01-2345-6789-abcd-ef0123456789\n")},
		"self":                      {Data: []byte("100"), Mode: fs.ModeSymlink},
		"100/ns/pid":                {Data: []byte("pid:[4026531836]"), Mode: fs.ModeSymlink},
		"100/stat":                  {Data: []byte(statLine("100", "nitipan", "R", "50", "9000"))},
		"100/cmdline":               {Data: []byte("nitipan\x00list\x00")},
		"50/stat":                   {Data: []byte(statLine("50", "my shell (x)", "S", "1", "8000"))},
		"50/cmdline":                {Data: []byte("zsh\x00")},
		"60/stat":                   {Data: []byte(statLine("60", "sleeper", "T", "1", "8500"))},
	}
}

func TestLinuxTableReadsFixtureTree(t *testing.T) {
	table := NewLinuxTable(procTree(), 100)

	start, err := table.Start(50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := record.ProcessStart{
		Platform: record.PlatformLinux,
		BootID:   "abcdef0123456789abcdef0123456789",
		Ticks:    8000,
		PIDNS:    4026531836,
	}
	if !start.Equal(want) {
		t.Errorf("start = %+v, want %+v", start, want)
	}

	if ppid, err := table.Parent(100); err != nil || ppid != 50 {
		t.Errorf("Parent = %d, %v", ppid, err)
	}

	if name, err := table.Name(50); err != nil || name != "my shell (x)" {
		t.Errorf("Name = %q, %v", name, err)
	}

	if args, err := table.Args(100); err != nil || !reflect.DeepEqual(args, []string{"nitipan", "list"}) {
		t.Errorf("Args = %q, %v", args, err)
	}

	if stopped, err := table.Stopped(60); err != nil || !stopped {
		t.Errorf("Stopped(60) = %v, %v", stopped, err)
	}

	if stopped, err := table.Stopped(50); err != nil || stopped {
		t.Errorf("Stopped(50) = %v, %v", stopped, err)
	}
}

func TestLinuxTableMissingProcessIsNoProcess(t *testing.T) {
	table := NewLinuxTable(procTree(), 100)

	if _, err := table.Start(999); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Start(999) = %v, want ErrNoProcess", err)
	}

	if _, err := table.Parent(999); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Parent(999) = %v, want ErrNoProcess", err)
	}
}

type unreadableTest struct {
	Name   string
	Remove string
}

var unreadableTests = []unreadableTest{
	{Name: "boot id", Remove: "sys/kernel/random/boot_id"},
	{Name: "pid namespace link", Remove: "100/ns/pid"},
}

func TestLinuxTableUnreadableFilesAreNotDead(t *testing.T) {
	for _, test := range unreadableTests {
		t.Run(test.Name, func(t *testing.T) {
			fsys := procTree()
			delete(fsys, test.Remove)

			table := NewLinuxTable(fsys, 100)

			_, err := table.Start(50)
			if err == nil || errors.Is(err, ErrNoProcess) {
				t.Errorf("Start = %v, want an error that is not ErrNoProcess", err)
			}
		})
	}
}

func TestLinuxTableMalformedStatIsNotDead(t *testing.T) {
	fsys := procTree()
	fsys["50/stat"] = &fstest.MapFile{Data: []byte("garbage")}

	table := NewLinuxTable(fsys, 100)

	_, err := table.Start(50)
	if err == nil || errors.Is(err, ErrNoProcess) {
		t.Errorf("Start = %v, want an error that is not ErrNoProcess", err)
	}
}

func TestProcArgs2(t *testing.T) {
	buf := binary.NativeEndian.AppendUint32(nil, 2)
	buf = append(buf, []byte("/usr/bin/codex\x00\x00\x00codex\x00app-server\x00HOME=/x\x00")...)

	got, err := ParseProcArgs2(buf)
	if err != nil {
		t.Fatalf("ParseProcArgs2: %v", err)
	}

	if want := []string{"codex", "app-server"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}

	if _, err := ParseProcArgs2(buf[:10]); err == nil {
		t.Error("a truncated buffer should fail")
	}

	if _, err := ParseProcArgs2([]byte{1}); err == nil {
		t.Error("a buffer shorter than its header should fail")
	}
}
