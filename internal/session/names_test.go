package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/record"
)

type nameTest struct {
	Name string
	In   string
	Want Key
	Bad  bool
}

var nameTests = []nameTest{
	{Name: "macOS", In: "codex.01a1.3067.1792103390-449120.json",
		Want: Key{Agent: "codex", Session: "01a1", PID: 3067, Start: "1792103390-449120"}},
	{Name: "linux", In: "opencode.ses_ab-c.12.deadbeef-99-4026531836",
		Want: Key{Agent: "opencode", Session: "ses_ab-c", PID: 12, Start: "deadbeef-99-4026531836"}},
	{Name: "no suffix", In: "codex.t.1.1-2", Want: Key{Agent: "codex", Session: "t", PID: 1, Start: "1-2"}},
	{Name: "too few fields", In: "codex.t.1", Bad: true},
	{Name: "session with a dot", In: "codex.a.b.1.1-2", Bad: true},
	{Name: "empty session", In: "codex..1.1-2", Bad: true},
	{Name: "session with a slash", In: "codex.a/b.1.1-2", Bad: true},
	{Name: "session too long", In: "codex." + strings.Repeat("a", 129) + ".1.1-2", Bad: true},
	{Name: "pid not a number", In: "codex.t.x.1-2", Bad: true},
	{Name: "pid zero", In: "codex.t.0.1-2", Bad: true},
	{Name: "start with uppercase", In: "codex.t.1.AB", Bad: true},
	{Name: "start with a letter past f", In: "codex.t.1.zz", Bad: true},
	{Name: "agent with uppercase", In: "Codex.t.1.1-2", Bad: true},
}

func TestParseKey(t *testing.T) {
	for _, test := range nameTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ParseKey(test.In)

			if test.Bad {
				if !errors.Is(err, ErrBadName) {
					t.Errorf("ParseKey(%q) = %+v, %v; want ErrBadName", test.In, got, err)
				}

				return
			}

			if err != nil || got != test.Want {
				t.Errorf("ParseKey(%q) = %+v, %v; want %+v", test.In, got, err, test.Want)
			}

			if got.Name()+".json" != got.FileName() {
				t.Errorf("FileName = %q", got.FileName())
			}
		})
	}
}

func TestValidSession(t *testing.T) {
	for _, ok := range []string{"a", "A_b-9", strings.Repeat("x", 128), "01a15245-b963-7900-844f-cbbd4aaaa364"} {
		if !ValidSession(ok) {
			t.Errorf("ValidSession(%q) = false", ok)
		}
	}

	for _, bad := range []string{"", "a.b", "a b", "../x", "a/b", "a\x00", "é", strings.Repeat("x", 129)} {
		if ValidSession(bad) {
			t.Errorf("ValidSession(%q) = true", bad)
		}
	}
}

func TestStartTextRoundTripsThroughParseStart(t *testing.T) {
	for _, start := range []record.ProcessStart{
		{Platform: record.PlatformDarwin, Sec: 1792103390, Usec: 449120},
		{Platform: record.PlatformLinux, BootID: "0a1b2c", Ticks: 99, PIDNS: 4026531836},
	} {
		got, ok := ParseStart(start.Platform, start.String())
		if !ok || !got.Equal(start) {
			t.Errorf("ParseStart(%q) = %+v, %v; want %+v", start.String(), got, ok, start)
		}
	}

	for _, bad := range []string{"", "1", "1-2-3", "a-b"} {
		if _, ok := ParseStart(record.PlatformDarwin, bad); ok {
			t.Errorf("ParseStart(darwin, %q) parsed", bad)
		}
	}

	if _, ok := ParseStart(record.PlatformLinux, "1-2"); ok {
		t.Error("a two-field start parsed as Linux")
	}
}
