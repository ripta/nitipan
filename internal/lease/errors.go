package lease

import (
	"errors"
	"fmt"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/record"
)

// Failures with a message of their own and no status of their own.
var (
	// ErrMissingSession means a multi-session agent exported no session ID, so its holder cannot be
	// told from another session in the same process.
	ErrMissingSession = errors.New("this agent hosts several sessions in one process and exported no session ID, " +
		"so nitipan cannot tell this session from the others")

	// ErrHandleCollision means two different live holders derived the same handle.
	ErrHandleCollision = errors.New("handle collision with another live holder")
)

// ErrWaitable marks the refusals that `acquire --wait` waits out: a live holder overlaps, or a slot is
// full. It never changes an exit status. Every other refusal ends a wait at once.
var ErrWaitable = errors.New("a refusal that a wait can outlast")

// statusError carries a message and the exit-status sentinel it maps to. The sentinel stays out of
// the message, so the text reads as written.
type statusError struct {
	msg    string
	status error

	// waitable is set only on the refusals that a later attempt can see lifted.
	waitable bool
}

// Error implements error.
func (e *statusError) Error() string { return e.msg }

// Is matches the sentinel the exit-status table keys on, and ErrWaitable when the refusal is one.
func (e *statusError) Is(target error) bool {
	return target == e.status || (e.waitable && target == ErrWaitable)
}

func failf(status error, format string, args ...any) error {
	return &statusError{msg: fmt.Sprintf(format, args...), status: status}
}

// waitablef is a refusal that exits as refused, and that a wait retries.
func waitablef(format string, args ...any) error {
	return &statusError{msg: fmt.Sprintf(format, args...), status: exitcode.ErrRefused, waitable: true}
}

func usagef(format string, args ...any) error {
	return failf(exitcode.ErrUsage, format, args...)
}

// unusableErr names the file of a record this nitipan cannot use.
func unusableErr(en *entry) error {
	msg := fmt.Sprintf("%s cannot be used by this nitipan (%s); upgrade nitipan or ask the user", en.file(),
		en.Bad.Reason)

	return failf(record.ErrUnusable, "%s%s", msg, strayFileHint(en.Bad.Reason))
}

// strayFileHint explains the one case that no command can fix. A file whose name is not an ID cannot
// be addressed, because break takes IDs only so that it always names its exact target.
func strayFileHint(reason record.Reason) string {
	if reason != reasonBadName && reason != reasonBadTombstoneName {
		return ""
	}

	return ". Its name is not an ID, so no nitipan command can address it. After checking what it is, " +
		"the user can delete the file by hand"
}
