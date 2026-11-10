package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// readClaimedRequest reads a request from the claimed directory.
func readClaimedRequest(root, id string) (handoff.Request, error) {
	data, err := os.ReadFile(filepath.Join(root, handoff.ClaimedDir, id+".json"))
	if err != nil {
		return handoff.Request{}, err
	}

	var req handoff.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return handoff.Request{}, err
	}

	return req, nil
}

// claimSubmittedWhileGated runs acquire with the extra flags while the broker holds the node's claim
// call. The node answers by removing the claimed request, so the test reads it while the gate holds the
// call, then releases the gate and waits for acquire to finish.
func claimSubmittedWhileGated(t *testing.T, flags ...string) (handoff.Request, result) {
	t.Helper()

	f := newWideFixture(t)

	gate := &gateRule{path: wire.PathClaim, open: make(chan struct{})}
	f.gate.Store(gate)

	m := f.machine("m1")
	m.start()

	w := m.w
	repo := w.repoIn("repo")
	c := w.claude(100, "s1")
	w.installHooks(c, repo)

	args := append([]string{"acquire", "--reach", "user", "--checkout", repo, "--scope", "svc"}, flags...)
	done := make(chan result, 1)

	go func() { done <- w.run(c, args...) }()

	var names []string

	waitFor(t, "the node to claim the request", func() bool {
		names, _ = state.ListNames(w.state, handoff.ClaimedDir)

		return len(names) > 0
	})

	req, err := readClaimedRequest(w.state, strings.TrimSuffix(names[0], ".json"))
	if err != nil {
		t.Fatalf("reading the claimed request: %v", err)
	}

	gate.release()

	return req, <-done
}

// TestClaimWithActingForWritesNewRequestVersion proves a claim with --acting-for naming someone other
// than the caller rides the request version that marks acting_for as explicit. It catches a CLI that
// writes such a claim at the claim kind's first version, where the node would let the broker substitute
// the label's own user silently. The fixture's label is unbound, so the broker refuses the claim and
// names the approve step.
func TestClaimWithActingForWritesNewRequestVersion(t *testing.T) {
	req, res := claimSubmittedWhileGated(t, "--acting-for", "myuser")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "nitipan broker approve")

	if req.Kind != handoff.KindClaim || req.Version != handoff.ActingForSince {
		t.Errorf("claim with --acting-for wrote kind %s at version %d, want claim at %d", req.Kind, req.Version, handoff.ActingForSince)
	}
}

// TestClaimWithoutActingForKeepsOldVersion proves a plain claim rides the claim kind's first version,
// so a node that predates explicit acting_for still reads it. It catches a CLI that writes every claim at
// the newest version and so gets "node too old" from an older node.
func TestClaimWithoutActingForKeepsOldVersion(t *testing.T) {
	req, res := claimSubmittedWhileGated(t)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "acquired user lease")

	if want := handoff.KindSince(handoff.KindClaim); req.Kind != handoff.KindClaim || req.Version != want {
		t.Errorf("claim without --acting-for wrote kind %s at version %d, want claim at %d", req.Kind, req.Version, want)
	}
}

// TestClaimWithActingForRefusedByOldNode tests that when the node's presence reports an older
// RequestVersion, a claim with --acting-for fails with "node too old" and writes no request.
func TestClaimWithActingForRefusedByOldNode(t *testing.T) {
	f := newWideFixture(t)
	m := f.machine("m1")
	m.start()

	w := m.w
	repo := w.repoIn("repo")
	c := w.claude(100, "s1")
	w.installHooks(c, repo)

	p, err := handoff.ReadPresence(w.state)
	if err != nil {
		t.Fatal(err)
	}

	p.RequestVersion = 4
	if err := handoff.WritePresence(w.state, p); err != nil {
		t.Fatal(err)
	}

	res := w.run(c, "acquire", "--reach", "user", "--checkout", repo, "--scope", "svc", "--acting-for", "other-user")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "node too old")

	pendingReqs, err := handoff.Pending(w.state)
	if err != nil {
		t.Fatal(err)
	}

	if len(pendingReqs) != 0 {
		t.Errorf("a refused claim should write no request when the node is too old, got %d pending", len(pendingReqs))
	}

	claimedNames, err := state.ListNames(w.state, handoff.ClaimedDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(claimedNames) != 0 {
		t.Errorf("a refused claim should write no request when the node is too old, got %d claimed", len(claimedNames))
	}
}

// labelOf returns the label the broker gave the machine's node.
func (m *wideMachine) labelOf(t *testing.T) string {
	t.Helper()

	var label string

	waitFor(t, "the node's label", func() bool {
		for _, v := range m.f.b.Nodes() {
			if v.Key.MachineID == m.w.machine.ID {
				label = v.Label
			}
		}

		return label != ""
	})

	return label
}

// TestAUserReleasesTheAgentsLeaseForThemByID proves `nitipan release <lease-id>` on U's own machine
// reaches the broker for a lease a shared agent holds for U on another machine, and ends it there. It
// catches a CLI that treats an ID with no local record as a local lease, so the release never leaves
// U's machine and U cannot release the agent's lease for them.
func TestAUserReleasesTheAgentsLeaseForThemByID(t *testing.T) {
	f := newWideFixture(t)

	mu := f.machine("mu")
	mu.start()

	ma := f.machine("ma")
	ma.start()

	now := time.Now()

	u, err := f.b.CreateUser("u@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	v, err := f.b.CreateUser("v@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	agentLabel := ma.labelOf(t)

	if err := f.b.BindLabelToUser(mu.labelOf(t), u.ID, now); err != nil {
		t.Fatal(err)
	}

	if err := f.b.BindLabelToUser(agentLabel, v.ID, now); err != nil {
		t.Fatal(err)
	}

	if err := f.b.LinkIssuerSub("https://issuer.test", "u-sub", u.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.b.GrantDelegation("https://issuer.test", "u-sub", agentLabel, now); err != nil {
		t.Fatal(err)
	}

	repo := ma.w.repoIn("repo")
	agent := ma.w.claude(100, "agent")
	ma.w.installHooks(agent, repo)

	res := ma.w.run(agent, "acquire", "--reach", "user", "--checkout", repo, "--scope", "svc", "--acting-for", u.UserLabel)
	wantStatus(t, res, exitcode.OK)

	id := leaseIDIn(t, res.Stdout)

	ownRepo := mu.w.repoIn("repo")
	session := mu.w.claude(200, "u-session")
	mu.w.installHooks(session, ownRepo)

	res = mu.w.run(session, "release", id)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "released "+id+" (user lease)")

	for _, l := range f.b.Leases() {
		if l.ID == id && l.State == wire.StateLive {
			t.Fatalf("the agent's lease is still live after U's release: %+v", l)
		}
	}
}
