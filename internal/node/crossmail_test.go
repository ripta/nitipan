package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/tlstest"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestStoredMailKeepsTheSenderUserTheBrokerStamped proves the node writes a message's sender-user field
// into the mailbox as the broker sent it, beside an absent sender_verified. It catches a node that drops
// the field, which would leave the reader unable to say whose mail it is.
func TestStoredMailKeepsTheSenderUserTheBrokerStamped(t *testing.T) {
	r := newRig(t)

	n, err := New(r.options())
	if err != nil {
		t.Fatal(err)
	}

	id := state.NewID()

	refusal, err := n.storeMail(wire.EventMail, wire.MailEvent{
		ID: id, ToSession: "sess-u", ToAddress: "name@label", Sender: "other@coworker", SenderUser: "able-baker-charlie-dog", Body: "hi",
	})
	if err != nil || refusal != "" {
		t.Fatalf("store: %q, %v", refusal, err)
	}

	msgs, _, err := mailbox.Pending(r.state, []string{"sess-u"})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("pending = %+v, %v", msgs, err)
	}

	if got := msgs[0]; got.SenderUser != "able-baker-charlie-dog" || got.SenderVerified {
		t.Errorf("stored = %+v", got)
	}
}

// TestAReplyThroughABrokerThatPredatesRepliesIsBrokerTooOld proves a node whose broker did not say it
// reads reply_to answers a reply "broker too old" instead of forwarding it. An older broker would carry
// it as a first message. It catches a node that forwards the reply anyway.
func TestAReplyThroughABrokerThatPredatesRepliesIsBrokerTooOld(t *testing.T) {
	r := newRig(t)
	files := tlstest.SelfSigned(t, t.TempDir())

	opts := r.options()
	opts.Config = config.Node{Broker: newTLSBroker(t, r, files), CAFile: files.Cert}

	n, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}

	n.registerMailKinds()

	body, err := json.Marshal(handoff.MailSendBody{To: "a@b", Body: "x", FromAgent: "claude", FromSession: "s", ReplyTo: state.NewID()})
	if err != nil {
		t.Fatal(err)
	}

	got := n.answer(context.Background(), handoff.Request{
		Version: handoff.ReplyToSince, ID: state.NewID(), Kind: handoff.KindMailSend, CreatedAt: time.Now(), Body: body,
	})

	if got.Error == nil || got.Error.Code != handoff.CodeBrokerTooOld {
		t.Errorf("reply = %+v, want broker too old", got.Error)
	}
}

// reportLog records the mail reports a broker's front door saw, and fails the first few complete ones.
type reportLog struct {
	mu       sync.Mutex
	fail     int
	failed   int
	complete []wire.MailReport
}

func (l *reportLog) intercept(w http.ResponseWriter, req *http.Request) bool {
	if req.URL.Path != wire.PathMailReport {
		return false
	}

	data, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(data))

	var rep wire.MailReport
	if json.Unmarshal(data, &rep) != nil || !rep.Complete {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failed < l.fail {
		l.failed++
		http.Error(w, "the broker lost the connection", http.StatusInternalServerError)

		return true
	}

	l.complete = append(l.complete, rep)

	return false
}

func (l *reportLog) passed() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.complete)
}

// TestAReconnectingNodeSendsACompleteMailReportAndRetriesOneThatFailed proves a node sends a complete
// mail report on every reconnect, even with nothing stored, and sends it again on the mail tick when it
// failed. The broker holds mail from another user until that report arrives. It catches a node that
// skips an empty report or gives up after one failure, which would hold such mail forever.
func TestAReconnectingNodeSendsACompleteMailReportAndRetriesOneThatFailed(t *testing.T) {
	mp := newMailPair(t, func(o *Options) { o.MailEvery = 20 * time.Millisecond })

	log := &reportLog{fail: 2}
	f := log.intercept
	mp.r.intercept.Store(&f)

	// Once node B's stream is live, the broker closes it. Node B then reconnects, and each reconnect
	// sends the complete report under test.
	key := broker.NodeKey{MachineID: secondMachine.ID, UID: os.Getuid()}

	eventually(t, "node B's live stream", func() bool { return mp.streamLive(key) })

	if !mp.r.b.CloseStream(key) {
		t.Fatal("node B had no stream to close")
	}

	eventually(t, "a complete report after the failed ones", func() bool { return log.passed() > 0 })

	log.mu.Lock()
	defer log.mu.Unlock()

	if log.failed != 2 {
		t.Errorf("failed reports = %d, want 2 before the retry passed", log.failed)
	}

	if rep := log.complete[0]; len(rep.Stored) != 0 || len(rep.Read) != 0 {
		t.Errorf("the empty node reported %+v", rep)
	}
}
