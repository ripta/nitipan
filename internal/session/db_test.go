package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureDB builds an opencode-shaped database with the sqlite3 program, or skips the test when the
// program is missing.
func fixtureDB(t *testing.T) string {
	t.Helper()

	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed, so the database fixture cannot be built")
	}

	path := filepath.Join(t.TempDir(), "opencode.db")

	script := "CREATE TABLE session_v2 (id text PRIMARY KEY, time_suspended integer);" +
		"INSERT INTO session_v2 VALUES ('ses_suspended', 1792103390000);" +
		"INSERT INTO session_v2 VALUES ('ses_clear', NULL);"

	if out, err := exec.Command(bin, path, script).CombinedOutput(); err != nil {
		t.Fatalf("building the fixture: %v\n%s", err, out)
	}

	return path
}

func TestSqliteCLIReadsTimeSuspended(t *testing.T) {
	db := SqliteCLI{Path: fixtureDB(t)}

	for session, want := range map[string]bool{"ses_suspended": true, "ses_clear": false, "ses_unknown": false} {
		got, err := db.Suspended(session)
		if err != nil || got != want {
			t.Errorf("Suspended(%q) = %v, %v; want %v", session, got, err, want)
		}
	}
}

func TestSqliteCLIReadsManySessionsInOneCall(t *testing.T) {
	db := SqliteCLI{Path: fixtureDB(t)}

	got, err := db.SuspendedAmong([]string{"ses_suspended", "ses_clear", "ses_unknown"}, time.Second)
	if err != nil || len(got) != 1 || !got["ses_suspended"] {
		t.Errorf("SuspendedAmong = %v, %v", got, err)
	}

	if _, err := db.SuspendedAmong([]string{"ok", "bad'; --"}, time.Second); err == nil {
		t.Error("a session ID outside the allowed set reached the query")
	}
}

func TestSqliteCLISweepReadGivesUpWithinItsBudget(t *testing.T) {
	good := fixtureDB(t)

	slow := filepath.Join(t.TempDir(), "slow-sqlite3")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()

	_, err := (SqliteCLI{Path: good, Bin: slow}).SuspendedAmong([]string{"ses_clear"}, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("err = %v", err)
	}

	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the read took %s, well past its 200ms budget", took)
	}
}

func TestSqliteCLIFailuresAreErrors(t *testing.T) {
	good := fixtureDB(t)

	missing := SqliteCLI{Path: filepath.Join(t.TempDir(), "absent.db")}
	if _, err := missing.Suspended("ses_clear"); err == nil {
		t.Error("a missing database read clear")
	}

	if _, err := (SqliteCLI{Path: good, Bin: "no-such-sqlite3-program"}).Suspended("ses_clear"); err == nil {
		t.Error("a missing sqlite3 program read clear")
	}

	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := (SqliteCLI{Path: garbage}).Suspended("ses_clear"); err == nil {
		t.Error("a file that is not a database read clear")
	}

	if _, err := (SqliteCLI{Path: good}).Suspended("x'; DROP TABLE session_v2; --"); err == nil {
		t.Error("a session ID outside the allowed set reached the query")
	}
}

func TestSqliteCLIOpensTheDatabaseReadOnly(t *testing.T) {
	path := fixtureDB(t)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (SqliteCLI{Path: path}).Suspended("ses_clear"); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf("reading changed the database: %v -> %v (%v)", before, after, err)
	}
}
