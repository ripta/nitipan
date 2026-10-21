package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FormatVersion is the highest state-directory format this binary can write. A directory with no
// format marker is format 1.
const FormatVersion = 1

// ErrFormatRefused matches every FormatError. A refusal forbids writes. Reads still work.
var ErrFormatRefused = errors.New("state directory format forbids writes by this nitipan")

// FormatError explains why the format marker forbids writes.
type FormatError struct {
	// Need is the minimum writer version the marker names. Zero when the marker is undecodable.
	Need int

	Detail string
}

// Error implements error.
func (e *FormatError) Error() string {
	if e.Need > 0 {
		return fmt.Sprintf("the state directory needs nitipan format %d or newer and this nitipan writes format %d; "+
			"upgrade nitipan or ask the user", e.Need, FormatVersion)
	}

	return fmt.Sprintf("the state directory format marker cannot be read (%s); upgrade nitipan or ask the user",
		e.Detail)
}

// Is matches ErrFormatRefused.
func (e *FormatError) Is(target error) bool {
	return target == ErrFormatRefused
}

// checkFormat reads the format marker. An absent marker means format 1. A marker above this
// binary's version, or one that does not decode, forbids writes.
func (d *Dir) checkFormat() error {
	data, err := os.ReadFile(filepath.Join(d.path, formatName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return &FormatError{Detail: err.Error()}
	}

	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 1 {
		return &FormatError{Detail: fmt.Sprintf("marker holds %q", strings.TrimSpace(string(data)))}
	}

	if n > FormatVersion {
		return &FormatError{Need: n}
	}

	return nil
}

// FormatErr returns the reason the format marker forbids writes, or nil.
func (l *Locked) FormatErr() error {
	return l.formatErr
}

// writable is the guard every write passes through. The marker is checked under the lock, so a
// newer binary's upgrade cannot interleave with an older binary's write.
func (l *Locked) writable() error {
	if l.file == nil {
		return errors.New("writing without the state lock")
	}

	return l.formatErr
}
