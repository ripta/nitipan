package broker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

func TestMigrateV1ToV2CreatesOwnerAndBindsPinnedLabels(t *testing.T) {
	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/501": {KeyHash: "hash1", Label: "label1"},
			"m2/502": {KeyHash: "hash2", Label: "label2"},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 501, Host: "host1", User: "user1", Derivation: 1, LastSeen: time.Now()},
			"label2": {MachineID: "m2", UID: 502, Host: "host2", User: "user2", Derivation: 1, LastSeen: time.Now()},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: "hash1"},
			"label2": {},
		},
	}

	v1Bytes, err := json.Marshal(v1Data)
	if err != nil {
		t.Fatal(err)
	}

	v2Bytes, err := MigrateV1ToV2(v1Bytes)
	if err != nil {
		t.Fatal(err)
	}

	var v2Data Data
	if err := json.Unmarshal(v2Bytes, &v2Data); err != nil {
		t.Fatal(err)
	}

	if v2Data.Version.Major != 2 || v2Data.Version.Minor != 0 {
		t.Errorf("version = %v, want 2.0", v2Data.Version)
	}

	if v2Data.OwnerID == "" {
		t.Error("owner ID is empty")
	}

	if len(v2Data.Users) != 1 {
		t.Errorf("users count = %d, want 1", len(v2Data.Users))
	}

	owner, ok := v2Data.Users[v2Data.OwnerID]
	if !ok {
		t.Fatal("owner user not found")
	}

	if owner.UserLabel == "" {
		t.Error("owner user label is empty")
	}

	if owner.LabelDerivation != UserLabelDerivation {
		t.Errorf("owner label derivation = %d, want %d", owner.LabelDerivation, UserLabelDerivation)
	}

	if len(v2Data.Bindings) != 1 {
		t.Errorf("bindings count = %d, want 1 (only pinned label)", len(v2Data.Bindings))
	}

	binding, ok := v2Data.Bindings["label1"]
	if !ok {
		t.Error("label1 binding not found")
	}

	if binding.UserID != v2Data.OwnerID {
		t.Errorf("binding user ID = %q, want %q", binding.UserID, v2Data.OwnerID)
	}

	if binding.LastBoundUser != v2Data.OwnerID {
		t.Errorf("binding last bound user = %q, want %q", binding.LastBoundUser, v2Data.OwnerID)
	}

	if binding.DerivationVersion != UserLabelDerivation {
		t.Errorf("binding derivation version = %d, want %d", binding.DerivationVersion, UserLabelDerivation)
	}

	if _, ok := v2Data.Bindings["label2"]; ok {
		t.Error("label2 should not be bound (not pinned)")
	}
}

// v1Grant is a lease as a 1.x broker stored it: the wire lease, which names its holder's label in `node`,
// plus the holder node's machine and UID. The 1.x holder record carries no label of its own.
func v1Grant(id, label, machine string, uid int, actingFor string) json.RawMessage {
	g := grant{
		Lease: wire.Lease{
			ID: id, Reach: wire.ReachUser, State: wire.StateLive, Revision: 1, Token: 1,
			Entries: []repoid.Name{repoid.New([]string{"root-" + id}, "/src/"+id, false, false)},
			Anchor:  "anchor-" + id, Holder: wire.Holder{Key: "key-" + id, Agent: "claude", User: "ripta"},
			ActingFor: actingFor, Node: label, Liveness: wire.LiveAlive,
		},
		Machine: machine, UID: uid, Derivation: 1,
	}

	raw, err := json.Marshal(g)
	if err != nil {
		panic(err)
	}

	return raw
}

type migrateActingForTest struct {
	Name      string
	Lease     string
	Owner     bool
	ActingFor string
}

// The pinned label is "pinned-label" on m1/501, and "loose-label" on m2/502 was never pinned.
var migrateActingForTests = []migrateActingForTest{
	{Name: "a pinned label's lease for an OS user moves to the owner", Lease: "pinned-os-user", Owner: true},
	{Name: "a pinned label's lease with no acting_for moves to the owner", Lease: "pinned-empty", Owner: true},
	{Name: "an unpinned label's lease keeps its acting_for", Lease: "loose-os-user", ActingFor: "ripta"},
}

// TestMigrateV1ToV2RewritesActingForOnPinnedLabelLeases upgrades a store whose leases have the shape a 1.x
// broker wrote, and reads them back through the broker's own lease decoder. It catches a migration that
// finds a label's leases by a field 1.x leases never carried, which leaves every legacy acting_for in place.
func TestMigrateV1ToV2RewritesActingForOnPinnedLabelLeases(t *testing.T) {
	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/501": {KeyHash: "hash1", Label: "pinned-label"},
			"m2/502": {KeyHash: "hash2", Label: "loose-label"},
		},
		Labels: map[string]LabelRecord{
			"pinned-label": {MachineID: "m1", UID: 501},
			"loose-label":  {MachineID: "m2", UID: 502},
		},
		Pins: map[string]PinRecord{
			"pinned-label": {PinnedHash: "hash1"},
			"loose-label":  {},
		},
		Leases: map[string]json.RawMessage{
			"pinned-os-user": v1Grant("pinned-os-user", "pinned-label", "m1", 501, "ripta"),
			"pinned-empty":   v1Grant("pinned-empty", "pinned-label", "m1", 501, ""),
			"loose-os-user":  v1Grant("loose-os-user", "loose-label", "m2", 502, "ripta"),
		},
	}

	v1Bytes, err := json.Marshal(v1Data)
	if err != nil {
		t.Fatal(err)
	}

	v2Bytes, err := MigrateV1ToV2(v1Bytes)
	if err != nil {
		t.Fatal(err)
	}

	var v2Data Data
	if err := json.Unmarshal(v2Bytes, &v2Data); err != nil {
		t.Fatal(err)
	}

	s, err := loadGState(&v2Data)
	if err != nil {
		t.Fatal(err)
	}

	ownerLabel := v2Data.Users[v2Data.OwnerID].UserLabel

	for _, test := range migrateActingForTests {
		t.Run(test.Name, func(t *testing.T) {
			g := s.grants[test.Lease]
			if g == nil {
				t.Fatalf("lease %s is missing after the migration", test.Lease)
			}

			want := test.ActingFor
			if test.Owner {
				want = ownerLabel
			}

			if g.ActingFor != want {
				t.Errorf("acting_for = %q, want %q", g.ActingFor, want)
			}

			var before grant
			if err := json.Unmarshal(v1Data.Leases[test.Lease], &before); err != nil {
				t.Fatal(err)
			}

			before.ActingFor = want

			if !reflect.DeepEqual(*g, before) {
				t.Errorf("the migration changed more than acting_for:\n got %+v\nwant %+v", *g, before)
			}
		})
	}
}

func TestDeriveUserLabelProducesFourWords(t *testing.T) {
	label := DeriveUserLabel("test-user-id")

	parts := len(label)
	if parts == 0 {
		t.Error("user label is empty")
	}

	wordCount := 0
	for _, ch := range label {
		if ch == '-' {
			wordCount++
		}
	}

	if wordCount != 3 {
		t.Errorf("user label has %d dashes, want 3 (four words)", wordCount)
	}
}

// TestMigrateV1ToV2PreservesAllFields ensures the migration preserves every v1 field.
// This test seeds every v1 field with a non-zero value, migrates, and asserts each survives.
func TestMigrateV1ToV2PreservesAllFields(t *testing.T) {
	now := time.Now()

	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/501": {KeyHash: "hash1", Label: "label1", MailCap: 3, HTTPS: true},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 501, Host: "host1", User: "user1", Derivation: 1, LastSeen: now},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: "hash1", Pending: map[string]time.Time{"pending1": now}},
		},
		Sessions: map[string]map[string]SessionRecord{
			"label1": {
				"session1": {Agent: "agent1", NameVersion: 1, FirstSeen: now},
			},
		},
		Mail: map[string]MailRecord{
			"mail1": {
				ID: "mail1", Kind: "mail", FromLabel: "label1", ToLabel: "label1",
				ToSession: "s1", ToAddress: "addr1", Stage: "queued", StageAt: now, EnqueuedAt: now,
			},
		},
		Leases: map[string]json.RawMessage{
			"lease1": json.RawMessage(`{"holder":{"label":"label1"},"acting_for":"user1"}`),
		},
		Outcomes: map[string]json.RawMessage{
			"outcome1": json.RawMessage(`{"at":"2024-01-01T00:00:00Z","result":{}}`),
		},
		Counter: 42,
	}

	v1Bytes, err := json.Marshal(v1Data)
	if err != nil {
		t.Fatal(err)
	}

	v2Bytes, err := MigrateV1ToV2(v1Bytes)
	if err != nil {
		t.Fatal(err)
	}

	var v2Data Data
	if err := json.Unmarshal(v2Bytes, &v2Data); err != nil {
		t.Fatal(err)
	}

	if len(v2Data.Nodes) != 1 || v2Data.Nodes["m1/501"].KeyHash != "hash1" {
		t.Errorf("Nodes = %v, want hash1", v2Data.Nodes)
	}

	if len(v2Data.Labels) != 1 || v2Data.Labels["label1"].Host != "host1" {
		t.Errorf("Labels = %v, want host1", v2Data.Labels)
	}

	if len(v2Data.Pins) != 1 || v2Data.Pins["label1"].PinnedHash != "hash1" {
		t.Errorf("Pins = %v, want hash1", v2Data.Pins)
	}

	if len(v2Data.Sessions) != 1 || v2Data.Sessions["label1"]["session1"].Agent != "agent1" {
		t.Errorf("Sessions = %v, want agent1", v2Data.Sessions)
	}

	if len(v2Data.Mail) != 1 || v2Data.Mail["mail1"].ID != "mail1" {
		t.Errorf("Mail = %v, want mail1", v2Data.Mail)
	}

	if len(v2Data.Leases) != 1 {
		t.Errorf("Leases count = %d, want 1", len(v2Data.Leases))
	}

	if len(v2Data.Outcomes) != 1 {
		t.Errorf("Outcomes count = %d, want 1", len(v2Data.Outcomes))
	}

	if v2Data.Counter != 42 {
		t.Errorf("Counter = %d, want 42", v2Data.Counter)
	}
}
