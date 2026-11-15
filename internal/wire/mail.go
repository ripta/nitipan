package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Mail protocol levels a node reports at registration. A node that reports none predates mail, and the
// broker never sends it a mail event kind. MailCapabilityBasic carries mail between one user's machines.
// MailCapabilityCrossUser also knows mail from another user: the sender-user field, the bounce reasons
// for a grant or reach that lapsed, replies, and the reconnect report that comes before any re-check.
const (
	MailCapabilityBasic     = 1
	MailCapabilityCrossUser = 2
)

// MailCapability is the mail protocol level this binary speaks.
const MailCapability = MailCapabilityCrossUser

// MaxMailBody is the largest message body, in bytes. The broker refuses a larger one at send, which
// keeps queued and bounced mail small in the whole-file store.
const MaxMailBody = 64 << 10

// MaxSnapshotSessions is the most mailbox sessions the broker reads from one snapshot. It reads the
// first ones in the order the node listed them, and logs the rest as refused. A node that holds this
// many registrations has stopped reaping the dead ones.
const MaxSnapshotSessions = 256

// MailRetention is how long the broker holds mail it never sent, how long a node keeps stored mail
// for a session that is not running, and how long a bounce that cannot reach its sender is kept.
const MailRetention = 7 * 24 * time.Hour

// Paths for the mail calls. A broker that does not serve one answers 404, which a node reports as
// "broker too old".
const (
	PathMailSend   = Prefix + "/mail/send"
	PathMailAck    = Prefix + "/mail/ack"
	PathMailReport = Prefix + "/mail/report"
	PathMailStatus = Prefix + "/mail/status"
)

// Event kinds the broker sends only to a node that registered a mail capability.
const (
	EventMail   = "mail"
	EventStage  = "stage"
	EventBounce = "bounce"
)

// Stages of a message.
const (
	StageQueued = "queued"
	StageStored = "stored"
	StageRead   = "read"
)

// Reasons a message bounces.
const (
	ReasonUnsent       = "the broker held it for 7 days without sending it"
	ReasonNotRunning   = "the recipient was not running, and no resume read it within 7 days"
	ReasonAmbiguous    = "two live sessions hold the recipient's address"
	ReasonNodeTooOld   = "the recipient node is too old"
	ReasonLabelState   = "the recipient's machine stayed unapproved or forgotten for 7 days"
	ReasonForgotten    = "the recipient's machine was forgotten with the message stored but unread"
	ReasonBadRecipient = "the recipient could not be resolved"
	ReasonRebound      = "the recipient's machine was bound to a different user"

	// ReasonNotAllowed and ReasonNoSharedLease bounce mail from another user that the broker re-checked
	// before delivery. The grant that admitted its conversation was revoked, or a first message's
	// recipient no longer holds a `shared` lease.
	ReasonNotAllowed    = "sender no longer allowed"
	ReasonNoSharedLease = "recipient holds no `shared` lease"
)

// Error codes the mail calls return, beside the codes in wire.go.
const (
	CodeNoSuchSession = "no_such_session"
	CodeNodeTooOld    = "recipient_node_too_old"
	CodeTooLarge      = "too_large"
	CodeNotApproved   = "not_approved"
	CodePlainHTTP     = "plain_http"
	CodeNotMailable   = "not_mailable"

	// CodeSenderTooOld refuses mail to another user from a node that does not know mail across users.
	CodeSenderTooOld = "sender_node_too_old"

	// CodeNotGranted refuses mail to another user that no grant admits: the recipient's user granted the
	// sender's user nothing, a first message's recipient holds no `shared` lease, or a reply names no
	// message it may answer.
	CodeNotGranted = "not_granted"
)

// MailSession is one session ID a node holds a live or past registration for. The broker keeps every
// ID a label ever reported.
type MailSession struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Live      bool   `json:"live,omitempty"`
}

// MailSend asks the broker to carry a message. The ID is the request ID, so a retry returns the same
// answer. FromSession names the sender's session. The broker takes the sender's label from the
// authenticated connection and checks the session against what the node reported. Any other sender
// field in the body is ignored.
type MailSend struct {
	Version     Version `json:"version"`
	ID          string  `json:"id"`
	To          string  `json:"to"`
	Body        string  `json:"body"`
	FromAgent   string  `json:"from_agent"`
	FromSession string  `json:"from_session"`

	// ReplyTo names the message this one answers. A reply to mail from another user rides the grant of
	// that message's conversation, and needs no `shared` lease on either side.
	ReplyTo string `json:"reply_to,omitempty"`
}

// MailSent answers a MailSend.
type MailSent struct {
	Version Version `json:"version"`
	ID      string  `json:"id"`
	To      string  `json:"to"`
	Stage   string  `json:"stage"`
}

// MailEvent is the data of a mail or bounce event. Sender is the stamped <mail name>@<label> of the
// sending session, and the empty string for a bounce from the broker. SenderVerified is absent for an
// unverified sender. It means the sender is one of the recipient user's own machines.
//
// SenderUser is the user label of the sender's user, set only on mail from another user. Such mail
// never carries SenderVerified, so a reader that predates SenderUser shows it as unverified, never as
// the user's own.
type MailEvent struct {
	ID             string `json:"id"`
	ToSession      string `json:"to_session"`
	ToAddress      string `json:"to_address"`
	Sender         string `json:"sender,omitempty"`
	SenderVerified bool   `json:"sender_verified,omitempty"`
	SenderUser     string `json:"sender_user,omitempty"`
	Body           string `json:"body"`

	// Reason and Orig are set on a bounce. Orig is the ID of the message that bounced.
	Reason string `json:"reason,omitempty"`
	Orig   string `json:"orig,omitempty"`

	// BodyDropped is true when a bounce carries no body, as when a forgotten machine held the message.
	BodyDropped bool `json:"body_dropped,omitempty"`

	EnqueuedAt time.Time `json:"enqueued_at"`
}

// StageEvent tells a sender's node that its message reached a stage.
type StageEvent struct {
	ID    string    `json:"id"`
	Stage string    `json:"stage"`
	At    time.Time `json:"at"`
}

// Mail ack outcomes.
const (
	MailStored  = "stored"
	MailRefused = "refused"
)

// MailAck tells the broker what a node did with a mail or bounce event. It is sent only after the
// message is on the node's disk.
type MailAck struct {
	Version Version `json:"version"`
	ID      string  `json:"id"`
	Outcome string  `json:"outcome"`

	// Reason says why a message was refused, in the words of the Reason constants.
	Reason string `json:"reason,omitempty"`
}

// ExpiredMail is a message a node held for 7 days with no session to read it. The node still has the
// body, so the bounce carries it.
type ExpiredMail struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// MailReport tells the broker what a node holds. A reconnecting node sends it before the broker
// resends anything, so only messages the node did not store are sent again.
//
// Complete marks the report a reconnecting node sends of everything it holds. The broker re-checks mail
// from another user, and may bounce it, only after the connection's complete report arrived, so no
// message the node stored or read is ever bounced.
type MailReport struct {
	Version  Version       `json:"version"`
	Complete bool          `json:"complete,omitempty"`
	Stored   []string      `json:"stored,omitempty"`
	Read     []string      `json:"read,omitempty"`
	Expired  []ExpiredMail `json:"expired,omitempty"`
}

// MailStatusRequest asks for the stage of a message the node sent.
type MailStatusRequest struct {
	Version Version `json:"version"`
	ID      string  `json:"id"`
}

// MailStatus answers a MailStatusRequest.
type MailStatus struct {
	Version Version `json:"version"`
	ID      string  `json:"id"`
	To      string  `json:"to"`
	Stage   string  `json:"stage"`

	// SentUnacked is true when the broker sent the message and saw no ack for it.
	SentUnacked bool `json:"sent_unacked,omitempty"`

	// Bounced is true when the message came back to its sender, and Reason says why.
	Bounced bool   `json:"bounced,omitempty"`
	Reason  string `json:"reason,omitempty"`

	// RecipientRunning is false when no live registration holds the recipient's session.
	RecipientRunning bool   `json:"recipient_running"`
	Note             string `json:"note,omitempty"`
}

// KeyHash is the SHA-256 of an instance key, in hex. The broker stores only this.
func KeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])
}

// Fingerprint is the short form of a key hash that the node prints and the user passes to the
// broker's approve command. It reveals nothing a hash does not.
func Fingerprint(keyHash string) string {
	if len(keyHash) < 16 {
		return keyHash
	}

	h := keyHash[:16]

	return strings.Join([]string{h[0:4], h[4:8], h[8:12], h[12:16]}, "-")
}
