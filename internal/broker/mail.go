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

	b.enqueueMailLocked(w, rec.Label, req)
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

	if rec, ok := b.store.Data().Nodes[NodeKey{MachineID: row.MachineID, UID: row.UID}.String()]; ok && rec.MailCap < wire.MailCapability {
		return http.StatusConflict, wire.CodeNodeTooOld, fmt.Sprintf("recipient node too old: the node under %s predates mail. "+
			"Upgrade its nitipan", label)
	}

	return 0, "", ""
}

func (b *Broker) enqueueMailLocked(w http.ResponseWriter, fromLabel string, req wire.MailSend) {
	name, label, _ := strings.Cut(req.To, "@")
	sid, _ := b.resolveRecipientLocked(label, name)
	now := b.cfg.Now()

	from, _ := b.mailAddress(wire.Holder{Agent: req.FromAgent, SessionID: req.FromSession}, fromLabel)

	m := MailRecord{
		ID: req.ID, Kind: wire.EventMail, FromLabel: fromLabel, FromAgent: req.FromAgent, FromSession: req.FromSession, FromAddress: from,
		ToLabel: label, ToSession: sid, ToAddress: req.To, Body: req.Body, Verified: true,
		Stage: wire.StageQueued, StageAt: now, EnqueuedAt: now,
	}

	err := b.commit(func(d *Data) error {
		d.Mail[m.ID] = m

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
		Body: m.Body, Reason: m.Reason, Orig: m.Orig, BodyDropped: m.BodyDropped, EnqueuedAt: m.EnqueuedAt,
	}
}

// flushMail writes the mail queued for a node to its stream. The broker sends only to a node whose
// label is pinned to the node's current key, that registered a mail capability, and that reaches the
// broker over HTTPS. Marking a message sent is what stops it bouncing as unsent, so it is persisted
// before the write.
func (b *Broker) flushMail(w http.ResponseWriter, key NodeKey, streamKey string, sentThisStream map[string]bool) error {
	b.mu.Lock()

	rec, ok := b.store.Data().Nodes[key.String()]
	if ok && rec.MailCap < wire.MailCapability {
		b.bounceOldNodeLocked(rec.Label)
	}

	if !ok || rec.MailCap < wire.MailCapability || !rec.HTTPS || rec.KeyHash != streamKey {
		b.mu.Unlock()

		return nil
	}

	if !b.pinnedLocked(rec.Label) {
		b.mu.Unlock()

		return nil
	}

	var out []MailRecord

	for _, m := range b.store.Data().Mail {
		if m.ToLabel == rec.Label && m.Stage == wire.StageQueued && !m.Bounced && !sentThisStream[m.ID] {
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
		if !ok || rec.MailCap < wire.MailCapability {
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
			if m, ok := d.Mail[id]; ok && m.ToLabel == label {
				changed = append(changed, b.advanceInto(d, m, wire.StageRead, now)...)
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
