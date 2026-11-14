package record

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func sampleRecall() *Recall {
	base := sampleRecord()

	return &Recall{
		Envelope: Envelope{
			Version:       RecallVersion,
			ID:            "01JDDDDDDDDDDDDDDDDDDDDDDD",
			Footprint:     NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      LivenessProcess,
			Machine:       base.Machine,
			Holder:        base.Holder,
		},
		Parent:     testID,
		RecalledAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC),
		Deadline:   time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC),
		State:      RecallAnnounced,
	}
}

func TestARecallRoundTripsAtTheRecallMinor(t *testing.T) {
	data, err := sampleRecall().Encode()
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeRecall(data)
	if err != nil {
		t.Fatal(err)
	}

	if got.Version != RecallVersion || got.Parent != testID || !got.Deadline.Equal(sampleRecall().Deadline) {
		t.Errorf("decoded %+v", got)
	}

	if RecallVersion.Minor > CurrentMinor {
		t.Errorf("the current minor %d does not cover the recall minor %d", CurrentMinor, RecallVersion.Minor)
	}
}

func TestARecallThatCannotEndASubLeaseIsUnusable(t *testing.T) {
	tests := []struct {
		Name   string
		Mutate func(m map[string]any)
	}{
		{"no parent", func(m map[string]any) { delete(m, "parent") }},
		{"no deadline", func(m map[string]any) { delete(m, "deadline") }},
		{"a state it does not know", func(m map[string]any) { m["state"] = "frozen" }},
		{"a deadline that is not a time", func(m map[string]any) { m["deadline"] = "soon" }},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			data, err := sampleRecall().Encode()
			if err != nil {
				t.Fatal(err)
			}

			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}

			test.Mutate(m)

			bad, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := DecodeRecall(bad); !errors.Is(err, ErrUnusable) {
				t.Errorf("err = %v, want ErrUnusable", err)
			}
		})
	}
}

func TestARecallOfAHigherMinorReadsAndIsNeverRewritten(t *testing.T) {
	rc := sampleRecall()
	rc.Version = Version{Major: CurrentMajor, Minor: CurrentMinor + 1}

	data, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeRecall(data)
	if err != nil {
		t.Fatalf("a higher minor must read: %v", err)
	}

	if _, err := got.Encode(); !errors.Is(err, ErrNewerRecord) {
		t.Errorf("err = %v, want ErrNewerRecord", err)
	}
}
