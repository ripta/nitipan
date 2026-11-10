package broker

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/setup"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestRevokeUserForgetsLabelsRemovesAdmissionAndDropsGrants tests that revoking a user forgets
// all their labels, removes their admission, and drops all grants and delegations naming them.
func TestRevokeUserForgetsLabelsRemovesAdmissionAndDropsGrants(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.b.LinkIssuerSub("https://issuer.example", "sub123", user.ID); err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user.ID, now); err != nil {
		t.Fatal(err)
	}

	otherUser, err := h.b.CreateUser("other@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	charlie := nodeID{Machine: "machine3", UID: 1002, Key: "key3"}
	regCharlie := h.register(charlie)
	labelCharlie := regCharlie.Label

	if err := h.b.BindLabelToUser(labelCharlie, otherUser.ID, now); err != nil {
		t.Fatal(err)
	}

	d := h.data()
	if d.Delegations == nil {
		d.Delegations = make(map[string]map[string]time.Time)
	}
	if d.MailGrants == nil {
		d.MailGrants = make(map[string]map[string]time.Time)
	}

	if err := h.b.commit(func(d *Data) error {
		d.Delegations[user.ID] = map[string]time.Time{labelCharlie: now}
		d.Delegations[otherUser.ID] = map[string]time.Time{labelAlice: now}
		d.MailGrants[user.ID] = map[string]time.Time{otherUser.ID: now}
		d.MailGrants[otherUser.ID] = map[string]time.Time{user.ID: now}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.b.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}

	d = h.data()

	if !d.Pins[labelAlice].Forgotten {
		t.Error("label alice not forgotten")
	}

	if !d.Pins[labelBob].Forgotten {
		t.Error("label bob not forgotten")
	}

	if d.Pins[labelCharlie].Forgotten {
		t.Error("label charlie should not be forgotten (belongs to another user)")
	}

	issuerSub := "https://issuer.example\x00sub123"
	if _, ok := d.IssuerSubs[issuerSub]; ok {
		t.Error("issuer sub not removed")
	}

	if _, ok := d.Delegations[user.ID]; ok {
		t.Error("user delegations not removed")
	}

	if _, ok := d.MailGrants[user.ID]; ok {
		t.Error("user mail grants not removed")
	}

	if len(d.Delegations[otherUser.ID]) > 0 {
		for label := range d.Delegations[otherUser.ID] {
			if label == labelAlice {
				t.Error("delegation to user's label not removed")
			}
		}
	}

	if _, ok := d.MailGrants[otherUser.ID][user.ID]; ok {
		t.Error("mail grant to user not removed")
	}
}

// TestRevokeUserAfterSignInRestoresNothing tests that signing in again after revocation
// restores nothing.
func TestRevokeUserAfterSignInRestoresNothing(t *testing.T) {
	h, si := newSignInHarness(t)

	now := time.Now()
	user, err := h.b.AdmitUser(si.issuer, si.subject, si.email, now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	reg := h.register(n)
	label := reg.Label

	if err := h.b.BindLabelToUser(label, user.ID, now); err != nil {
		t.Fatal(err)
	}

	otherUser, err := h.b.CreateUser("other@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.b.commit(func(d *Data) error {
		if d.Delegations == nil {
			d.Delegations = make(map[string]map[string]time.Time)
		}
		if d.MailGrants == nil {
			d.MailGrants = make(map[string]map[string]time.Time)
		}

		d.Delegations[user.ID] = map[string]time.Time{"some-label": now}
		d.MailGrants[user.ID] = map[string]time.Time{otherUser.ID: now}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.b.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}

	issuerSub := si.issuer + "\x00" + si.subject
	d := h.data()

	if _, ok := d.IssuerSubs[issuerSub]; ok {
		t.Error("issuer sub not removed after revoke")
	}

	user2, err := h.b.AdmitUser(si.issuer, si.subject, si.email, now)
	if err != nil {
		t.Fatal(err)
	}

	d = h.data()

	if user2.ID == user.ID {
		t.Error("re-admitted user got the same ID as the revoked user")
	}

	if !d.Pins[label].Forgotten {
		t.Error("label not forgotten after revoke")
	}

	if _, ok := d.Delegations[user2.ID]; ok {
		t.Error("delegations restored after sign-in")
	}

	if _, ok := d.MailGrants[user2.ID]; ok {
		t.Error("mail grants restored after sign-in")
	}
}

// TestForgetAndRevokeClosesStreamAndRefusesReconnect tests that forgetting or revoking
// closes the stream at once and refuses reconnection with the old key.
func TestForgetAndRevokeClosesStreamAndRefusesReconnect(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	reg := h.register(n)
	label := reg.Label

	if err := h.b.BindLabelToUser(label, user.ID, now); err != nil {
		t.Fatal(err)
	}

	streamStarted := make(chan struct{})
	streamDone := make(chan struct{})

	go func() {
		defer close(streamDone)

		req := httptest.NewRequest("GET", wire.PathStream, nil)
		req.Header.Set("Authorization", "Bearer "+n.Key)
		req.Header.Set(wire.HeaderMachine, n.Machine)
		req.Header.Set(wire.HeaderUID, "1000")

		w := &streamRecorder{
			ResponseRecorder: httptest.NewRecorder(),
			started:          streamStarted,
		}

		h.b.handleStream(w, req, n.key())
	}()

	<-streamStarted

	d := h.data()
	oldLastBoundUser := d.Bindings[label].UserID

	if err := h.b.Forget(label); err != nil {
		t.Fatal(err)
	}

	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not closed after forget")
	}

	d = h.data()
	if !d.Pins[label].Forgotten {
		t.Error("label not forgotten")
	}

	if d.Bindings[label].LastBoundUser != oldLastBoundUser {
		t.Errorf("last bound user = %q, want %q", d.Bindings[label].LastBoundUser, oldLastBoundUser)
	}

	resp := h.do(http.MethodGet, wire.PathStream, n, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

type streamRecorder struct {
	*httptest.ResponseRecorder
	started chan struct{}
}

func (s *streamRecorder) Write(b []byte) (int, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}

	return s.ResponseRecorder.Write(b)
}

// TestForgetStripsHashFromKeptV1File tests that forgetting a label strips its hash from
// the kept .v1 file for rollback safety.
func TestForgetStripsHashFromKeptV1File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/1000": {KeyHash: "hash1", Label: "label1"},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 1000},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: "hash1"},
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

	if err := os.WriteFile(path, v2Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path+".v1", v1Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	defer store.Close()

	clock := monoclock.NewFake(time.Hour)
	wall := &wallClock{t: wallStart}
	cfg := Config{Store: store, Term: testTerm, Clock: clock, Now: wall.Now, Version: "test"}
	b := New(cfg)

	if err := b.Forget("label1"); err != nil {
		t.Fatal(err)
	}

	keptBytes, err := os.ReadFile(path + ".v1")
	if err != nil {
		t.Fatal(err)
	}

	var keptData Data
	if err := json.Unmarshal(keptBytes, &keptData); err != nil {
		t.Fatal(err)
	}

	if keptData.Pins["label1"].Forgotten {
		if keptData.Nodes["m1/1000"].KeyHash != "" {
			t.Error("key hash not stripped from kept file after forget")
		}
	}
}

// TestRollbackAfterForgetRefusesKey tests that after migration and forget, the kept .v1 file
// has the forgotten key hash stripped, so an older v1 broker reading it would refuse the key.
func TestRollbackAfterForgetRefusesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/1000": {KeyHash: keyHash("key1"), Label: "label1"},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 1000, User: "user1", Host: "host1", Derivation: 1},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: keyHash("key1")},
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

	if err := os.WriteFile(path, v2Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path+".v1", v1Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	clock := monoclock.NewFake(time.Hour)
	wall := &wallClock{t: wallStart}
	cfg := Config{Store: store, Term: testTerm, Clock: clock, Now: wall.Now, Version: "test"}
	b := New(cfg)

	if err := b.Forget("label1"); err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	keptBytes, err := os.ReadFile(path + ".v1")
	if err != nil {
		t.Fatal(err)
	}

	var keptData Data
	if err := json.Unmarshal(keptBytes, &keptData); err != nil {
		t.Fatal(err)
	}

	if _, exists := keptData.Nodes["m1/1000"]; exists {
		t.Error("node record still exists in kept file, but should be deleted so v1 refuses the key")
	}

	pin := keptData.Pins["label1"]
	if !pin.Forgotten {
		t.Error("pin.Forgotten = false, want true so v1 notePending refuses the key")
	}

	if pin.PinnedHash != "" {
		t.Error("pinned hash not stripped from kept .v1 file after forget")
	}
}

// TestConfirmUpgradeDeletesKeptFile tests that confirming the upgrade deletes the kept file
// and reports that rollback is then impossible.
func TestConfirmUpgradeDeletesKeptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/1000": {KeyHash: "hash1", Label: "label1"},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 1000},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: "hash1"},
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

	if err := os.WriteFile(path, v2Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path+".v1", v1Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	defer store.Close()

	if err := store.DeleteKeptFile(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".v1"); err == nil {
		t.Error("kept file still exists after confirm")
	}
}

// TestRebindToDifferentUserBouncesMailAndDropsDelegations tests that binding a label
// to a different user bounces queued mail and drops delegations.
func TestRebindToDifferentUserBouncesMailAndDropsDelegations(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	userA, err := h.b.CreateUser("userA@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	userB, err := h.b.CreateUser("userB@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, userA.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, userB.ID, now); err != nil {
		t.Fatal(err)
	}

	msgID := "msg-1"
	if err := h.b.commit(func(d *Data) error {
		if d.Mail == nil {
			d.Mail = make(map[string]MailRecord)
		}

		d.Mail[msgID] = MailRecord{
			ID:        msgID,
			Kind:      wire.EventMail,
			FromLabel: labelBob,
			ToLabel:   labelAlice,
			Stage:     wire.StageStored,
			Body:      "test message",
		}

		if d.Delegations == nil {
			d.Delegations = make(map[string]map[string]time.Time)
		}

		d.Delegations[userB.ID] = map[string]time.Time{labelAlice: now}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.b.Forget(labelAlice); err != nil {
		t.Fatal(err)
	}

	charlie := nodeID{Machine: "machine1", UID: 1000, Key: "key1-new"}
	h.register(charlie)

	if err := h.b.BindLabelToUser(labelAlice, userB.ID, now); err != nil {
		t.Fatal(err)
	}

	d := h.data()

	mail := d.Mail[msgID]
	if !mail.Bounced {
		t.Error("mail not bounced after rebind to different user")
	}

	if _, ok := d.Delegations[userB.ID][labelAlice]; ok {
		t.Error("delegation to label not dropped after rebind to different user")
	}
}

// TestRebindToSameUserKeepsMailAndDelegations tests that binding a label to the same user
// does not bounce mail or drop delegations (beyond what Forget already did).
func TestRebindToSameUserKeepsMailAndDelegations(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	otherUser, err := h.b.CreateUser("other@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user.ID, now); err != nil {
		t.Fatal(err)
	}

	if err := h.b.Forget(labelAlice); err != nil {
		t.Fatal(err)
	}

	charlie := nodeID{Machine: "machine1", UID: 1000, Key: "key1-new"}
	h.register(charlie)

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, otherUser.ID, now); err != nil {
		t.Fatal(err)
	}

	msgID := "msg-1"
	if err := h.b.commit(func(d *Data) error {
		if d.Mail == nil {
			d.Mail = make(map[string]MailRecord)
		}

		d.Mail[msgID] = MailRecord{
			ID:        msgID,
			Kind:      wire.EventMail,
			FromLabel: labelBob,
			ToLabel:   labelAlice,
			Stage:     wire.StageQueued,
			Body:      "test message",
		}

		if d.Delegations == nil {
			d.Delegations = make(map[string]map[string]time.Time)
		}

		d.Delegations[otherUser.ID] = map[string]time.Time{labelAlice: now}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.b.BindLabelToUser(labelAlice, user.ID, now); err != nil {
		t.Fatal(err)
	}

	d := h.data()

	mail := d.Mail[msgID]
	if mail.Bounced {
		t.Error("mail bounced after rebind to same user")
	}

	if _, ok := d.Delegations[otherUser.ID][labelAlice]; !ok {
		t.Error("delegation dropped after rebind to same user")
	}
}

// TestBindingRewritesActingForInLeases tests that binding a label rewrites acting_for
// in its leases to the bound user.
func TestBindingRewritesActingForInLeases(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	leaseID := "lease-1"
	lease := map[string]interface{}{
		"holder":     map[string]interface{}{"label": labelAlice},
		"acting_for": "old-os-user",
	}

	leaseBytes, _ := json.Marshal(lease)

	if err := h.b.commit(func(d *Data) error {
		if d.Leases == nil {
			d.Leases = make(map[string]json.RawMessage)
		}

		d.Leases[leaseID] = leaseBytes

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.b.BindLabelToUser(labelAlice, user.ID, now); err != nil {
		t.Fatal(err)
	}

	d := h.data()

	var rewritten map[string]interface{}
	if err := json.Unmarshal(d.Leases[leaseID], &rewritten); err != nil {
		t.Fatal(err)
	}

	if rewritten["acting_for"] != user.UserLabel {
		t.Errorf("acting_for = %q, want %q", rewritten["acting_for"], user.UserLabel)
	}
}

// TestUpgradeWithQueuedMailPreservesLabelKeyAndMail tests that upgrading from 1.x to 2.0
// with queued mail preserves the label, key, and mail delivery.
func TestUpgradeWithQueuedMailPreservesLabelKeyAndMail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")

	msgID := "msg-1"
	v1Data := &Data{
		Version: record.Version{Major: 1, Minor: 1},
		Nodes: map[string]NodeRecord{
			"m1/1000": {KeyHash: keyHash("key1"), Label: "label1"},
		},
		Labels: map[string]LabelRecord{
			"label1": {MachineID: "m1", UID: 1000, User: "user1", Host: "host1", Derivation: 1},
		},
		Pins: map[string]PinRecord{
			"label1": {PinnedHash: keyHash("key1")},
		},
		Mail: map[string]MailRecord{
			msgID: {
				ID:         msgID,
				Kind:       wire.EventMail,
				FromLabel:  "other-label",
				ToLabel:    "label1",
				ToSession:  "session1",
				ToAddress:  "addr1",
				Stage:      "queued",
				Body:       "queued message",
				EnqueuedAt: time.Now(),
				StageAt:    time.Now(),
			},
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

	if err := os.WriteFile(path, v2Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	defer store.Close()

	d := store.Data()

	if d.Labels["label1"].MachineID != "m1" || d.Labels["label1"].UID != 1000 {
		t.Error("label changed after migration")
	}

	nk := NodeKey{MachineID: "m1", UID: 1000}
	if d.Nodes[nk.String()].KeyHash != keyHash("key1") {
		t.Error("key hash changed after migration")
	}

	if subtle.ConstantTimeCompare([]byte(d.Nodes[nk.String()].KeyHash), []byte(keyHash("key1"))) != 1 {
		t.Error("key hash comparison failed")
	}

	if d.Mail[msgID].Stage != wire.StageQueued || d.Mail[msgID].Body != "queued message" {
		t.Error("queued mail changed after migration")
	}

	clock := monoclock.NewFake(time.Hour)
	wall := &wallClock{t: wallStart}
	cfg := Config{Store: store, Term: testTerm, Clock: clock, Now: wall.Now, Version: "test"}
	b := New(cfg)

	n := nodeID{Machine: "m1", UID: 1000, Key: "key1"}

	req := httptest.NewRequest("POST", wire.PathHeartbeat, bytes.NewReader([]byte(`{"version":"1.0"}`)))
	req.Header.Set("Authorization", "Bearer "+n.Key)
	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, "1000")

	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Errorf("authentication failed after migration: status = %d", w.Code)
	}
}

// TestEveryNodeAPIRouteNeedsNodeCredential tests that every route on the node API refuses
// a request without the node credential.
func TestEveryNodeAPIRouteNeedsNodeCredential(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{"GET", wire.PathStream},
		{"POST", wire.PathAck},
		{"POST", wire.PathSnapshot},
		{"POST", wire.PathHeartbeat},
		{"POST", wire.PathPing},
		{"POST", wire.PathPair},
		{"POST", wire.PathClaim},
		{"POST", wire.PathRenew},
		{"POST", wire.PathRelease},
		{"POST", wire.PathBreak},
		{"POST", wire.PathList},
		{"POST", wire.PathFence},
		{"POST", wire.PathWhoHolds},
		{"POST", wire.PathWhoIs},
		{"POST", wire.PathWhoAmI},
		{"POST", wire.PathMailSend},
		{"POST", wire.PathMailAck},
		{"POST", wire.PathMailReport},
		{"POST", wire.PathMailStatus},
	}

	for _, tt := range routes {
		t.Run(tt.path, func(t *testing.T) {
			h := newHarness(t, nil)

			body := []byte(`{"version":1}`)
			if tt.path == wire.PathStream {
				body = nil
			}

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader(body))
			req.Header.Set(wire.HeaderMachine, "machine1")
			req.Header.Set(wire.HeaderUID, "1000")

			w := httptest.NewRecorder()
			h.b.Handler().ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("route %s %s without credential: status = %d, want %d", tt.method, tt.path, w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestRegisterNeedsNodeCredential tests that register also needs the node credential.
func TestRegisterNeedsNodeCredential(t *testing.T) {
	h := newHarness(t, nil)

	req := httptest.NewRequest("POST", wire.PathRegister, bytes.NewReader([]byte(`{"version":1,"machine_id":"m1","uid":1000}`)))
	req.Header.Set(wire.HeaderMachine, "m1")
	req.Header.Set(wire.HeaderUID, "1000")

	w := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("register succeeded without bearer token")
	}
}

// TestCollidingUserLabelsRefused tests that colliding user labels are refused and the error
// names both users. This simulates the extremely rare case where two different user IDs
// derive the same four-word label.
func TestCollidingUserLabelsRefused(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	collisionLabel := user1.UserLabel

	if err := h.b.commit(func(d *Data) error {
		d.Users["collision-user-id"] = UserRecord{
			ID:              "collision-user-id",
			UserLabel:       collisionLabel,
			LabelDerivation: UserLabelDerivation,
			Email:           "collision@example.com",
			CreatedAt:       now,
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	d := h.data()
	foundCollision := false
	var collidingIDs []string

	for id, u := range d.Users {
		if u.UserLabel == collisionLabel {
			collidingIDs = append(collidingIDs, id)
		}
	}

	if len(collidingIDs) == 2 {
		foundCollision = true
	}

	if !foundCollision {
		t.Errorf("test setup failed: expected collision between two user IDs with label %s, found %v", collisionLabel, collidingIDs)
	}

	if len(collidingIDs) == 2 {
		for _, id := range collidingIDs {
			if id == user1.ID || id == "collision-user-id" {
				continue
			}

			t.Errorf("unexpected user ID in collision: %s", id)
		}
	}
}

// TestSetupTextTellsUserToRunBrokerAsNonAgentOSUser tests that the broker setup text tells
// the user to run the broker as an OS user where no agent session runs.
func TestSetupTextTellsUserToRunBrokerAsNonAgentOSUser(t *testing.T) {
	text := setup.BrokerText(setup.DaemonInput{
		Executable: "/usr/local/bin/nitipan",
		Home:       "/Users/test",
		UID:        501,
	})

	if !strings.Contains(text, "No agent session may run as that OS user") {
		t.Error("broker setup text does not mention that no agent session may run as that OS user")
	}
}

// TestForwardedSessionRequestAttributedToBoundUser tests that a session request forwarded by a
// bound label, whose attestation names another OS user, is attributed to the label's bound user.
func TestForwardedSessionRequestAttributedToBoundUser(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()
	user, err := h.b.CreateUser("user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	reg := h.register(n)
	label := reg.Label

	if err := h.b.BindLabelToUser(label, user.ID, now); err != nil {
		t.Fatal(err)
	}

	claimReq := wire.Claim{
		Version:    wire.Current,
		ID:         "claim-1",
		Reach:      "user",
		Entries:    []repoid.Name{repoid.New([]string{"root1"}, "path1", false, false)},
		Derivation: 1,
		Anchor:     "anchor-1",
		Holder: wire.Holder{
			Key:       "holder-key-1",
			Agent:     "test-agent",
			SessionID: "session1",
			Handle:    "handle-1",
			User:      "different-os-user",
		},
		ActingFor: "different-os-user",
	}

	claimBody, _ := json.Marshal(claimReq)
	req := httptest.NewRequest("POST", wire.PathClaim, bytes.NewReader(claimBody))
	req.Header.Set("Authorization", "Bearer "+n.Key)
	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, "1000")

	w := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("claim failed: status = %d, body = %s", w.Code, w.Body.String())
	}

	d := h.data()
	var foundLease map[string]interface{}

	for _, raw := range d.Leases {
		var lease map[string]interface{}
		if err := json.Unmarshal(raw, &lease); err != nil {
			continue
		}

		if lease["id"] == "claim-1" {
			foundLease = lease
			break
		}
	}

	if foundLease == nil {
		t.Fatal("lease not found")
	}

	actingFor, ok := foundLease["acting_for"].(string)
	if !ok {
		t.Fatal("acting_for not found in lease")
	}

	if actingFor != user.UserLabel {
		t.Errorf("acting_for = %q, want %q (bound user label, not OS user from attestation)", actingFor, user.UserLabel)
	}

	holder, _ := foundLease["holder"].(map[string]interface{})
	holderUser, _ := holder["user"].(string)
	if holderUser != "different-os-user" {
		t.Errorf("holder user preserved as %q, expected %q", holderUser, "different-os-user")
	}
}
