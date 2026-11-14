package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
)

const testID = "01JAAAAAAAAAAAAAAAAAAAAAAA"

func sampleRecord() *Record {
	key := keys.Key{Path: "/repo/src", FoldCase: true, Normalize: true}

	return &Record{
		Envelope: Envelope{
			Version:       PathVersion,
			ID:            testID,
			Footprint:     PathFootprint([]keys.Key{key}),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       Machine{Host: "laptop", ID: "machine-1"},
			Holder: Holder{
				Agent:     "claude",
				SessionID: "sess-1",
				Handle:    "amber-otter-lamp-quill",
				Name:      "guardian",
				PID:       4242,
				Start:     ProcessStart{Platform: PlatformDarwin, Sec: 1700000000, Usec: 25},
				User:      "ripta",
			},
		},
		Reach: ReachMachine,
		Domain: Domain{
			Kind:     KindPath,
			Checkout: "/repo",
			Scopes:   []string{"src"},
			Keys:     []keys.Key{key},
		},
		ActingFor:      "ripta",
		PermissionMode: "default",
		Description:    "owns src",
		AcquiredAt:     time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Absorbs:        []string{"01JBBBBBBBBBBBBBBBBBBBBBBB"},
		BodyKnown:      true,
	}
}

func mustEncode(t *testing.T, r *Record) []byte {
	t.Helper()

	data, err := r.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	return data
}

func TestRecordRoundTripsEveryField(t *testing.T) {
	want := sampleRecord()

	got, err := Decode(mustEncode(t, want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !bytes.Equal(mustEncode(t, got), mustEncode(t, want)) {
		t.Errorf("round trip changed the record:\n got %s\nwant %s", mustEncode(t, got), mustEncode(t, want))
	}

	if !got.BodyKnown {
		t.Error("a path record should have a known body")
	}

	if got.Holder.Start.Sec != 1700000000 || got.Holder.Start.Usec != 25 {
		t.Errorf("start = %+v", got.Holder.Start)
	}

	if !got.AcquiredAt.Equal(want.AcquiredAt) {
		t.Errorf("acquired_at = %v", got.AcquiredAt)
	}
}

// mutate decodes the sample record into a generic map, applies fn, and encodes it again.
func mutate(t *testing.T, fn func(m map[string]any)) []byte {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(mustEncode(t, sampleRecord()), &m); err != nil {
		t.Fatal(err)
	}

	fn(m)

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// nested returns the object under key, which a test mutates in place. The sample record always has
// the objects the tests ask for.
func nested(m map[string]any, key string) map[string]any {
	sub, _ := m[key].(map[string]any)

	return sub
}

type unusableTest struct {
	Name   string
	Data   func(t *testing.T) []byte
	Reason Reason
}

var unusableTests = []unusableTest{
	{
		Name:   "higher major",
		Data:   func(t *testing.T) []byte { return mutate(t, func(m map[string]any) { m["version"] = "2.0" }) },
		Reason: ReasonHigherMajor,
	},
	{
		Name:   "higher major with a body that would not decode",
		Data:   func(*testing.T) []byte { return []byte(`{"version":"9.3","id":"x","holder":7}`) },
		Reason: ReasonHigherMajor,
	},
	{
		Name:   "not json",
		Data:   func(*testing.T) []byte { return []byte("{not json") },
		Reason: ReasonUndecodable,
	},
	{
		Name:   "no version",
		Data:   func(t *testing.T) []byte { return mutate(t, func(m map[string]any) { delete(m, "version") }) },
		Reason: ReasonUndecodable,
	},
	{
		Name:   "malformed version",
		Data:   func(t *testing.T) []byte { return mutate(t, func(m map[string]any) { m["version"] = "one" }) },
		Reason: ReasonUndecodable,
	},
	{
		Name:   "no id",
		Data:   func(t *testing.T) []byte { return mutate(t, func(m map[string]any) { delete(m, "id") }) },
		Reason: ReasonUndecodable,
	},
	{
		Name:   "holder of the wrong shape",
		Data:   func(t *testing.T) []byte { return mutate(t, func(m map[string]any) { m["holder"] = "nobody" }) },
		Reason: ReasonUndecodable,
	},
	{
		Name: "footprint with a key the body does not have",
		Data: func(t *testing.T) []byte {
			return mutate(t, func(m map[string]any) {
				m["footprint"] = []any{map[string]any{"type": "path", "path": "/elsewhere"}}
			})
		},
		Reason: ReasonFootprintMismatch,
	},
	{
		Name: "footprint all for a known path kind",
		Data: func(t *testing.T) []byte {
			return mutate(t, func(m map[string]any) { m["footprint"] = "all" })
		},
		Reason: ReasonFootprintMismatch,
	},
	{
		Name: "footprint missing for a known path kind",
		Data: func(t *testing.T) []byte {
			return mutate(t, func(m map[string]any) { delete(m, "footprint") })
		},
		Reason: ReasonFootprintMismatch,
	},
}

func TestDecodeRejectsUnusableRecords(t *testing.T) {
	for _, test := range unusableTests {
		t.Run(test.Name, func(t *testing.T) {
			_, err := Decode(test.Data(t))
			if !errors.Is(err, ErrUnusable) {
				t.Fatalf("err = %v, want ErrUnusable", err)
			}

			var unusable *UnusableError
			if !errors.As(err, &unusable) || unusable.Reason != test.Reason {
				t.Errorf("reason = %v, want %q", err, test.Reason)
			}
		})
	}
}

func TestUnusableRecordKeepsReadableFields(t *testing.T) {
	_, err := Decode(mutate(t, func(m map[string]any) { m["version"] = "2.0" }))

	var unusable *UnusableError
	if !errors.As(err, &unusable) {
		t.Fatalf("err = %v", err)
	}

	if unusable.Version != (Version{Major: 2}) {
		t.Errorf("version = %v, want 2.0", unusable.Version)
	}

	if _, ok := unusable.Raw["id"]; !ok {
		t.Error("raw fields lost the id")
	}
}

func TestHigherMinorIsReadWithUnknownFieldsIgnored(t *testing.T) {
	data := mutate(t, func(m map[string]any) {
		m["version"] = "1.7"
		m["fencing_token"] = 99
		nested(m, "domain")["slots"] = 3
		nested(m, "holder")["future"] = true
	})

	rec, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if rec.ID != testID || !rec.BodyKnown {
		t.Errorf("record = %+v", rec)
	}

	if !rec.Clearable() {
		t.Error("a higher-minor record with clear_when_dead true should stay clearable")
	}
}

func TestHigherMinorIsNeverRewritten(t *testing.T) {
	rec, err := Decode(mutate(t, func(m map[string]any) { m["version"] = "1.5" }))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if err := rec.Rewritable(); !errors.Is(err, ErrNewerRecord) {
		t.Errorf("Rewritable = %v, want ErrNewerRecord", err)
	}

	if _, err := rec.Encode(); !errors.Is(err, ErrNewerRecord) {
		t.Errorf("Encode = %v, want ErrNewerRecord", err)
	}
}

func TestCurrentMinorIsRewritable(t *testing.T) {
	if err := sampleRecord().Rewritable(); err != nil {
		t.Errorf("Rewritable = %v", err)
	}
}

func TestUnknownDomainKindIsJudgedByItsEnvelope(t *testing.T) {
	data := mutate(t, func(m map[string]any) {
		m["version"] = "1.2"
		m["domain"] = []any{"cpu:heavy", 3}
		m["footprint"] = []any{map[string]any{"type": "name", "name": "cpu:heavy"}}
	})

	rec, err := Decode(data)
	if err != nil {
		t.Fatalf("a body of an unknown shape should not make the record unusable: %v", err)
	}

	if rec.BodyKnown {
		t.Error("an unknown body should not be marked known")
	}
}

func TestUnknownDomainKindKeepsFootprint(t *testing.T) {
	data := mutate(t, func(m map[string]any) {
		m["version"] = "1.2"
		m["domain"] = map[string]any{"kind": "future", "name": "cpu:heavy"}
		m["footprint"] = []any{map[string]any{"type": "name", "name": "cpu:heavy"}}
		m["clear_when_dead"] = false
	})

	rec, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if rec.BodyKnown || rec.Domain.Kind != "future" {
		t.Errorf("domain = %+v, body known = %v", rec.Domain, rec.BodyKnown)
	}

	if rec.Footprint.Kind != FootprintEntries || rec.Footprint.Entries[0].Name != "cpu:heavy" {
		t.Errorf("footprint = %+v", rec.Footprint)
	}

	if rec.Clearable() {
		t.Error("clear_when_dead false was lost")
	}
}

func TestClearWhenDeadDefaultsToTrue(t *testing.T) {
	rec, err := Decode(mutate(t, func(m map[string]any) { delete(m, "clear_when_dead") }))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !rec.Clearable() {
		t.Error("an absent clear_when_dead should read true")
	}
}

type footprintDecodeTest struct {
	Name string
	JSON string
	Want FootprintKind
}

var footprintDecodeTests = []footprintDecodeTest{
	{Name: "none", JSON: `"none"`, Want: FootprintNone},
	{Name: "all", JSON: `"all"`, Want: FootprintAll},
	{Name: "an unknown word", JSON: `"nothing"`, Want: FootprintAll},
	{Name: "an empty list", JSON: `[]`, Want: FootprintAll},
	{Name: "an object", JSON: `{"type":"path"}`, Want: FootprintAll},
	{Name: "a number", JSON: `7`, Want: FootprintAll},
	{Name: "null", JSON: `null`, Want: FootprintAll},
	{Name: "a list of entries", JSON: `[{"type":"path","path":"/a"}]`, Want: FootprintEntries},
	{Name: "a list of the wrong shape", JSON: `[1,2]`, Want: FootprintAll},
}

func TestFootprintDecodeFailsSafe(t *testing.T) {
	for _, test := range footprintDecodeTests {
		t.Run(test.Name, func(t *testing.T) {
			var f Footprint
			if err := json.Unmarshal([]byte(test.JSON), &f); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if f.Kind != test.Want {
				t.Errorf("kind = %v, want %v", f.Kind, test.Want)
			}
		})
	}
}

func TestAbsentFootprintCountsAsAll(t *testing.T) {
	data := mutate(t, func(m map[string]any) {
		m["domain"] = map[string]any{"kind": "future"}
		delete(m, "footprint")
	})

	rec, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if rec.Footprint.Kind != FootprintAll {
		t.Errorf("kind = %v, want all", rec.Footprint.Kind)
	}
}

type footprintOverlapTest struct {
	Name string
	A    Footprint
	B    Footprint
	Want bool
}

var footprintOverlapTests = []footprintOverlapTest{
	{
		Name: "path entries use the path rule",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a/b"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a/b/c"}}},
		Want: true,
	},
	{
		Name: "path entries with a shared prefix do not overlap",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a/b"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a/bc"}}},
		Want: false,
	},
	{
		Name: "a path entry never overlaps a name entry",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a/b"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "/a/b"}}},
		Want: false,
	},
	{
		Name: "name entries match exactly",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "cpu:heavy"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "cpu:heavy"}}},
		Want: true,
	},
	{
		Name: "different names do not overlap",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "cpu:heavy"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "cpu"}}},
		Want: false,
	},
	{
		Name: "an unknown entry type counts as all",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: "slot"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a"}}},
		Want: true,
	},
	{
		Name: "an unknown entry type overlaps a name entry too",
		A:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: "slot"}}},
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryName, Name: "x"}}},
		Want: true,
	},
	{
		Name: "all overlaps entries",
		A:    AllFootprint(),
		B:    Footprint{Kind: FootprintEntries, Entries: []Entry{{Type: EntryPath, Path: "/a"}}},
		Want: true,
	},
	{
		Name: "none overlaps nothing",
		A:    NoneFootprint(),
		B:    AllFootprint(),
		Want: false,
	},
	{
		Name: "all overlaps all",
		A:    AllFootprint(),
		B:    AllFootprint(),
		Want: true,
	},
}

func TestFootprintOverlaps(t *testing.T) {
	for _, test := range footprintOverlapTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := test.A.Overlaps(test.B, nil); got != test.Want {
				t.Errorf("A.Overlaps(B) = %v, want %v", got, test.Want)
			}

			if got := test.B.Overlaps(test.A, nil); got != test.Want {
				t.Errorf("B.Overlaps(A) = %v, want %v", got, test.Want)
			}
		})
	}
}

func TestUnknownProcessStartPlatformFailsSafe(t *testing.T) {
	data := mutate(t, func(m map[string]any) {
		nested(m, "holder")["start"] = map[string]any{"platform": "plan9", "epoch": 77}
	})

	rec, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if rec.Holder.Start.Known() {
		t.Error("an unknown platform tag should not be known")
	}

	out, err := rec.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if !strings.Contains(string(out), `"epoch":77`) {
		t.Errorf("rewriting dropped the unknown platform's fields: %s", out)
	}
}

type startTest struct {
	Name  string
	Start ProcessStart
	Want  string
}

var startTests = []startTest{
	{
		Name:  "darwin",
		Start: ProcessStart{Platform: PlatformDarwin, Sec: 12, Usec: 34},
		Want:  "12-34",
	},
	{
		Name:  "linux",
		Start: ProcessStart{Platform: PlatformLinux, BootID: "abc123", Ticks: 99, PIDNS: 4026531836},
		Want:  "abc123-99-4026531836",
	},
}

func TestProcessStartString(t *testing.T) {
	for _, test := range startTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := test.Start.String(); got != test.Want {
				t.Errorf("String = %q, want %q", got, test.Want)
			}

			data, err := json.Marshal(test.Start)
			if err != nil {
				t.Fatal(err)
			}

			var back ProcessStart
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}

			if !back.Equal(test.Start) {
				t.Errorf("round trip = %+v, want %+v", back, test.Start)
			}
		})
	}
}

func TestProcessStartEqualIsExact(t *testing.T) {
	a := ProcessStart{Platform: PlatformLinux, BootID: "b", Ticks: 1, PIDNS: 2}

	for _, other := range []ProcessStart{
		{Platform: PlatformLinux, BootID: "c", Ticks: 1, PIDNS: 2},
		{Platform: PlatformLinux, BootID: "b", Ticks: 2, PIDNS: 2},
		{Platform: PlatformLinux, BootID: "b", Ticks: 1, PIDNS: 3},
		{Platform: PlatformDarwin, BootID: "b", Ticks: 1, PIDNS: 2},
	} {
		if a.Equal(other) {
			t.Errorf("%+v compared equal to %+v", a, other)
		}
	}
}

func TestTombstoneRoundTripAndRules(t *testing.T) {
	tomb := &Tombstone{
		Envelope: Envelope{
			Version:       TombstoneVersion,
			ID:            testID,
			Footprint:     NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       Machine{Host: "laptop"},
			Holder:        Holder{Agent: "claude", PID: 1, Start: ProcessStart{Platform: PlatformDarwin, Sec: 5}},
		},
		Lease:     "01JBBBBBBBBBBBBBBBBBBBBBBB",
		BrokenBy:  Holder{PID: 9, User: "ripta"},
		Anonymous: true,
		BrokenAt:  time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC),
	}

	data, err := tomb.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := DecodeTombstone(data)
	if err != nil {
		t.Fatalf("DecodeTombstone: %v", err)
	}

	if got.Lease != tomb.Lease || !got.Anonymous || got.Footprint.Kind != FootprintNone {
		t.Errorf("tombstone = %+v", got)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	m["version"] = "3.0"

	higher, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DecodeTombstone(higher); !errors.Is(err, ErrUnusable) {
		t.Errorf("higher major tombstone err = %v, want ErrUnusable", err)
	}

	m["version"] = "1.5"

	newer, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	got, err = DecodeTombstone(newer)
	if err != nil {
		t.Fatalf("higher minor tombstone: %v", err)
	}

	if _, err := got.Encode(); !errors.Is(err, ErrNewerRecord) {
		t.Errorf("Encode of a higher minor tombstone = %v, want ErrNewerRecord", err)
	}
}

func sampleResource() *Record {
	return &Record{
		Envelope: Envelope{
			Version:       ResourceVersion,
			ID:            testID,
			Footprint:     ResourceFootprint("cpu:heavy"),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       Machine{Host: "laptop", ID: "machine-1"},
			Holder:        Holder{Agent: "claude", PID: 4242, Start: ProcessStart{Platform: PlatformDarwin, Sec: 7}},
		},
		Reach:      ReachMachine,
		Domain:     Domain{Kind: KindResource, Name: "cpu:heavy", Slots: 1},
		ActingFor:  "ripta",
		AcquiredAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		BodyKnown:  true,
	}
}

func TestResourceRecordRoundTrips(t *testing.T) {
	want := sampleResource()

	got, err := Decode(mustEncode(t, want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !got.BodyKnown || got.Domain.Kind != KindResource || got.Domain.Name != "cpu:heavy" || got.Domain.Slots != 1 {
		t.Errorf("record = %+v", got)
	}

	if !bytes.Equal(mustEncode(t, got), mustEncode(t, want)) {
		t.Errorf("round trip changed the record:\n got %s\nwant %s", mustEncode(t, got), mustEncode(t, want))
	}

	if err := got.Rewritable(); err != nil {
		t.Errorf("a resource record of this binary's own minor must be rewritable: %v", err)
	}
}

func TestWritersStampTheLowestMinorTheirFieldsUse(t *testing.T) {
	if got := PathVersion.String(); got != "1.0" {
		t.Errorf("PathVersion = %s, want 1.0", got)
	}

	if got := TombstoneVersion.String(); got != "1.0" {
		t.Errorf("TombstoneVersion = %s, want 1.0", got)
	}

	if got := SessionVersion.String(); got != "1.0" {
		t.Errorf("SessionVersion = %s, want 1.0", got)
	}

	if got := ResourceVersion.String(); got != "1.1" {
		t.Errorf("ResourceVersion = %s, want 1.1", got)
	}

	if ResourceVersion.Newer() {
		t.Error("this binary must be able to rewrite the records it writes")
	}
}

type resourceFootprintTest struct {
	Name      string
	Footprint any
	Want      Reason
}

var resourceFootprintTests = []resourceFootprintTest{
	{Name: "one name entry equal to the name", Footprint: []any{map[string]any{"type": "name", "name": "cpu:heavy"}}},
	{Name: "a different name", Footprint: []any{map[string]any{"type": "name", "name": "cpu:light"}}, Want: ReasonFootprintMismatch},
	{Name: "a path entry", Footprint: []any{map[string]any{"type": "path", "path": "/a"}}, Want: ReasonFootprintMismatch},
	{Name: "an extra entry", Footprint: []any{
		map[string]any{"type": "name", "name": "cpu:heavy"}, map[string]any{"type": "name", "name": "gpu:main"},
	}, Want: ReasonFootprintMismatch},
	{Name: "none", Footprint: "none", Want: ReasonFootprintMismatch},
	{Name: "all", Footprint: "all", Want: ReasonFootprintMismatch},
}

func TestResourceFootprintMustEqualTheName(t *testing.T) {
	for _, test := range resourceFootprintTests {
		t.Run(test.Name, func(t *testing.T) {
			data := mutate(t, func(m map[string]any) {
				m["version"] = "1.1"
				m["domain"] = map[string]any{"kind": "resource", "name": "cpu:heavy"}
				m["footprint"] = test.Footprint
			})

			_, err := Decode(data)
			if test.Want == "" {
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}

				return
			}

			var unusable *UnusableError
			if !errors.As(err, &unusable) || unusable.Reason != test.Want {
				t.Errorf("err = %v, want unusable for %s", err, test.Want)
			}
		})
	}
}
