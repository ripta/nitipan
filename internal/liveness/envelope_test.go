package liveness

import (
	"fmt"
	"testing"

	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
)

const newerRecord = `{
  "version": "1.5",
  "id": "01JAAAAAAAAAAAAAAAAAAAAAAA",
  "footprint": [{"type": "name", "name": "cpu:heavy"}],
  "clear_when_dead": %s,
  "liveness": "process",
  "machine": {"host": "laptop", "id": "machine-1"},
  "holder": {"agent": "claude", "pid": 77, "start": {"platform": "darwin", "sec": 1000, "usec": 5}},
  "domain": {"kind": "future", "name": "cpu:heavy"}
}`

type newerRecordTest struct {
	Name          string
	ClearWhenDead string
	HolderAlive   bool
	WantClear     bool
}

var newerRecordTests = []newerRecordTest{
	{Name: "dead holder, clearable", ClearWhenDead: "true", HolderAlive: false, WantClear: true},
	{Name: "dead holder, not clearable", ClearWhenDead: "false", HolderAlive: false, WantClear: false},
	{Name: "live holder", ClearWhenDead: "true", HolderAlive: true, WantClear: false},
}

func TestRecordOfAHigherMinorIsClearedOnlyWhenItsHolderIsDeadAndItAllowsIt(t *testing.T) {
	for _, test := range newerRecordTests {
		t.Run(test.Name, func(t *testing.T) {
			data := []byte(fmt.Sprintf(newerRecord, test.ClearWhenDead))

			rec, err := record.Decode(data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			procs := map[int]proctest.Process{}
			if test.HolderAlive {
				procs[77] = proctest.Process{Start: darwinStart}
			}

			checker := &Checker{Procs: newTable(record.PlatformDarwin, procs), Local: localMachine}
			if got := checker.MayClear(rec.Envelope); got != test.WantClear {
				t.Errorf("MayClear = %v, want %v", got, test.WantClear)
			}

			if err := rec.Rewritable(); err == nil {
				t.Error("a record of a higher minor must never be rewritten")
			}

			own := record.Footprint{Kind: record.FootprintEntries, Entries: []record.Entry{{Type: record.EntryPath, Path: "/repo"}}}

			if rec.Footprint.Overlaps(own, nil) {
				t.Error("a name footprint must not block a path claim")
			}

			name := record.Footprint{Kind: record.FootprintEntries, Entries: []record.Entry{record.NameEntry("cpu:heavy")}}
			if !rec.Footprint.Overlaps(name, nil) {
				t.Error("the footprint must still block a claim on the same name")
			}
		})
	}
}
