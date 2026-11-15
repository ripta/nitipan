package node

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

func TestGrantWritesTheBrokersTokenIntoTheSidecar(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	if granted.Token == 0 {
		t.Fatal("the broker returned no token")
	}

	sc, err := handoff.ReadSidecar(lr.state, granted.ID)
	if err != nil || sc.Token != granted.Token {
		t.Fatalf("sidecar = %+v, %v, want token %d", sc, err, granted.Token)
	}
}

func TestRenewalPutsBackATokenAnOlderBinaryDropped(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	sc, err := handoff.ReadSidecar(lr.state, granted.ID)
	if err != nil {
		t.Fatal(err)
	}

	sc.Token = 0

	err = lr.run.n.withLock(func(l *state.Locked) error { return handoff.WriteSidecar(l, sc) })
	if err != nil {
		t.Fatal(err)
	}

	lr.clock.Advance(2 * time.Minute)

	eventually(t, "a renewal to record the token again", func() bool {
		got, err := handoff.ReadSidecar(lr.state, granted.ID)

		return err == nil && got.Token == granted.Token
	})
}

func TestRenewalNeverRecreatesAReleasedSidecar(t *testing.T) {
	lr := newLeaseRig(t, nil)
	granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

	err := lr.run.n.withLock(func(l *state.Locked) error { return handoff.RemoveSidecar(l, granted.ID) })
	if err != nil {
		t.Fatal(err)
	}

	lr.run.n.recordToken(handoff.Sidecar{ID: granted.ID}, granted.Token+1)

	if _, err := handoff.ReadSidecar(lr.state, granted.ID); err == nil {
		t.Fatal("recording a token must not create a sidecar")
	}
}

type fenceForwardTest struct {
	Name    string
	Token   func(counter uint64) uint64
	Verdict string
}

var fenceForwardTests = []fenceForwardTest{
	{"known", func(c uint64) uint64 { return c }, wire.FenceKnown},
	{"raised", func(c uint64) uint64 { return c + 7 }, wire.FenceRaised},
	{"unusable", func(uint64) uint64 { return ^uint64(0) - 1 }, wire.FenceUnusable},
}

func TestFenceReportIsForwardedAndTheKindIsAdvertised(t *testing.T) {
	for _, test := range fenceForwardTests {
		t.Run(test.Name, func(t *testing.T) {
			lr := newLeaseRig(t, nil)
			granted := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))

			p, err := handoff.ReadPresence(lr.state)
			if err != nil || p.Supports(handoff.KindFence, handoff.RequestVersion) != nil {
				t.Fatalf("presence = %+v, %v", p, err)
			}

			body, _ := json.Marshal(handoff.FenceBody{Token: test.Token(granted.Token)})
			rep := lr.submit(handoff.Request{ID: state.NewID(), Kind: handoff.KindFence, CreatedAt: time.Now(), Body: body})

			var out wire.FenceVerdict
			if err := json.Unmarshal(rep.Body, &out); err != nil || rep.Error != nil || out.Verdict != test.Verdict {
				t.Fatalf("reply = %+v (%s), %v, want %s", rep.Error, rep.Body, err, test.Verdict)
			}
		})
	}
}
