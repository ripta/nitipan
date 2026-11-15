package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OpencodeDB reads the one fact nitipan needs from opencode's database.
type OpencodeDB interface {
	// Suspended reports whether the session's time_suspended is set. A set value means the next
	// service boot resumes the turn. A session the database does not know reads clear, because it
	// has no turn to resume. Any failure to read is an error, and the caller reads unknown.
	Suspended(session string) (bool, error)

	// SuspendedAmong answers Suspended for several sessions in one read, within the budget. A
	// session missing from the result is clear. A read that fails or runs out of time is an error
	// for all of them.
	SuspendedAmong(sessions []string, budget time.Duration) (map[string]bool, error)
}

// SqliteCLI reads opencode's database by running `sqlite3 -readonly`. nitipan takes no SQLite
// library, so a missing sqlite3 binary, a missing database, or a schema change all surface as an
// error, which reads unknown.
type SqliteCLI struct {
	// Path is the database file, ~/.local/share/opencode/opencode.db.
	Path string

	// Bin is the sqlite3 program. Empty means "sqlite3" on PATH.
	Bin string

	// Timeout bounds one single-session read. Zero means three seconds.
	Timeout time.Duration
}

// Suspended implements OpencodeDB.
func (c SqliteCLI) Suspended(session string) (bool, error) {
	if !ValidSession(session) {
		return false, fmt.Errorf("session %q cannot be used in a query", session)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}

	// The session ID passed the grammar above, so it holds no quote and is safe inside the literal.
	query := fmt.Sprintf("SELECT CASE WHEN time_suspended IS NULL THEN 0 ELSE 1 END FROM session_v2 WHERE id = '%s';",
		session)

	out, err := c.run(query, timeout)
	if err != nil {
		return false, err
	}

	switch out {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("opencode database %s: unexpected answer %q", c.Path, out)
	}
}

// SuspendedAmong implements OpencodeDB.
func (c SqliteCLI) SuspendedAmong(sessions []string, budget time.Duration) (map[string]bool, error) {
	quoted := make([]string, 0, len(sessions))

	for _, s := range sessions {
		if !ValidSession(s) {
			return nil, fmt.Errorf("session %q cannot be used in a query", s)
		}

		quoted = append(quoted, "'"+s+"'")
	}

	query := fmt.Sprintf("SELECT id FROM session_v2 WHERE time_suspended IS NOT NULL AND id IN (%s);",
		strings.Join(quoted, ","))

	out, err := c.run(query, budget)
	if err != nil {
		return nil, err
	}

	set := map[string]bool{}

	for _, id := range strings.Fields(out) {
		set[id] = true
	}

	return set, nil
}

// run executes one query and returns its trimmed output.
func (c SqliteCLI) run(query string, timeout time.Duration) (string, error) {
	if _, err := os.Stat(c.Path); err != nil {
		return "", fmt.Errorf("opencode database %s: %w", c.Path, err)
	}

	bin := c.Bin
	if bin == "" {
		bin = "sqlite3"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-readonly", "-batch", "-noheader", c.Path, query)

	// A killed program can leave a child holding its output pipe. WaitDelay stops Output from waiting
	// on it past the budget.
	cmd.WaitDelay = 100 * time.Millisecond

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("reading opencode database %s: no answer within %s", c.Path, timeout)
		}

		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("reading opencode database %s: %s", c.Path, strings.TrimSpace(string(exit.Stderr)))
		}

		return "", fmt.Errorf("reading opencode database %s: %w", c.Path, err)
	}

	return strings.TrimSpace(string(out)), nil
}
