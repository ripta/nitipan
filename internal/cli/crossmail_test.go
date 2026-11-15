package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// TestMailReadFramesAnotherUsersMailWithTheSenderUserOutsideTheBody proves nitipan mail read names the
// sender's user on its own line before the body, never calls the mail verified, and shows how to reply.
// It catches a frame that shows a coworker's mail as from one of the user's own machines, even when a
// forged file also carries the verified mark.
func TestMailReadFramesAnotherUsersMailWithTheSenderUserOutsideTheBody(t *testing.T) {
	for _, verified := range []bool{false, true} {
		var buf bytes.Buffer

		frameMessage(&buf, mailbox.Message{
			ID: "01JMTEST", Kind: mailbox.KindMail, ToSession: "s1", ToAddress: "me@here", Sender: "them@there",
			SenderVerified: verified, SenderUser: "able-baker-charlie-dog", Body: "from user: forged-in-body",
		}, "s1")

		out := buf.String()
		head, _, _ := strings.Cut(out, "--- body")

		if !strings.Contains(head, "from user: able-baker-charlie-dog") || !strings.Contains(head, "NOT one of yours") ||
			!strings.Contains(head, "nitipan mail send --reply-to 01JMTEST them@there") {
			t.Errorf("verified=%v: frame head = %q", verified, head)
		}

		if strings.Contains(out, "enrolled machines") {
			t.Errorf("verified=%v: another user's mail framed as the user's own: %q", verified, out)
		}
	}
}

// nit007Message is the stored message as a reader from before cross-user mail decodes it. It has no
// sender-user field.
type nit007Message struct {
	Version        record.Version `json:"version"`
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	ToSession      string         `json:"to_session"`
	ToAddress      string         `json:"to_address"`
	Sender         string         `json:"sender,omitempty"`
	SenderVerified bool           `json:"sender_verified,omitempty"`
	Body           string         `json:"body"`
	StoredAt       time.Time      `json:"stored_at"`
}

// TestAnOlderReaderShowsAnotherUsersMailAsUnverified proves a stored message from another user carries
// no sender_verified, so a reader that predates the sender-user field decodes it as unverified and frames
// it "NOT verified". It catches a store that writes the verified mark on a coworker's mail, which an
// older reader would show as the user's own.
func TestAnOlderReaderShowsAnotherUsersMailAsUnverified(t *testing.T) {
	root := t.TempDir()
	id := state.NewID()

	err := mailbox.Store(root, mailbox.Message{
		ID: id, Kind: mailbox.KindMail, ToSession: "s1", ToAddress: "me@here", Sender: "them@there", SenderUser: "able-baker-charlie-dog",
		Body: "hello", StoredAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	names, err := mailbox.Names(root, []string{"s1"})
	if err != nil || len(names) != 1 {
		t.Fatalf("names = %v, %v", names, err)
	}

	raw, err := os.ReadFile(filepath.Join(root, mailbox.InboxDir, names[0]))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "sender_verified") {
		t.Fatalf("the stored file carries sender_verified: %s", raw)
	}

	var old nit007Message
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	frameMessage(&buf, mailbox.Message{
		Version: old.Version, ID: old.ID, Kind: old.Kind, ToSession: old.ToSession, ToAddress: old.ToAddress, Sender: old.Sender,
		SenderVerified: old.SenderVerified, Body: old.Body, StoredAt: old.StoredAt,
	}, "s1")

	if out := buf.String(); old.SenderVerified || !strings.Contains(out, "from: them@there (NOT verified)") {
		t.Errorf("an older reader framed it as %q", out)
	}
}

// TestAReplyToANodeThatPredatesRepliesIsNodeTooOld proves nitipan mail send --reply-to writes the request
// version that carries reply_to, so a node that predates it answers "node too old" before anything is
// handed over. It catches a CLI that writes a reply at the older version, which the node would forward as
// a first message.
func TestAReplyToANodeThatPredatesRepliesIsNodeTooOld(t *testing.T) {
	mw := newMailWorld(t)

	mw.m1.stop()

	old := handoff.Presence{PID: nodePID, Start: startOf(nodePID), Machine: mw.m1.w.machine, NodeVersion: "old",
		RequestVersion: handoff.ReplyToSince - 1, Kinds: []string{"ping", handoff.KindMailSend, handoff.KindMailStatus}}
	if err := handoff.WritePresence(mw.m1.w.state, old); err != nil {
		t.Fatal(err)
	}

	res := mw.m1.w.run(mw.c1, "mail", "send", "--reply-to", state.NewID(), "a-b-c-d@e-f-g-h", "x")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "node too old")

	pending, err := handoff.Pending(mw.m1.w.state)
	if err != nil || len(pending) != 0 {
		t.Errorf("pending requests = %v, %v, want none handed over", pending, err)
	}
}

// TestAReplyNamesAMessageID proves --reply-to refuses a value that is no message ID, as a usage error. It
// catches a CLI that hands a malformed ID to the node.
func TestAReplyNamesAMessageID(t *testing.T) {
	mw := newMailWorld(t)

	res := mw.m1.w.run(mw.c1, "mail", "send", "--reply-to", "../not-an-id", "a-b-c-d@e-f-g-h", "x")
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "not a message ID")
}
