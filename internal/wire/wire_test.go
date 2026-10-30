package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type sseCase struct {
	Name   string
	Stream string
	Want   []SSE
	Err    error
}

var sseCases = []sseCase{
	{
		Name:   "one event",
		Stream: "id: 1\nevent: note\ndata: {\"a\":1}\n\n",
		Want:   []SSE{{ID: "1", Event: "note", Data: `{"a":1}`}},
	},
	{
		Name:   "comments and unknown fields are skipped",
		Stream: ": keepalive\n\nretry: 100\nfoo: bar\nid: 2\ndata: x\n\n",
		Want:   []SSE{{ID: "2", Data: "x"}},
	},
	{
		Name:   "several data lines join with newlines",
		Stream: "data: a\ndata: b\n\n",
		Want:   []SSE{{Data: "a\nb"}},
	},
	{
		Name:   "CRLF line ends",
		Stream: "id: 3\r\ndata: y\r\n\r\n",
		Want:   []SSE{{ID: "3", Data: "y"}},
	},
	{
		Name:   "no space after the colon",
		Stream: "data:z\n\n",
		Want:   []SSE{{Data: "z"}},
	},
	{
		Name:   "an event cut off mid-way is dropped",
		Stream: "data: a\n\ndata: b\n",
		Want:   []SSE{{Data: "a"}},
		Err:    io.EOF,
	},
	{
		Name:   "an event with no data is not dispatched",
		Stream: "id: 4\n\ndata: q\n\n",
		Want:   []SSE{{Data: "q"}},
	},
}

func TestParserReadsEvents(t *testing.T) {
	for _, tc := range sseCases {
		t.Run(tc.Name, func(t *testing.T) {
			p := NewParser(strings.NewReader(tc.Stream))

			var got []SSE

			var err error

			for {
				var ev SSE

				ev, err = p.Next()
				if err != nil {
					break
				}

				got = append(got, ev)
			}

			if len(got) != len(tc.Want) {
				t.Fatalf("events = %+v, want %+v", got, tc.Want)
			}

			for i := range got {
				if got[i] != tc.Want[i] {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tc.Want[i])
				}
			}

			wantErr := tc.Err
			if wantErr == nil {
				wantErr = io.EOF
			}

			if !errors.Is(err, wantErr) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("end error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestWriteSSERoundTripsAPayloadWithNewlines(t *testing.T) {
	var buf bytes.Buffer

	in := SSE{ID: "9", Event: "mail", Data: "line one\nline two\n\nafter blank"}

	if err := WriteComment(&buf, "keepalive\ninjected"); err != nil {
		t.Fatal(err)
	}

	if err := WriteSSE(&buf, in); err != nil {
		t.Fatal(err)
	}

	got, err := NewParser(&buf).Next()
	if err != nil || got != in {
		t.Errorf("round trip = %+v, %v, want %+v", got, err, in)
	}
}

func TestParserReportsEveryLineToTheWatchdog(t *testing.T) {
	p := NewParser(strings.NewReader(": a\n: b\ndata: c\n\n"))

	lines := 0
	p.OnLine = func() { lines++ }

	if _, err := p.Next(); err != nil || lines != 4 {
		t.Errorf("Next = %v after %d lines, want 4", err, lines)
	}
}

type versionCase struct {
	Name string
	JSON string
	Err  bool
	Want Version
}

var versionCases = []versionCase{
	{Name: "current", JSON: `"1.0"`, Want: Version{1, 0}},
	{Name: "higher minor reads", JSON: `"1.7"`, Want: Version{1, 7}},
	{Name: "not a version", JSON: `"one"`, Err: true},
	{Name: "not a string", JSON: `1`, Err: true},
}

func TestVersionDecodeAndCheck(t *testing.T) {
	for _, tc := range versionCases {
		t.Run(tc.Name, func(t *testing.T) {
			var v Version

			err := json.Unmarshal([]byte(tc.JSON), &v)
			if (err != nil) != tc.Err {
				t.Fatalf("Unmarshal err = %v, want error %v", err, tc.Err)
			}

			if err == nil && v != tc.Want {
				t.Errorf("version = %v, want %v", v, tc.Want)
			}

			if err == nil && v.Check() != nil {
				t.Errorf("Check(%v) = %v, want nil", v, v.Check())
			}
		})
	}

	if err := (Version{Major: 2}).Check(); !errors.Is(err, ErrVersion) {
		t.Errorf("Check(2.0) = %v, want ErrVersion", err)
	}
}

func TestMessagesIgnoreFieldsAddedByAHigherMinor(t *testing.T) {
	var reg Register

	body := `{"version":"1.4","machine_id":"m","uid":501,"user":"u","host":"h","node_version":"v","https":true,"future":{"x":1}}`
	if err := json.Unmarshal([]byte(body), &reg); err != nil || reg.MachineID != "m" || reg.Version.Minor != 4 {
		t.Errorf("Register = %+v, %v", reg, err)
	}
}

type hostCase struct {
	Host string
	Want string
}

var hostCases = []hostCase{
	{"localhost", HostLoopback},
	{"127.0.0.1", HostLoopback},
	{"::1", HostLoopback},
	{"[::1]", HostLoopback},
	{"10.1.2.3", HostPrivate},
	{"192.168.0.9", HostPrivate},
	{"172.16.5.5", HostPrivate},
	{"fd00::1", HostPrivate},
	{"169.254.1.1", HostPrivate},
	{"8.8.8.8", HostPublic},
	{"0.0.0.0", HostPublic},
	{"::", HostPublic},
	{"", HostPublic},
	{"broker.example", HostPublic},
	{"2001:db8::1", HostPublic},
}

func TestClassifyHost(t *testing.T) {
	for _, tc := range hostCases {
		t.Run(tc.Host, func(t *testing.T) {
			if got := ClassifyHost(tc.Host); got != tc.Want {
				t.Errorf("ClassifyHost(%q) = %s, want %s", tc.Host, got, tc.Want)
			}
		})
	}
}
