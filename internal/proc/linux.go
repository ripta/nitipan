package proc

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// Stat holds the fields of /proc/<pid>/stat that nitipan reads.
type Stat struct {
	Comm       string
	State      byte
	PPID       int
	StartTicks uint64
}

// statFieldsAfterComm is how many whitespace-separated fields follow the command name, up to and
// including starttime. starttime is field 22 of the line and the state is field 3.
const statFieldsAfterComm = 20

// ParseStat parses a /proc/<pid>/stat line.
//
// The command name sits in parentheses and may itself contain spaces and parentheses, so the line
// is split at the last closing parenthesis, never at the first.
func ParseStat(data []byte) (Stat, error) {
	open := bytes.IndexByte(data, '(')
	closing := bytes.LastIndexByte(data, ')')

	if open < 1 || closing < open {
		return Stat{}, errors.New("stat line has no parenthesized command name")
	}

	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) < statFieldsAfterComm {
		return Stat{}, fmt.Errorf("stat line has %d fields after the command name, want %d",
			len(fields), statFieldsAfterComm)
	}

	if len(fields[0]) != 1 {
		return Stat{}, fmt.Errorf("stat state %q is not one character", fields[0])
	}

	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Stat{}, fmt.Errorf("stat ppid: %w", err)
	}

	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Stat{}, fmt.Errorf("stat starttime: %w", err)
	}

	return Stat{
		Comm:       string(data[open+1 : closing]),
		State:      fields[0][0],
		PPID:       ppid,
		StartTicks: ticks,
	}, nil
}

// ParseNamespaceInode parses the target of /proc/<pid>/ns/pid, which reads "pid:[4026531836]".
func ParseNamespaceInode(link string) (uint64, error) {
	inner, ok := strings.CutPrefix(link, "pid:[")
	if !ok {
		return 0, fmt.Errorf("namespace link %q is not pid:[inode]", link)
	}

	inner, ok = strings.CutSuffix(inner, "]")
	if !ok {
		return 0, fmt.Errorf("namespace link %q is not pid:[inode]", link)
	}

	inode, err := strconv.ParseUint(inner, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("namespace link %q: %w", link, err)
	}

	return inode, nil
}

// LinuxTable reads a /proc tree. It takes the tree as an fs.FS rooted at /proc, so tests run it on
// fixtures from any platform.
type LinuxTable struct {
	fsys fs.FS
	self int
}

// NewLinuxTable returns a table over a /proc tree. The tree must expose "self" as a link to the
// calling process, as /proc does.
func NewLinuxTable(fsys fs.FS, self int) *LinuxTable {
	return &LinuxTable{fsys: fsys, self: self}
}

// Platform implements Table.
func (t *LinuxTable) Platform() string {
	return record.PlatformLinux
}

// Self implements Table.
func (t *LinuxTable) Self() int {
	return t.self
}

// Start implements Table.
//
// Order matters. The boot ID and the namespace come first: when either is unreadable the answer is
// unknown, whether or not the process exists. Only then does a missing stat file mean the process
// is gone.
func (t *LinuxTable) Start(pid int) (record.ProcessStart, error) {
	boot, err := t.bootID()
	if err != nil {
		return record.ProcessStart{}, err
	}

	pidns, err := t.pidNamespace()
	if err != nil {
		return record.ProcessStart{}, err
	}

	st, err := t.stat(pid)
	if err != nil {
		return record.ProcessStart{}, err
	}

	return record.ProcessStart{
		Platform: record.PlatformLinux,
		BootID:   boot,
		Ticks:    st.StartTicks,
		PIDNS:    pidns,
	}, nil
}

// Parent implements Table.
func (t *LinuxTable) Parent(pid int) (int, error) {
	st, err := t.stat(pid)
	if err != nil {
		return 0, err
	}

	return st.PPID, nil
}

// Name implements Table.
func (t *LinuxTable) Name(pid int) (string, error) {
	st, err := t.stat(pid)
	if err != nil {
		return "", err
	}

	return st.Comm, nil
}

// Stopped implements Table.
func (t *LinuxTable) Stopped(pid int) (bool, error) {
	st, err := t.stat(pid)
	if err != nil {
		return false, err
	}

	return st.State == 'T' || st.State == 't', nil
}

// Args implements Table.
func (t *LinuxTable) Args(pid int) ([]string, error) {
	data, err := fs.ReadFile(t.fsys, fmt.Sprintf("%d/cmdline", pid))
	if err != nil {
		return nil, mapReadError(fmt.Sprintf("reading cmdline of %d", pid), err)
	}

	return SplitCmdline(data), nil
}

// SplitCmdline splits the NUL-separated contents of /proc/<pid>/cmdline.
func SplitCmdline(data []byte) []string {
	data = bytes.TrimSuffix(data, []byte{0})
	if len(data) == 0 {
		return nil
	}

	parts := bytes.Split(data, []byte{0})
	args := make([]string, len(parts))

	for i, part := range parts {
		args[i] = string(part)
	}

	return args
}

func (t *LinuxTable) stat(pid int) (Stat, error) {
	data, err := fs.ReadFile(t.fsys, fmt.Sprintf("%d/stat", pid))
	if err != nil {
		return Stat{}, mapReadError(fmt.Sprintf("reading stat of %d", pid), err)
	}

	st, err := ParseStat(data)
	if err != nil {
		return Stat{}, fmt.Errorf("parsing stat of %d: %w", pid, err)
	}

	return st, nil
}

func (t *LinuxTable) bootID() (string, error) {
	data, err := fs.ReadFile(t.fsys, "sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("reading boot id: %w", err)
	}

	id := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(string(data)), "-", ""))
	if id == "" {
		return "", errors.New("boot id is empty")
	}

	return id, nil
}

func (t *LinuxTable) pidNamespace() (uint64, error) {
	link, err := fs.ReadLink(t.fsys, "self/ns/pid")
	if err != nil {
		return 0, fmt.Errorf("reading pid namespace: %w", err)
	}

	return ParseNamespaceInode(link)
}

// mapReadError turns "no such file" into ErrNoProcess. Every other failure stays unknown.
func mapReadError(context string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", context, ErrNoProcess)
	}

	return fmt.Errorf("%s: %w", context, err)
}
