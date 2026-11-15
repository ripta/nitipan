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
