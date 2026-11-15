package keys

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

type casefoldTest struct {
	Name  string
	Flags uint32
	Err   error
	Want  Sensitivity
}

var casefoldTests = []casefoldTest{
	{Name: "plain directory", Flags: 0x00080000, Want: Sensitivity{}},
	{Name: "casefold directory", Flags: fsCasefoldFlag | 0x00080000, Want: Sensitivity{FoldCase: true, Normalize: true}},
	{Name: "no flags ioctl on this filesystem", Err: syscall.ENOTTY, Want: Sensitivity{}},
	{Name: "ioctl not supported", Err: syscall.EOPNOTSUPP, Want: Sensitivity{}},
	{Name: "invalid for this inode", Err: syscall.EINVAL, Want: Sensitivity{}},
	{Name: "wrapped no-support error", Err: fmt.Errorf("ioctl: %w", syscall.ENOTTY), Want: Sensitivity{}},
	{Name: "permission denied fails safe", Err: syscall.EACCES, Want: Sensitivity{FoldCase: true, Normalize: true}},
	{Name: "unknown error fails safe", Err: errors.New("boom"), Want: Sensitivity{FoldCase: true, Normalize: true}},
}

func TestCasefoldSensitivity(t *testing.T) {
	for _, test := range casefoldTests {
		t.Run(test.Name, func(t *testing.T) {
			read := func(string) (uint32, error) { return test.Flags, test.Err }

			if got := casefoldSensitivity(read, "/some/dir"); got != test.Want {
				t.Errorf("sensitivity = %+v, want %+v", got, test.Want)
			}
		})
	}
}

func TestCasefoldDecisionReadsTheGivenDirectory(t *testing.T) {
	var asked string

	casefoldSensitivity(func(dir string) (uint32, error) {
		asked = dir

		return 0, nil
	}, "/deepest/existing")

	if asked != "/deepest/existing" {
		t.Errorf("asked about %q", asked)
	}
}
