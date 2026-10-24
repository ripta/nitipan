package listing

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

var (
	machine = record.Machine{Host: "laptop", ID: "m1"}
	started = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 10}
)

func checker(alive bool) *liveness.Checker {
	table := proctest.New(record.PlatformDarwin, 1)
	if alive {
		table.Add(77, proctest.Process{Start: started})
	}

	return &liveness.Checker{Procs: table, Local: machine}
}

const unknownKindRecord = `{
  "version": "1.3",
  "id": "01JAAAAAAAAAAAAAAAAAAAAAAA",
  "footprint": [{"type": "name", "name": "cpu:heavy"}],
  "clear_when_dead": false,
  "liveness": "process",
  "machine": {"host": "laptop", "id": "m1"},
  "holder": {"agent": "claude", "pid": 77, "start": {"platform": "darwin", "sec": 10}},
  "domain": {"kind": "future", "name": "cpu:heavy", "slots": 3},
  "slot_count": 3
}`

func decodeOutput(t *testing.T, out Output) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	if err := Encode(&buf, out); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not json: %v\n%s", err, buf.String())
	}

	return doc
}

func firstLease(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()

	leases, ok := doc["leases"].([]any)
	if !ok || len(leases) == 0 {
		t.Fatalf("leases = %v", doc["leases"])
	}

	lease, ok := leases[0].(map[string]any)
	if !ok {
		t.Fatalf("lease = %v", leases[0])
	}

	return lease
}

func TestListJSONEmitsALeaseOfAnUnknownKindWithItsLivenessAndFootprint(t *testing.T) {
	for _, alive := range []bool{true, false} {
		rec, err := record.Decode([]byte(unknownKindRecord))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}

		res := checker(alive).Check(rec.Envelope)
		doc := decodeOutput(t, Output{Leases: []Lease{FromRecord(rec, res)}})

		if doc["schema_version"] != SchemaVersion {
			t.Errorf("schema_version = %v", doc["schema_version"])
		}

		lease := firstLease(t, doc)

		if lease["kind"] != "future" || lease["usable"] != true || lease["version"] != "1.3" {
			t.Errorf("lease = %v", lease)
		}

		wantStatus := "alive"
		if !alive {
			wantStatus = "dead"
		}

		status, _ := lease["liveness"].(map[string]any)
		if status["status"] != wantStatus {
			t.Errorf("liveness = %v, want %s", status, wantStatus)
		}

		footprint, _ := lease["footprint"].([]any)
		if len(footprint) != 1 {
			t.Fatalf("footprint = %v", lease["footprint"])
		}

		entry, _ := footprint[0].(map[string]any)
		if entry["type"] != "name" || entry["name"] != "cpu:heavy" {
			t.Errorf("footprint entry = %v", entry)
		}

		if lease["clear_when_dead"] != false {
			t.Errorf("clear_when_dead = %v, want false", lease["clear_when_dead"])
		}

		if _, ok := lease["domain"]; ok {
			t.Error("an unknown body should stay opaque and out of the output")
		}

		if _, ok := lease["holder"]; !ok {
			t.Error("the envelope's holder is missing")
		}
	}
}

func TestListJSONReportsUnknownLivenessForAKindItCannotJudge(t *testing.T) {
	rec, err := record.Decode([]byte(unknownKindRecord))
	if err != nil {
		t.Fatal(err)
	}

	rec.Liveness = "future-kind"

	res := checker(false).Check(rec.Envelope)
	lease := firstLease(t, decodeOutput(t, Output{Leases: []Lease{FromRecord(rec, res)}}))

	status, _ := lease["liveness"].(map[string]any)
	if status["status"] != "unknown" {
		t.Errorf("liveness = %v, want unknown", status)
	}
}

func TestListJSONShowsAKnownPathRecordWithItsBody(t *testing.T) {
	rec := &record.Record{
		Envelope: record.Envelope{
			Version:       record.Current,
			ID:            "01JAAAAAAAAAAAAAAAAAAAAAAA",
			Footprint:     record.PathFootprint(nil),
			ClearWhenDead: true,
			Liveness:      record.LivenessProcess,
			Machine:       machine,
			Holder:        record.Holder{Agent: "claude", PID: 77, Start: started},
		},
		Reach:       record.ReachMachine,
		Domain:      record.Domain{Kind: record.KindPath, Checkout: "/repo"},
		Description: "owns the repo",
		BodyKnown:   true,
	}

	lease := firstLease(t, decodeOutput(t, Output{Leases: []Lease{FromRecord(rec, liveness.Result{Status: liveness.Alive})}}))

	domain, _ := lease["domain"].(map[string]any)
	if domain["checkout"] != "/repo" || lease["description"] != "owns the repo" || lease["reach"] != "machine" {
		t.Errorf("lease = %v", lease)
	}
}

func TestListJSONShowsAnUnusableRecordAsUnknown(t *testing.T) {
	_, err := record.Decode([]byte(`{"version":"2.0","id":"01JAAAAAAAAAAAAAAAAAAAAAAA","new":"shape"}`))

	var unusable *record.UnusableError
	if !errors.As(err, &unusable) {
		t.Fatalf("err = %v", err)
	}

	lease := firstLease(t, decodeOutput(t, Output{Leases: []Lease{FromUnusable("01JAAAAAAAAAAAAAAAAAAAAAAA.json", unusable)}}))

	if lease["usable"] != false || lease["file"] != "01JAAAAAAAAAAAAAAAAAAAAAAA.json" {
		t.Errorf("lease = %v", lease)
	}

	status, _ := lease["liveness"].(map[string]any)
	if status["status"] != "unknown" {
		t.Errorf("liveness = %v, want unknown", status)
	}

	raw, _ := lease["raw_fields"].(map[string]any)
	if raw["new"] != "shape" {
		t.Errorf("raw_fields = %v, want the fields that could be read", raw)
	}
}

func TestListJSONWithNoLeasesIsAnEmptyList(t *testing.T) {
	doc := decodeOutput(t, Output{})

	leases, ok := doc["leases"].([]any)
	if !ok || len(leases) != 0 {
		t.Errorf("leases = %v, want an empty list", doc["leases"])
	}
}

func resourceRecord() *record.Record {
	return &record.Record{
		Envelope: record.Envelope{
			Version:       record.ResourceVersion,
			ID:            "01JAAAAAAAAAAAAAAAAAAAAAAA",
			Footprint:     record.ResourceFootprint("cpu:heavy"),
			ClearWhenDead: true,
			Liveness:      record.LivenessProcess,
			Machine:       machine,
			Holder:        record.Holder{Agent: "claude", PID: 77, Start: started},
		},
		Reach:     record.ReachMachine,
		Domain:    record.Domain{Kind: record.KindResource, Name: "cpu:heavy", Slots: 1},
		BodyKnown: true,
	}
}

func TestListJSONShowsAResourceRecordWithItsName(t *testing.T) {
	lease := firstLease(t, decodeOutput(t, Output{Leases: []Lease{
		FromRecord(resourceRecord(), liveness.Result{Status: liveness.Alive}),
	}}))

	domain, _ := lease["domain"].(map[string]any)
	if lease["kind"] != "resource" || lease["version"] != "1.1" || domain["name"] != "cpu:heavy" {
		t.Errorf("lease = %v", lease)
	}

	if _, ok := lease["remote"]; ok {
		t.Error("a local lease carries the remote flag")
	}
}

func TestListJSONMarksARemoteResourceLeaseAndGivesItNoCount(t *testing.T) {
	l := FromRecord(resourceRecord(), liveness.Result{Status: liveness.Unknown})
	l.Remote = true

	lease := firstLease(t, decodeOutput(t, Output{Leases: []Lease{l}}))
	if lease["remote"] != true {
		t.Errorf("lease = %v, want the remote flag", lease)
	}

	if _, ok := lease["effective_slots"]; ok {
		t.Errorf("a remote lease carries an effective count: %v", lease)
	}
}

func TestListTextMarksARemoteResourceLease(t *testing.T) {
	rec := resourceRecord()
	rec.Machine = record.Machine{Host: "workstation", ID: "m2"}

	l := FromRecord(rec, liveness.Result{Status: liveness.Unknown})
	l.Remote = true

	var buf bytes.Buffer
	if err := WriteText(&buf, Output{Leases: []Lease{l}}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "domain: resource cpu:heavy (remote, on machine workstation)") {
		t.Errorf("text = %q", buf.String())
	}
}

func TestListJSONAlwaysWritesRequestsAndNoticesAsArrays(t *testing.T) {
	doc := decodeOutput(t, Output{})

	for _, field := range []string{"requests", "notices", "tombstones"} {
		arr, ok := doc[field].([]any)
		if !ok || len(arr) != 0 {
			t.Errorf("%s = %v, want an empty array", field, doc[field])
		}
	}

	if doc["schema_version"] != "1.2" {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}
}

func TestListJSONWritesTheSubLeaseFieldsOnlyWhereTheyApply(t *testing.T) {
	owner := false

	parent := Lease{ID: "P", Usable: true, Owner: &owner, GrantorOf: []string{"C"},
		Delegated: []Delegation{{ID: "C", Keys: []string{"/repo/src"}}}}
	child := Lease{ID: "C", Usable: true, Parent: "P"}
	plain := Lease{ID: "X", Usable: true}

	doc := decodeOutput(t, Output{Leases: []Lease{parent, child, plain}})
	leases, _ := doc["leases"].([]any)

	p, _ := leases[0].(map[string]any)
	grantor, _ := p["grantor_of"].([]any)
	delegated, _ := p["delegated"].([]any)

	if p["owner"] != false || len(grantor) != 1 || len(delegated) != 1 {
		t.Errorf("parent = %v", p)
	}

	c, _ := leases[1].(map[string]any)
	if c["parent"] != "P" {
		t.Errorf("child = %v", c)
	}

	x, _ := leases[2].(map[string]any)
	for _, field := range []string{"parent", "delegated", "owner", "grantor_of"} {
		if _, ok := x[field]; ok {
			t.Errorf("a plain lease carries %s: %v", field, x)
		}
	}
}

func TestListJSONShowsAPendingRequestWithItsRequesterHandle(t *testing.T) {
	doc := decodeOutput(t, Output{Requests: []Request{{
		ID: "R", Parent: "P", Requester: record.Holder{Agent: "claude", Handle: "quiet-otter", PID: 77},
		Liveness: Liveness{Status: "alive"}, Keys: []string{"/repo/lib"},
	}}})

	reqs, _ := doc["requests"].([]any)
	req, _ := reqs[0].(map[string]any)
	requester, _ := req["requester"].(map[string]any)

	reqKeys, _ := req["keys"].([]any)

	if req["parent"] != "P" || requester["handle"] != "quiet-otter" || len(reqKeys) != 1 {
		t.Errorf("request = %v", req)
	}
}

func subLeaseOutput() Output {
	owner := true
	notOwner := false

	return Output{
		Leases: []Lease{
			{ID: "P", Usable: true, Liveness: Liveness{Status: "alive"}, Owner: &notOwner, GrantorOf: []string{"C"},
				Delegated: []Delegation{{ID: "C", Keys: []string{"/repo/src"}}}},
			{ID: "C", Usable: true, Liveness: Liveness{Status: "alive"}, Parent: "P", Owner: &owner},
			{ID: "Z", Usable: true, Liveness: Liveness{Status: "alive"}},
		},
		Requests: []Request{
			{ID: "R", Parent: "P", Requester: record.Holder{Handle: "quiet-otter"}, Liveness: Liveness{Status: "alive"},
				Keys: []string{"/repo/lib"}},
			{ID: "Q", Parent: "GONE", Requester: record.Holder{Handle: "loud-owl"}, Liveness: Liveness{Status: "alive"},
				Keys: []string{"/other"}},
		},
	}
}

func TestListTextIndentsSubLeasesAndRequestsUnderTheirParent(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteText(&buf, subLeaseOutput()); err != nil {
		t.Fatal(err)
	}

	want := `P  alive
  owner of the match: no
  grantor of: C
  request R for lease P, alive
    wants: /repo/lib
    requester handle: quiet-otter, agent name: (none)
  C  alive
    owner of the match: yes

Z  alive

  request Q for lease GONE, alive
    wants: /other
    requester handle: loud-owl, agent name: (none)
`

	if buf.String() != want {
		t.Errorf("text =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestListTextPrintsASubLeaseWhoseParentIsNotListedAtTheTopLevel(t *testing.T) {
	out := subLeaseOutput()
	out.Leases = out.Leases[1:2]
	out.Leases[0].Owner = nil
	out.Requests = nil

	var buf bytes.Buffer
	if err := WriteText(&buf, out); err != nil {
		t.Fatal(err)
	}

	want := "C  alive\n  parent link: P\n"
	if buf.String() != want {
		t.Errorf("text = %q, want %q", buf.String(), want)
	}
}

func TestListTextNeverNestsASubLeaseUnderASubLease(t *testing.T) {
	out := Output{Leases: []Lease{
		{ID: "A", Usable: true, Liveness: Liveness{Status: "alive"}, Parent: "B", Delegated: []Delegation{{ID: "B"}}},
		{ID: "B", Usable: true, Liveness: Liveness{Status: "alive"}, Parent: "A", Delegated: []Delegation{{ID: "A"}}},
	}}

	var buf bytes.Buffer
	if err := WriteText(&buf, out); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(buf.String(), "\n  B ") || strings.HasPrefix(buf.String(), "  ") {
		t.Errorf("text = %q", buf.String())
	}
}
