package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// maxLabelSessions is the most session IDs the broker stores for one label. An address once reported
// stays valid, so the set only grows. This bound is about 1 MiB of store per label. At 10 new sessions a
// day it lasts about 2.7 years. A label that reaches it refuses new IDs with a logged reason, and
// forgetting the label does not clear them.
const maxLabelSessions = 10000

// noteSessionsLocked records the sessions a node reported. The live set replaces the last one. An ID
// the label never reported before joins the stored set, which only grows.
//
// Only a pinned label's sessions are read. A pending label can neither send nor receive mail, and its
// key proves nothing yet. The broker reads the first wire.MaxSnapshotSessions entries and refuses an
// entry whose agent has no mail name or whose ID is not safe in a file name. It refuses a new ID once
// the label holds maxLabelSessions. Each refusal is logged with its reason, and none is dropped quietly.
func (b *Broker) noteSessionsLocked(key NodeKey, ns *nodeState, sessions []wire.MailSession) {
	label := b.store.Data().Nodes[key.String()].Label

	if label == "" || !b.pinnedLocked(label) {
		ns.liveSessions = nil

		return
	}

	if len(sessions) > wire.MaxSnapshotSessions {
		b.cfg.Logger.Warn("mail sessions refused: the snapshot lists too many", "label", label,
			"reported", len(sessions), "limit", wire.MaxSnapshotSessions, "refused", len(sessions)-wire.MaxSnapshotSessions)

		sessions = sessions[:wire.MaxSnapshotSessions]
	}

	live := make(map[string]bool, len(sessions))
	stored := b.store.Data().Sessions[label]

	var fresh []wire.MailSession

	invalid, overCap := 0, 0

	for _, s := range sessions {
		if !mailableAgent(s.Agent) || !session.ValidSession(s.SessionID) {
			invalid++

			continue
		}

		if _, known := stored[s.SessionID]; !known {
			if len(stored)+len(fresh) >= maxLabelSessions {
				overCap++

				continue
			}

			fresh = append(fresh, s)
		}

		if s.Live {
			live[s.SessionID] = true
		}
	}

	ns.liveSessions = live

	if invalid > 0 {
		b.cfg.Logger.Warn("mail sessions refused: unknown agent or unsafe session ID", "label", label, "refused", invalid)
	}

	if overCap > 0 {
		b.cfg.Logger.Warn("mail sessions refused: the label already stores the most it may", "label", label,
			"limit", maxLabelSessions, "refused", overCap)
	}

	if len(fresh) == 0 {
		return
	}

	now := b.cfg.Now()

	_ = b.commit(func(d *Data) error {
		if d.Sessions[label] == nil {
			d.Sessions[label] = map[string]SessionRecord{}
		}

		for _, s := range fresh {
			d.Sessions[label][s.SessionID] = SessionRecord{Agent: s.Agent, NameVersion: MailNameDerivation, FirstSeen: now}
		}

		return nil
	})
}

// mailableAgent reports whether an agent has a mail name derivation.
func mailableAgent(agent string) bool {
	switch agent {
	case agentClaude, agentCodex, agentOpencode:
		return true
	}

	return false
}

// resolveRecipientLocked finds the session ID a mail name stands for under a label.
func (b *Broker) resolveRecipientLocked(label, name string) (sessionID string, ok bool) {
	row, found := b.store.Data().Labels[label]
	if !found {
		return "", false
	}

	ids := make([]string, 0, len(b.store.Data().Sessions[label]))
	for id := range b.store.Data().Sessions[label] {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		rec := b.store.Data().Sessions[label][id]

		if got, err := DeriveMailName(rec.Agent, id, row.MachineID); err == nil && got == name {
			return id, true
		}
	}

	return "", false
}

func (b *Broker) handleMailSend(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.MailSend

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	rec, nodeOK := b.store.Data().Nodes[key.String()]
	if !nodeOK {
		writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "this node is not registered")

		return
	}

	if status, code, msg := b.checkSenderLocked(key, rec, req); code != "" {
		writeErr(w, status, code, msg)

		return
	}

	if status, code, msg := b.checkRecipientLocked(req); code != "" {
		writeErr(w, status, code, msg)

		return
	}

	if have, ok := b.store.Data().Mail[req.ID]; ok {
		if have.FromLabel != rec.Label {
			writeErr(w, http.StatusConflict, wire.CodeBadRequest, "that message ID belongs to another sender")

			return
		}

		writeJSON(w, http.StatusOK, wire.MailSent{Version: wire.Current, ID: have.ID, To: have.ToAddress, Stage: have.Stage})

		return
	}

	// A conversation is keyed by its first message's ID, and outlives that message while its replies are
	// held. A new message under that ID would rebind the conversation to another grant, so it is refused.
	if _, held := b.store.Data().Conversations[req.ID]; held {
		writeErr(w, http.StatusConflict, wire.CodeBadRequest, fmt.Sprintf("message ID %s already names a conversation the broker "+
			"holds. Send the message under a new ID", req.ID))

		return
	}

	cross, status, code, msg := b.checkCrossUserLocked(rec, req)
	if code != "" {
		writeErr(w, status, code, msg)

		return
	}

	b.enqueueMailLocked(w, rec.Label, req, cross)
}

// checkSenderLocked judges the sending node and session. It returns the status, code, and message of
// the refusal, or an empty code.
func (b *Broker) checkSenderLocked(key NodeKey, rec NodeRecord, req wire.MailSend) (status int, code, msg string) {
	if !state.ValidID(req.ID) {
		return http.StatusBadRequest, wire.CodeBadRequest, "the message ID is not a request ID"
	}

	if !rec.HTTPS {
		return http.StatusForbidden, wire.CodePlainHTTP, "this node reaches the broker over plain HTTP. Mail needs a broker address " +
			"that is HTTPS with a certificate the node verifies"
	}

	if !b.pinnedLocked(rec.Label) {
		return http.StatusForbidden, wire.CodeNotApproved, fmt.Sprintf("label %s is not approved for mail. Run nitipan broker pending "+
			"on the broker host, then nitipan broker approve", rec.Label)
	}

	if len(req.Body) == 0 {
		return http.StatusBadRequest, wire.CodeBadRequest, "the message has no body"
	}

	if len(req.Body) > wire.MaxMailBody {
		return http.StatusRequestEntityTooLarge, wire.CodeTooLarge, fmt.Sprintf("the body is %d bytes, and the limit is %d", len(req.Body), wire.MaxMailBody)
	}

	ns := b.node(key)

	if _, known := b.store.Data().Sessions[rec.Label][req.FromSession]; !known || !ns.liveSessions[req.FromSession] {
		return http.StatusForbidden, wire.CodeNotMailable, "the sending session is not a live registration on this node, " +
			"so replies and bounces would have no mailbox to land in"
	}

	return 0, "", ""
}

func (b *Broker) checkRecipientLocked(req wire.MailSend) (status int, code, msg string) {
	name, label, found := strings.Cut(req.To, "@")
	if !found || name == "" || label == "" || strings.Contains(label, "@") {
		return http.StatusBadRequest, wire.CodeBadRequest, fmt.Sprintf("%q is not a <mail name>@<label> address", req.To)
	}

	if _, ok := b.store.Data().Labels[label]; !ok {
		return http.StatusNotFound, wire.CodeNoSuchSession, fmt.Sprintf("no such session: the broker has never seen the machine label %q", label)
	}

	if _, ok := b.resolveRecipientLocked(label, name); !ok {
		return http.StatusNotFound, wire.CodeNoSuchSession, fmt.Sprintf("no such session: no node under %s ever reported %q", label, name)
	}

	row := b.store.Data().Labels[label]

	if rec, ok := b.store.Data().Nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}.String()]; ok && rec.MailCap < wire.MailCapabilityBasic {
		return http.StatusConflict, wire.CodeNodeTooOld, fmt.Sprintf("recipient node too old: the node under %s predates mail. "+
			"Upgrade its nitipan", label)
	}

	return 0, "", ""
}

// mailUserLocked names the user ID a label's mail belongs to: the user whose scope the label acts in. It
// is empty for a label nobody bound once cross-user features are on, and no mail crosses to such a label.
func (b *Broker) mailUserLocked(label string) string {
	user, _ := b.scopeUserLocked(label)

	return user.ID
}

// crossMail is what admits one message across users. Its zero value is mail between one user's own
// machines, which no grant governs.
type crossMail struct {
	// SenderUser and RecipientUser are user IDs. SenderLabel is the sender user's user label, which the
	// message carries in place of sender_verified.
	SenderUser, RecipientUser, SenderLabel string

	// Conversation is the conversation whose grant admits the message. Opens is true on its first
	// message, which creates the conversation record.
	Conversation string
	Opens        bool
}

func (c crossMail) across() bool { return c.Conversation != "" }

// checkCrossUserLocked judges mail whose sender and recipient belong to different users. A first message
// needs the recipient user's grant to the sender's user, and an address whose session holds a live or
// lapsed `shared` lease. A reply needs neither. It rides the grant of the conversation of the message it
// answers, and must come from that message's recipient address, within 7 days of its read.
//
// acting_for plays no part. The recipient's user is the bound user of the address's label, so mail to a
// shared agent follows the grants of the agent's own user.
func (b *Broker) checkCrossUserLocked(rec NodeRecord, req wire.MailSend) (cross crossMail, status int, code, msg string) {
	d := b.store.Data()

	_, toLabel, _ := strings.Cut(req.To, "@")
	senderUser, recipientUser := b.mailUserLocked(rec.Label), b.mailUserLocked(toLabel)

	if senderUser == recipientUser {
		return crossMail{}, 0, "", ""
	}

	if !d.CrossUserEnabled {
		return crossMail{}, http.StatusForbidden, wire.CodeNotGranted, "mail to another user's machine needs cross-user features on. " +
			"The broker owner turns them on with nitipan broker cross-user true on the broker host"
	}

	if rec.MailCap < wire.MailCapabilityCrossUser {
		return crossMail{}, http.StatusForbidden, wire.CodeSenderTooOld, "this node's nitipan predates mail to another user. " +
			"Upgrade it, then send again"
	}

	row := d.Labels[toLabel]
	if to, ok := d.Nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}.String()]; ok && to.MailCap < wire.MailCapabilityCrossUser {
		return crossMail{}, http.StatusConflict, wire.CodeNodeTooOld, fmt.Sprintf("recipient node too old: the node under %s predates "+
			"mail from another user. Its user can upgrade its nitipan", toLabel)
	}

	sender, recipient := d.Users[senderUser], d.Users[recipientUser]
	if sender.UserLabel == "" || recipient.UserLabel == "" {
		return crossMail{}, http.StatusForbidden, wire.CodeNotGranted, "the broker cannot name the user of the sender or the recipient, " +
			"so it carries no mail between them"
	}

	cross = crossMail{SenderUser: senderUser, RecipientUser: recipientUser, SenderLabel: sender.UserLabel}

	if req.ReplyTo != "" {
		conv, why := b.replyConversationLocked(rec, req, senderUser, recipientUser)
		if why != "" {
			return crossMail{}, http.StatusForbidden, wire.CodeNotGranted, why
		}

		cross.Conversation = conv

		return cross, 0, "", ""
	}

	if _, ok := d.MailGrants[recipientUser][senderUser]; !ok {
		return crossMail{}, http.StatusForbidden, wire.CodeNotGranted, fmt.Sprintf("user %s has not granted mail from your user, %s. "+
			"They grant it on the broker's sign-in page", recipient.UserLabel, sender.UserLabel)
	}

	if !b.sharedReachLocked(toLabel, req.To) {
		return crossMail{}, http.StatusForbidden, wire.CodeNotGranted, fmt.Sprintf("%s holds no live or lapsed `shared` lease, so a "+
			"first message from another user cannot reach it. A reply to mail it sent you needs no lease", req.To)
	}

	cross.Conversation, cross.Opens = req.ID, true

	return cross, 0, "", ""
}

// replyConversationLocked finds the conversation a reply rides. It returns the reason the reply is
// refused, or an empty reason.
func (b *Broker) replyConversationLocked(rec NodeRecord, req wire.MailSend, senderUser, recipientUser string) (conv, why string) {
	d := b.store.Data()

	orig, ok := d.Mail[req.ReplyTo]
	if !ok || orig.Kind != wire.EventMail || orig.Conversation == "" || orig.Bounced {
		return "", fmt.Sprintf("message %s is not mail from another user that the broker still holds. A reply answers such a "+
			"message within 7 days of its read", req.ReplyTo)
	}

	from, _ := b.mailAddress(wire.Holder{Agent: req.FromAgent, SessionID: req.FromSession}, rec.Label)
	if orig.ToLabel != rec.Label || orig.ToAddress != from {
		return "", fmt.Sprintf("only %s, the address message %s was sent to, may reply to it", orig.ToAddress, req.ReplyTo)
	}

	if req.To != orig.FromAddress {
		return "", fmt.Sprintf("a reply to message %s goes to its sender, %s", req.ReplyTo, orig.FromAddress)
	}

	if orig.Stage != wire.StageRead {
		return "", fmt.Sprintf("message %s has not been read yet, and a reply answers a message that was read. Read it with "+
			"nitipan mail read, then reply", req.ReplyTo)
	}

	if b.cfg.Now().Sub(orig.StageAt) > wire.MailRetention {
		return "", fmt.Sprintf("message %s was read more than 7 days ago, so the window for a reply has closed", req.ReplyTo)
	}

	c, ok := d.Conversations[orig.Conversation]
	if !ok || !sameUsers(c, senderUser, recipientUser) {
		return "", fmt.Sprintf("the broker no longer holds the conversation of message %s", req.ReplyTo)
	}

	if _, ok := d.MailGrants[c.RecipientUser][c.SenderUser]; !ok {
		return "", fmt.Sprintf("%s: the grant that admitted the conversation of message %s was revoked", wire.ReasonNotAllowed, req.ReplyTo)
	}

	return orig.Conversation, ""
}

// sameUsers reports whether two users are the two ends of a conversation, in either direction.
func sameUsers(c ConversationRecord, a, b string) bool {
	return (c.SenderUser == a && c.RecipientUser == b) || (c.SenderUser == b && c.RecipientUser == a)
}

// sharedReachLocked reports whether the session at an address holds a live or lapsed `shared` lease,
// the reach cross-user discovery already gives it. A lease another holder took is not reach.
func (b *Broker) sharedReachLocked(label, addr string) bool {
	for _, g := range b.g.grants {
		if g.Node != label || g.Reach != wire.ReachShared || (g.State != wire.StateLive && g.State != wire.StateLapsed) {
			continue
		}

		if got, _ := b.mailAddress(g.Holder, label); got == addr {
			return true
		}
	}

	return false
}

func (b *Broker) enqueueMailLocked(w http.ResponseWriter, fromLabel string, req wire.MailSend, cross crossMail) {
	name, label, _ := strings.Cut(req.To, "@")
	sid, _ := b.resolveRecipientLocked(label, name)
	now := b.cfg.Now()

	from, _ := b.mailAddress(wire.Holder{Agent: req.FromAgent, SessionID: req.FromSession}, fromLabel)

	// sender_verified keeps its single-user meaning: the sender is one of the recipient user's own
	// machines. Mail from another user carries that user's label instead.
	m := MailRecord{
		ID: req.ID, Kind: wire.EventMail, FromLabel: fromLabel, FromAgent: req.FromAgent, FromSession: req.FromSession, FromAddress: from,
		ToLabel: label, ToSession: sid, ToAddress: req.To, Body: req.Body, Verified: !cross.across(),
		SenderUser: cross.SenderLabel, Conversation: cross.Conversation,
		Stage: wire.StageQueued, StageAt: now, EnqueuedAt: now,
	}

	if cross.across() {
		m.ReplyTo = req.ReplyTo
	}

	err := b.commit(func(d *Data) error {
		d.Mail[m.ID] = m

		if cross.Opens {
			d.Conversations[cross.Conversation] = ConversationRecord{
				SenderUser: cross.SenderUser, RecipientUser: cross.RecipientUser, CreatedAt: now,
			}
		}

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	b.wakeLabelLocked(label)

	writeJSON(w, http.StatusOK, wire.MailSent{Version: wire.Current, ID: m.ID, To: m.ToAddress, Stage: m.Stage})
}

// mailEventOf builds the event data for a record. The sender is stamped from the record, which the
// broker filled from the authenticated connection.
func mailEventOf(m MailRecord) wire.MailEvent {
	return wire.MailEvent{
		ID: m.ID, ToSession: m.ToSession, ToAddress: m.ToAddress, Sender: m.FromAddress, SenderVerified: m.Verified,
		SenderUser: m.SenderUser, Body: m.Body, Reason: m.Reason, Orig: m.Orig, BodyDropped: m.BodyDropped, EnqueuedAt: m.EnqueuedAt,
	}
}

// flushMail writes the mail queued for a node to its stream. The broker sends only to a node whose
// label is pinned to the node's current key, that registered a mail capability, and that reaches the
// broker over HTTPS. Marking a message sent is what stops it bouncing as unsent, so it is persisted
// before the write.
//
// Mail from another user goes only to a node that knows it, and is re-checked first. The re-check runs
// only once this connection's complete stored-and-read report arrived. Until then such mail is held, so
// a message the node already stored or read is never bounced. The report covers only the stream's own
// connection, and only mail sent before it, so the re-check skips what this connection was sent.
//
// A node that came back below the cross-user capability is sent none of it. Mail it was never sent bounces
// at once. Mail it was sent as a newer binary bounces only once the complete report left it queued.
func (b *Broker) flushMail(w http.ResponseWriter, key NodeKey, st *streamState, sentThisStream map[string]bool) error {
	b.mu.Lock()

	rec, ok := b.store.Data().Nodes[key.String()]
	if ok && rec.MailCap < wire.MailCapabilityBasic {
		b.bounceOldNodeLocked(rec.Label)
	}

	if !ok || rec.MailCap < wire.MailCapabilityBasic || !rec.HTTPS || rec.KeyHash != st.keyHash {
		b.mu.Unlock()

		return nil
	}

	if !b.pinnedLocked(rec.Label) {
		b.mu.Unlock()

		return nil
	}

	ns := b.node(key)
	reported := ns.mailReported && st.conn == ns.conn

	crossCapable := rec.MailCap >= wire.MailCapabilityCrossUser

	switch {
	case !crossCapable:
		// A node that came back too old may hold mail it stored as a newer binary before its ack was
		// lost. Such mail waits for this connection's complete report, which advances what the node holds.
		b.bounceCrossUserLocked(rec.Label, func(m MailRecord) string {
			if m.Sent && (!reported || ns.sentSinceReport[m.ID]) {
				return ""
			}

			return wire.ReasonNodeTooOld
		})
	case reported:
		b.bounceCrossUserLocked(rec.Label, func(m MailRecord) string {
			if ns.sentSinceReport[m.ID] {
				return ""
			}

			return b.recheckCrossUserLocked(m)
		})
	}

	var out []MailRecord

	for _, m := range b.store.Data().Mail {
		if m.ToLabel == rec.Label && m.Stage == wire.StageQueued && !m.Bounced && !sentThisStream[m.ID] &&
			(m.Conversation == "" || (reported && crossCapable)) {
			out = append(out, m)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].EnqueuedAt.Before(out[j].EnqueuedAt)
		}

		return out[i].ID < out[j].ID
	})

	err := b.commit(func(d *Data) error {
		for _, m := range out {
			m.Sent = true
			d.Mail[m.ID] = m
		}

		return nil
	})

	if err == nil {
		for _, m := range out {
			if m.Conversation == "" {
				continue
			}

			if ns.sentSinceReport == nil {
				ns.sentSinceReport = map[string]bool{}
			}

			ns.sentSinceReport[m.ID] = true
		}
	}

	b.mu.Unlock()

	if err != nil {
		return err
	}

	for _, m := range out {
		sentThisStream[m.ID] = true

		data, err := json.Marshal(mailEventOf(m))
		if err != nil {
			return fmt.Errorf("encoding mail %s: %w", m.ID, err)
		}

		if err := wire.WriteSSE(w, wire.SSE{ID: "mail." + m.ID, Event: m.Kind, Data: string(data)}); err != nil {
			return err
		}
	}

	return nil
}

// bounceInto turns a message into a bounce for its sender. It runs inside a commit. A bounce never
// bounces: one that cannot reach its sender is dropped.
func (b *Broker) bounceInto(d *Data, orig MailRecord, reason string, withBody bool, now time.Time) {
	if orig.Kind != wire.EventMail {
		delete(d.Mail, orig.ID)

		return
	}

	body := orig.Body

	orig.Bounced, orig.BounceReason, orig.Body, orig.StageAt = true, reason, "", now
	d.Mail[orig.ID] = orig

	if orig.FromLabel == "" || orig.FromSession == "" {
		return
	}

	if !withBody {
		body = ""
	}

	id := state.NewID()

	d.Mail[id] = MailRecord{
		ID: id, Kind: wire.EventBounce, ToLabel: orig.FromLabel, ToSession: orig.FromSession, ToAddress: orig.FromAddress,
		Body: body, BodyDropped: !withBody, Reason: reason, Orig: orig.ID, Stage: wire.StageQueued, StageAt: now, EnqueuedAt: now,
	}
}

func (b *Broker) handleMailAck(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.MailAck

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	label := b.store.Data().Nodes[key.String()].Label
	now := b.cfg.Now()

	m, ok := b.store.Data().Mail[req.ID]
	if !ok || m.ToLabel != label {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	var changed []MailRecord

	err := b.commit(func(d *Data) error {
		switch req.Outcome {
		case wire.MailStored:
			changed = b.advanceInto(d, m, wire.StageStored, now)
		case wire.MailRefused:
			if m.Stage == wire.StageQueued && !m.Bounced {
				b.bounceInto(d, m, nonEmpty(req.Reason, wire.ReasonAmbiguous), true, now)
			}
		}

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	b.afterMailChangeLocked(changed)
	b.wakeSendersLocked(m.FromLabel)

	w.WriteHeader(http.StatusNoContent)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}

// advanceInto moves a message to a later stage inside a commit. A bounce that reached its recipient is
// done and is dropped. It returns the message when the stage changed, so the sender can be told.
func (b *Broker) advanceInto(d *Data, m MailRecord, stage string, now time.Time) []MailRecord {
	if m.Kind != wire.EventMail {
		delete(d.Mail, m.ID)

		return nil
	}

	if m.Bounced || stageRank(stage) <= stageRank(m.Stage) {
		return nil
	}

	m.Stage, m.StageAt, m.Body = stage, now, ""
	d.Mail[m.ID] = m

	return []MailRecord{m}
}

func stageRank(stage string) int {
	switch stage {
	case wire.StageQueued:
		return 0
	case wire.StageStored:
		return 1
	}

	return 2
}

// afterMailChangeLocked tells each sender its message reached a new stage. A stage never wakes the
// sender, and it never produces a stage or a bounce of its own.
func (b *Broker) afterMailChangeLocked(changed []MailRecord) {
	for _, m := range changed {
		row, ok := b.store.Data().Labels[m.FromLabel]
		if !ok {
			continue
		}

		nk := NodeKey{MachineID: row.MachineID, UID: row.UID}

		rec, ok := b.store.Data().Nodes[nk.String()]
		if !ok || rec.MailCap < wire.MailCapabilityBasic {
			continue
		}

		_, _ = b.enqueueLocked(nk, wire.EventStage, wire.StageEvent{ID: m.ID, Stage: m.Stage, At: m.StageAt})
	}
}

// wakeSendersLocked wakes the stream of a label so a bounce just queued for it goes out.
func (b *Broker) wakeSendersLocked(label string) {
	if label != "" {
		b.wakeLabelLocked(label)
	}
}

func (b *Broker) handleMailReport(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.MailReport

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	label := b.store.Data().Nodes[key.String()].Label
	now := b.cfg.Now()

	var changed []MailRecord

	senders := map[string]bool{}

	err := b.commit(func(d *Data) error {
		for _, id := range req.Stored {
			if m, ok := d.Mail[id]; ok && m.ToLabel == label {
				changed = append(changed, b.advanceInto(d, m, wire.StageStored, now)...)
			}
		}

		for _, id := range req.Read {
			m, ok := d.Mail[id]
			if !ok || m.ToLabel != label {
				continue
			}

			read := b.advanceInto(d, m, wire.StageRead, now)
			changed = append(changed, read...)

			if c, ok := d.Conversations[m.Conversation]; ok && len(read) > 0 {
				c.LastMessageRead = now
				d.Conversations[m.Conversation] = c
			}
		}

		for _, e := range req.Expired {
			m, ok := d.Mail[e.ID]
			if !ok || m.ToLabel != label || m.Bounced || m.Stage == wire.StageRead {
				continue
			}

			m.Body = e.Body
			b.bounceInto(d, m, wire.ReasonNotRunning, true, now)

			senders[m.FromLabel] = true
		}

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	b.afterMailChangeLocked(changed)

	for l := range senders {
		b.wakeSendersLocked(l)
	}

	// The complete report is what lets the stream re-check mail from another user.
	if req.Complete {
		b.node(key).mailReported = true
		b.wakeLabelLocked(label)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (b *Broker) handleMailStatus(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.MailStatusRequest

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	label := b.store.Data().Nodes[key.String()].Label

	m, ok := b.store.Data().Mail[req.ID]
	if !ok || m.FromLabel != label || m.Kind != wire.EventMail {
		writeErr(w, http.StatusNotFound, wire.CodeNoSuchSession, "no message "+req.ID+" was sent from this machine")

		return
	}

	st := wire.MailStatus{
		Version: wire.Current, ID: m.ID, To: m.ToAddress, Stage: m.Stage, Bounced: m.Bounced, Reason: m.BounceReason,
		SentUnacked: m.Sent && m.Stage == wire.StageQueued && !m.Bounced,
	}

	if row, ok := b.store.Data().Labels[m.ToLabel]; ok {
		if ns, ok := b.nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}]; ok {
			st.RecipientRunning = ns.liveSessions[m.ToSession]
		}
	}

	if !st.RecipientRunning && !st.Bounced && m.Stage != wire.StageRead {
		st.Note = "the recipient is not running. The message waits for a resume"
	}

	writeJSON(w, http.StatusOK, st)
}

// sweepMailLocked bounces mail the broker never sent after 7 days, and drops what is finished. A
// message the broker sent and saw no ack for never bounces here. Its node reports it when it returns.
func (b *Broker) sweepMailLocked() {
	wall := b.cfg.Now()

	b.sweepConversationsLocked(wall)

	due := false

	for _, m := range b.store.Data().Mail {
		if mailDue(m, wall) {
			due = true

			break
		}
	}

	if !due {
		return
	}

	var senders []string

	_ = b.commit(func(d *Data) error {
		for _, m := range d.Mail {
			if !mailDue(m, wall) {
				continue
			}

			switch {
			case m.Kind == wire.EventBounce || m.Bounced || m.Stage == wire.StageRead:
				delete(d.Mail, m.ID)
			default:
				reason := wire.ReasonUnsent
				if d.Pins[m.ToLabel].PinnedHash == "" {
					reason = wire.ReasonLabelState
				}

				b.bounceInto(d, m, reason, true, wall)

				senders = append(senders, m.FromLabel)
			}
		}

		return nil
	})

	for _, l := range senders {
		b.wakeSendersLocked(l)
	}
}

// mailDue reports whether a record needs the sweeper.
func mailDue(m MailRecord, wall time.Time) bool {
	switch {
	case m.Kind == wire.EventBounce:
		return wall.Sub(m.EnqueuedAt) > wire.MailRetention
	case m.Bounced || m.Stage == wire.StageRead:
		return wall.Sub(m.StageAt) > wire.MailRetention
	case m.Stage == wire.StageQueued && !m.Sent:
		return wall.Sub(m.EnqueuedAt) > wire.MailRetention
	}

	return false
}

// bounceOldNodeLocked returns the mail queued for a label whose node no longer speaks mail, as when a
// node was downgraded after the mail was queued. The broker never sends such a node a mail event kind.
func (b *Broker) bounceOldNodeLocked(label string) {
	now := b.cfg.Now()

	var senders []string

	for _, m := range b.store.Data().Mail {
		if m.ToLabel == label && m.Stage == wire.StageQueued && !m.Bounced && m.Kind == wire.EventMail {
			senders = append(senders, m.FromLabel)
		}
	}

	if len(senders) == 0 {
		return
	}

	_ = b.commit(func(d *Data) error {
		for _, m := range d.Mail {
			if m.ToLabel == label && m.Stage == wire.StageQueued && !m.Bounced && m.Kind == wire.EventMail {
				b.bounceInto(d, m, wire.ReasonNodeTooOld, true, now)
			}
		}

		return nil
	})

	for _, l := range senders {
		b.wakeSendersLocked(l)
	}
}

// sweepConversationsLocked drops a conversation once the broker holds none of its messages and its last
// read is more than 7 days old. A reply needs the message it answers, which the broker drops 7 days after
// its read, so a dropped conversation could admit no reply anyway.
func (b *Broker) sweepConversationsLocked(wall time.Time) {
	held := map[string]bool{}

	for _, m := range b.store.Data().Mail {
		if m.Conversation != "" {
			held[m.Conversation] = true
		}
	}

	var done []string

	for id, c := range b.store.Data().Conversations {
		if !held[id] && (c.LastMessageRead.IsZero() || wall.Sub(c.LastMessageRead) > wire.MailRetention) {
			done = append(done, id)
		}
	}

	if len(done) == 0 {
		return
	}

	_ = b.commit(func(d *Data) error {
		for _, id := range done {
			delete(d.Conversations, id)
		}

		return nil
	})
}

// recheckCrossUserLocked judges a queued message from another user again before delivery. It returns
// the bounce reason, or an empty reason. The grant that admitted the conversation must still stand, and a
// first message's recipient must still hold a live or lapsed `shared` lease.
func (b *Broker) recheckCrossUserLocked(m MailRecord) string {
	d := b.store.Data()

	c, ok := d.Conversations[m.Conversation]
	if !ok || !d.CrossUserEnabled {
		return wire.ReasonNotAllowed
	}

	if _, ok := d.MailGrants[c.RecipientUser][c.SenderUser]; !ok {
		return wire.ReasonNotAllowed
	}

	if m.ReplyTo == "" && !b.sharedReachLocked(m.ToLabel, m.ToAddress) {
		return wire.ReasonNoSharedLease
	}

	return ""
}

// bounceCrossUserLocked bounces queued mail from another user to a label, and wakes its senders. The reason
// func judges each message. It returns the bounce reason, or an empty reason to keep the message queued.
func (b *Broker) bounceCrossUserLocked(label string, reason func(MailRecord) string) {
	bounce := map[string]string{}

	for id, m := range b.store.Data().Mail {
		if m.ToLabel != label || m.Kind != wire.EventMail || m.Conversation == "" || m.Stage != wire.StageQueued || m.Bounced {
			continue
		}

		if why := reason(m); why != "" {
			bounce[id] = why
		}
	}

	if len(bounce) == 0 {
		return
	}

	now := b.cfg.Now()

	var senders []string

	_ = b.commit(func(d *Data) error {
		for id, why := range bounce {
			m := d.Mail[id]
			senders = append(senders, m.FromLabel)

			b.bounceInto(d, m, why, true, now)
		}

		return nil
	})

	for _, l := range senders {
		b.wakeSendersLocked(l)
	}
}
