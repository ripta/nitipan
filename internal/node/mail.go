package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/peercred"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// ErrPlainHTTP means the node reaches the broker over plain HTTP, so it sends and accepts no mail.
var ErrPlainHTTP = errors.New("mail needs a broker address that is HTTPS with a certificate the node verifies")

// ringRetry is how long the node waits before it tries a socket ring again after one failed.
const ringRetry = 30 * time.Second

// ringDeadline bounds one socket ring, so a session that never reads cannot stall the node.
const ringDeadline = 2 * time.Second

func (n *Node) registerMailKinds() {
	n.forwrd[handoff.KindMailSend] = n.forwardMailSend
	n.forwrd[handoff.KindMailStatus] = n.forwardMailStatus
}

func (n *Node) checker() *liveness.Checker { return n.opts.Checker }

func (n *Node) plainHTTPError() error {
	return fmt.Errorf("the broker at %s is reached over plain HTTP. %w", n.opts.Config.Broker, ErrPlainHTTP)
}

// mailSessions lists the session IDs of the node's registrations, live or not. A session ID counts as
// live when any live registration holds it.
func (n *Node) mailSessions() []wire.MailSession {
	if !n.https {
		return nil
	}

	n.mailMu.Lock()
	defer n.mailMu.Unlock()

	regs, _, err := mailbox.Registrations(n.opts.StateDir)
	if err != nil {
		n.warn(fmt.Sprintf("reading the mail registrations: %v", err))

		return nil
	}

	type key struct{ agent, id string }

	seen := map[key]*wire.MailSession{}

	var order []key

	for _, r := range regs {
		alive := mailbox.Live(r, n.checker())

		for _, id := range r.SessionIDs {
			k := key{r.Agent, id}

			s, ok := seen[k]
			if !ok {
				s = &wire.MailSession{Agent: r.Agent, SessionID: id}
				seen[k] = s
				order = append(order, k)
			}

			s.Live = s.Live || alive
		}
	}

	out := make([]wire.MailSession, 0, len(order))
	for _, k := range order {
		out = append(out, *seen[k])
	}

	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })

	return out
}

// handleMail stores a message, then acks the broker. The ack means the message is on this machine's
// disk, so the broker may drop its copy. A message the node already accepted is acked again without a
// second copy, even when the first copy was read.
func (n *Node) handleMail(ctx context.Context, ev wire.Event) error {
	if !n.https {
		n.warn(n.plainHTTPError().Error())

		return nil
	}

	var m wire.MailEvent
	if err := json.Unmarshal(ev.Data, &m); err != nil {
		n.warn(fmt.Sprintf("decoding mail event %s: %v", ev.ID, err))

		return nil
	}

	if !mailbox.ValidID(m.ID) {
		n.warn(fmt.Sprintf("mail event %s names the ID %q, which is no request ID", ev.ID, m.ID))

		return nil
	}

	if n.mailSeen(m.ID) {
		return n.ackMail(ctx, m.ID, wire.MailStored, "")
	}

	refusal, err := n.storeMail(ev.Kind, m)
	if err != nil {
		return err
	}

	if refusal != "" {
		return n.ackMail(ctx, m.ID, wire.MailRefused, refusal)
	}

	if err := n.ackMail(ctx, m.ID, wire.MailStored, ""); err != nil {
		return err
	}

	n.ringMail()

	return nil
}

// storeMail refuses an ambiguous address, and otherwise writes the message and its ledger entry to disk.
// The ledger entry follows the message, and a resend in between is still dropped, because a message
// that is stored, or already read, counts as seen.
func (n *Node) storeMail(eventKind string, m wire.MailEvent) (refusal string, err error) {
	n.mailMu.Lock()
	defer n.mailMu.Unlock()

	root := n.opts.StateDir

	regs, _, err := mailbox.Registrations(root)
	if err != nil {
		return "", fmt.Errorf("reading the registrations: %w", err)
	}

	if res := mailbox.Resolve(regs, m.ToSession, n.checker()); len(res.Ambiguous) > 0 {
		return ambiguityReason(res.Ambiguous), nil
	}

	kind := mailbox.KindMail
	if eventKind == wire.EventBounce {
		kind = mailbox.KindBounce
	}

	err = mailbox.Store(root, mailbox.Message{
		ID: m.ID, Kind: kind, ToSession: m.ToSession, ToAddress: m.ToAddress, Sender: m.Sender, SenderVerified: m.SenderVerified,
		Body: m.Body, Reason: m.Reason, Orig: m.Orig, BodyDropped: m.BodyDropped, StoredAt: n.opts.Now().UTC(),
	})
	if err != nil {
		return "", fmt.Errorf("storing mail %s: %w", m.ID, err)
	}

	if err := mailbox.Accept(root, m.ID, n.opts.Now()); err != nil {
		return "", fmt.Errorf("recording mail %s in the ledger: %w", m.ID, err)
	}

	return "", nil
}

// mailSeen reports whether the node already took a message in: it is in the ledger, still in the inbox,
// or already read. The last two cover a crash between the write and the ledger entry.
func (n *Node) mailSeen(id string) bool {
	root := n.opts.StateDir

	if mailbox.AcceptedExists(root, id) {
		return true
	}

	reads, _ := mailbox.PendingReads(root)
	if slices.Contains(reads, id) {
		return true
	}

	msgs, _ := mailbox.All(root)

	return slices.ContainsFunc(msgs, func(m mailbox.Message) bool { return m.ID == id })
}

func ambiguityReason(regs []mailbox.Registration) string {
	var who []string
	for _, r := range regs {
		who = append(who, fmt.Sprintf("%s pid %d", r.Agent, r.PID))
	}

	return wire.ReasonAmbiguous + ": " + strings.Join(who, " and ")
}

func (n *Node) ackMail(ctx context.Context, id, outcome, reason string) error {
	return n.cl.call(ctx, wire.PathMailAck, wire.MailAck{Version: wire.Current, ID: id, Outcome: outcome, Reason: reason}, nil)
}

func (n *Node) handleStage(ev wire.Event) error {
	var s wire.StageEvent
	if err := json.Unmarshal(ev.Data, &s); err != nil {
		return fmt.Errorf("decoding a stage event: %w", err)
	}

	err := mailbox.SetStage(n.opts.StateDir, s.ID, s.Stage, s.At)
	if errors.Is(err, mailbox.ErrNotRewritten) {
		n.warn(err.Error())

		return nil
	}

	return err
}

// reportMail tells the broker what this node stored and read. A reconnecting node sends it first, so
// the broker resends only what the node did not store.
func (n *Node) reportMail(ctx context.Context) {
	if !n.https {
		return
	}

	root := n.opts.StateDir
	rep := wire.MailReport{Version: wire.Current}

	accepted, err := mailbox.AcceptedAll(root)
	if err != nil {
		n.warn(fmt.Sprintf("reading the mail ledger: %v", err))

		return
	}

	for id, a := range accepted {
		rep.Stored = append(rep.Stored, id)

		if a.State == mailbox.StateRead {
			rep.Read = append(rep.Read, id)
		}
	}

	reads, _ := mailbox.PendingReads(root)
	for _, id := range reads {
		rep.Stored = append(rep.Stored, id)
		rep.Read = append(rep.Read, id)
	}

	msgs, _ := mailbox.All(root)
	for _, m := range msgs {
		rep.Stored = append(rep.Stored, m.ID)
	}

	rep.Stored, rep.Read = uniq(rep.Stored), uniq(rep.Read)

	if len(rep.Stored) == 0 {
		return
	}

	n.sendReport(ctx, rep, reads)
}

func uniq(ids []string) []string {
	sort.Strings(ids)

	return slices.Compact(ids)
}

// sendReport posts a report and, once the broker has it, settles the read markers it carried.
func (n *Node) sendReport(ctx context.Context, rep wire.MailReport, reads []string) {
	err := n.cl.call(ctx, wire.PathMailReport, rep, nil)
	if errors.Is(err, ErrBrokerTooOld) {
		return
	}

	if err != nil {
		n.warn(fmt.Sprintf("reporting mail to the broker: %v", err))

		return
	}

	for _, id := range reads {
		// The broker has the read. A ledger entry this node cannot rewrite keeps its older state, and the
		// marker still goes, since the broker never resends mail it knows was read.
		if err := mailbox.SetAcceptedRead(n.opts.StateDir, id, n.opts.Now()); err != nil && !errors.Is(err, mailbox.ErrNotRewritten) {
			n.warn(fmt.Sprintf("marking mail %s read in the ledger: %v", id, err))

			continue
		}

		mailbox.ClearRead(n.opts.StateDir, id)
	}
}

// reportReads tells the broker which messages readers consumed. It lists one directory, so it runs on
// the node's fast tick, and the sender sees the read stage within moments.
func (n *Node) reportReads(ctx context.Context) {
	if !n.https {
		return
	}

	reads, _ := mailbox.PendingReads(n.opts.StateDir)
	if len(reads) > 0 {
		n.sendReport(ctx, wire.MailReport{Version: wire.Current, Stored: reads, Read: reads}, reads)
	}
}

// serviceMail expires mail that waited 7 days for a session that never resumed, and prunes what is
// finished. It reads the whole mailbox, so it runs on a slow tick.
func (n *Node) serviceMail(ctx context.Context) {
	if !n.https {
		return
	}

	now := n.opts.Now()

	n.expireMail(ctx, now)

	if err := mailbox.PruneAccepted(n.opts.StateDir, now); err != nil {
		n.warn(fmt.Sprintf("pruning the mail ledger: %v", err))
	}

	n.reapRegistrations(now)
}

// reapAfter is how long a dead process's registration stays before the node removes it.
const reapAfter = 24 * time.Hour

func (n *Node) reapRegistrations(now time.Time) {
	n.mailMu.Lock()
	defer n.mailMu.Unlock()

	regs, _, err := mailbox.Registrations(n.opts.StateDir)
	if err != nil {
		return
	}

	for _, r := range regs {
		if !mailbox.Live(r, n.checker()) && now.Sub(r.UpdatedAt) > reapAfter {
			if err := mailbox.RemoveRegistration(n.opts.StateDir, r.Identity); err != nil {
				n.warn(err.Error())
			}
		}
	}
}

// expireMail reports stored mail no live session has held for 7 days. The broker bounces it with the
// body the node still has, and the node removes its copy once the broker has the report.
func (n *Node) expireMail(ctx context.Context, now time.Time) {
	root := n.opts.StateDir

	due := n.expiredMail(now)
	if len(due) == 0 {
		return
	}

	rep := wire.MailReport{Version: wire.Current}

	for _, m := range due {
		if m.Kind == mailbox.KindMail {
			rep.Expired = append(rep.Expired, wire.ExpiredMail{ID: m.ID, Body: m.Body})
		}
	}

	if len(rep.Expired) > 0 {
		if err := n.cl.call(ctx, wire.PathMailReport, rep, nil); err != nil {
			n.warn(fmt.Sprintf("reporting expired mail to the broker: %v", err))

			return
		}
	}

	for _, m := range due {
		if err := mailbox.Remove(root, m); err != nil {
			n.warn(err.Error())
		}
	}
}

func (n *Node) expiredMail(now time.Time) []mailbox.Message {
	n.mailMu.Lock()
	defer n.mailMu.Unlock()

	root := n.opts.StateDir

	msgs, err := mailbox.All(root)
	if err != nil {
		return nil
	}

	regs, unusable, err := mailbox.Registrations(root)
	if err != nil {
		return nil
	}

	// A registration this node cannot read may name any session. Until it is gone or readable, no session
	// can be called dead, so no mail expires on the ground that nothing holds its session.
	if len(unusable) > 0 {
		return nil
	}

	var due []mailbox.Message

	for _, m := range msgs {
		if now.Sub(m.StoredAt) <= wire.MailRetention || mailbox.Resolve(regs, m.ToSession, n.checker()).Holder != nil {
			continue
		}

		due = append(due, m)
	}

	return due
}

// ringMail rings every live Claude Code session that has unrung mail and no live waiter. The socket
// ring is the fallback to the waiter, so a message the waiter already rang is never rung again.
func (n *Node) ringMail() {
	if !n.https {
		return
	}

	n.mailMu.Lock()
	defer n.mailMu.Unlock()

	root := n.opts.StateDir

	if names, err := state.ListNames(root, mailbox.InboxDir); err != nil || len(names) == 0 {
		return
	}

	regs, _, err := mailbox.Registrations(root)
	if err != nil || len(regs) == 0 {
		return
	}

	msgs, _ := mailbox.All(root)
	if len(msgs) == 0 {
		return
	}

	for _, r := range regs {
		ad, ok := adapter.ByName(r.Agent)
		if !ok || ad.Delivery.Push != adapter.PushSocket || !mailbox.Live(r, n.checker()) || n.waiterLive(r) {
			continue
		}

		var keys []string

		for _, m := range msgs {
			if r.Holds(m.ToSession) && mailbox.Resolve(regs, m.ToSession, n.checker()).Holder != nil &&
				mailbox.Resolve(regs, m.ToSession, n.checker()).Holder.Identity == r.Identity {
				keys = append(keys, mailbox.MailKey(m.ID))
			}
		}

		if len(keys) > 0 {
			n.ringSocket(ad, r, keys)
		}
	}
}

func (n *Node) waiterLive(r mailbox.Registration) bool {
	w, ok := mailbox.Waiter(n.opts.StateDir, r.Identity)
	if !ok {
		return false
	}

	env := r.Envelope()
	env.Holder.PID, env.Holder.Start = w.PID, w.Start

	return n.checker() == nil || n.checker().Check(env).Status != liveness.Dead
}

func (n *Node) ringSocket(ad adapter.Adapter, r mailbox.Registration, keys []string) {
	if r.Endpoint.Socket == "" {
		return
	}

	n.ringMu.Lock()
	until := n.ringAfter[r.Identity]
	n.ringMu.Unlock()

	now := n.opts.Now()
	if now.Before(until) {
		return
	}

	fresh, err := mailbox.Claim(n.opts.StateDir, r.Identity, keys, now)

	switch {
	case errors.Is(err, mailbox.ErrNotRewritten):
		// The ledger cannot record this ring, so a repeat is possible. Ring at most once in a while.
		n.warn(err.Error())

		n.ringMu.Lock()
		n.ringAfter[r.Identity] = now.Add(ringRetry)
		n.ringMu.Unlock()
	case err != nil:
		n.warn(fmt.Sprintf("claiming the ring ledger of %s: %v", r.Identity, err))

		return
	}

	if len(fresh) == 0 {
		return
	}

	if err := dialRing(r, len(fresh)); err != nil {
		_ = mailbox.Unclaim(n.opts.StateDir, r.Identity, fresh)

		n.ringMu.Lock()
		n.ringAfter[r.Identity] = now.Add(ringRetry)
		n.ringMu.Unlock()

		n.warn(fmt.Sprintf("ringing the %s session %s: %v", ad.Name, r.Current, err))
	}
}

// ringLine is the line a ring writes to a Claude Code inbox socket. Its body format is undocumented,
// and only the auth line is. The node never sends the auth line: posting with the session's token would
// pass the node off as the session's own child and get around the receiver's inbound controls.
type ringLine struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Sender    string `json:"sender"`
	MessageID string `json:"message_id"`
}

// dialRing writes one ring to a session's inbox socket, only after the kernel names the registered PID
// as the socket's peer. A registration lives in a directory every sandbox can write, so its socket path
// is not trusted. The ring carries no content.
func dialRing(r mailbox.Registration, count int) error {
	conn, err := (&net.Dialer{Timeout: ringDeadline}).Dial("unix", r.Endpoint.Socket)
	if err != nil {
		return fmt.Errorf("connecting to the inbox socket: %w", err)
	}

	defer conn.Close()

	peer, err := peercred.Of(conn)
	if err != nil {
		return fmt.Errorf("checking the inbox socket's peer: %w", err)
	}

	if peer.PID != r.PID || peer.UID != os.Getuid() {
		return fmt.Errorf("the inbox socket's peer is pid %d, uid %d, and the registration names pid %d: refusing to ring it",
			peer.PID, peer.UID, r.PID)
	}

	line, err := json.Marshal(ringLine{
		Type: "user", Content: mailbox.RingText(count), Sender: "nitipan", MessageID: fmt.Sprintf("nitipan-ring-%d", time.Now().UnixNano()),
	})
	if err != nil {
		return fmt.Errorf("encoding the ring: %w", err)
	}

	_ = conn.SetWriteDeadline(time.Now().Add(ringDeadline))

	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing the ring: %w", err)
	}

	return nil
}

func (n *Node) forwardMailSend(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	if !n.https {
		return nil, n.plainHTTPError()
	}

	var body handoff.MailSendBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	// Publish the sender's registration first. The broker sends only for a session the node reported.
	if err := n.sendSnapshot(ctx); err != nil {
		return nil, err
	}

	var out wire.MailSent

	err := n.cl.call(ctx, wire.PathMailSend, wire.MailSend{
		Version: wire.Current, ID: req.ID, To: body.To, Body: body.Body, FromAgent: body.FromAgent, FromSession: body.FromSession,
	}, &out)
	if err != nil {
		return nil, err
	}

	err = mailbox.WriteSent(n.opts.StateDir, mailbox.Sent{ID: out.ID, To: out.To, Stage: out.Stage, At: n.opts.Now()})
	if err != nil {
		n.warn(fmt.Sprintf("noting sent mail %s: %v", out.ID, err))
	}

	return json.Marshal(out)
}

func (n *Node) forwardMailStatus(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	if !n.https {
		return nil, n.plainHTTPError()
	}

	var body handoff.MailStatusBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	var out wire.MailStatus

	err := n.cl.call(ctx, wire.PathMailStatus, wire.MailStatusRequest{Version: wire.Current, ID: body.ID}, &out)
	if err != nil {
		return nil, err
	}

	return json.Marshal(out)
}
