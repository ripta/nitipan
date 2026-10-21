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

// statusError carries a message and the exit-status sentinel it maps to. The sentinel stays out of
// the message, so the text reads as written.
type statusError struct {
	msg    string
	status error
}

// Error implements error.
func (e *statusError) Error() string { return e.msg }

// Is matches the sentinel the exit-status table keys on.
func (e *statusError) Is(target error) bool { return target == e.status }

func failf(status error, format string, args ...any) error {
	return &statusError{msg: fmt.Sprintf(format, args...), status: status}
}

func usagef(format string, args ...any) error {
	return failf(exitcode.ErrUsage, format, args...)
}

// unusableErr names the file of a record this nitipan cannot use.
func unusableErr(en *entry) error {
	return failf(record.ErrUnusable, "%s cannot be used by this nitipan (%s); upgrade nitipan or ask the user",
		en.file(), en.Bad.Reason)
}
