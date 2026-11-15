package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

func (n *Node) registerLeaseKinds() {
	n.forwrd[handoff.KindClaim] = n.forwardClaim
	n.forwrd[handoff.KindRelease] = n.forwardRelease
	n.forwrd[handoff.KindBreak] = n.forwardBreak
	n.forwrd[handoff.KindRenew] = n.forwardRenew
	n.forwrd[handoff.KindList] = n.forwardList
	n.forwrd[handoff.KindFence] = n.forwardFence
	n.forwrd[handoff.KindWhoHolds] = n.forwardWhoHolds
	n.forwrd[handoff.KindWhoIs] = n.forwardWhoIs
}

func decodeBodyOf(req handoff.Request, into any) error {
	if err := json.Unmarshal(req.Body, into); err != nil {
		return fmt.Errorf("decoding the %s request body: %w", req.Kind, err)
	}

	return nil
}

func (n *Node) forwardClaim(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body handoff.ClaimBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	n.rememberLate(req.ID, lateReply{holder: body.Holder, kind: req.Kind, anchor: body.Anchor})

	sent := n.clockNow()

	var res wire.Result

	err := n.cl.call(ctx, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: req.ID, Reach: body.Reach, Entries: body.Entries, Derivation: body.Derivation,
		Anchor: body.Anchor.ID, Holder: wireHolder(body.Holder), ActingFor: body.ActingFor, Description: body.Description,
	}, &res)
	if err != nil {
		return nil, err
	}

	if res.Code == wire.CodeGranted && res.Lease != nil {
		// A repeat of a claim the broker already granted returns the held lease under its own ID. The
		// sidecar is keyed by that ID, and an existing one keeps its created flag.
		leaseID := res.Lease.ID

		sc := handoff.Sidecar{
			ID: leaseID, Reach: body.Reach, Anchor: body.Anchor, Entries: body.Entries, Derivation: body.Derivation,
			Holder: body.Holder, Description: body.Description, CreatedAt: n.opts.Now().UTC(), Token: res.Lease.Token,
		}

		err := n.withLock(func(l *state.Locked) error {
			if have, err := handoff.ReadSidecar(n.opts.StateDir, leaseID); err == nil {
				if have.Token == sc.Token {
					return nil
				}

				have.Token = sc.Token

				return handoff.WriteSidecar(l, have)
			}

			return handoff.WriteSidecar(l, sc)
		})
		if err != nil {
			return nil, fmt.Errorf("writing the sidecar of lease %s: %w", leaseID, err)
		}

		n.book.apply(*res.Lease, sent, true)
		n.publishDeadlinesNow()

		// The claimant sees these overlaps as warnings in the claim's own output, but only once it reads
		// the reply. Until then the snapshot answer that repeats them holds back its notice.
		held := make([]string, len(res.Overlaps))
		for i, o := range res.Overlaps {
			held[i] = overlapKey(o, sideGrant)
		}

		n.holdOverlaps(req.ID, held)
	}

	return json.Marshal(res)
}

func (n *Node) forwardRelease(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body handoff.ReleaseBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	n.rememberLate(req.ID, lateReply{holder: body.Holder, kind: req.Kind})

	var res wire.Result

	err := n.cl.call(ctx, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: req.ID, ID: body.ID, Name: body.Name, Holder: wireHolder(body.Holder), Cause: wire.CauseReleased,
	}, &res)
	if err != nil {
		return nil, err
	}

	reply := handoff.ReleaseReply{Result: res}

	if res.Code == wire.CodeReleased || res.Code == wire.CodeNotFound {
		ids := []string{body.ID}

		if res.Lease != nil {
			ids = []string{res.Lease.ID}
		}

		for _, o := range res.Others {
			ids = append(ids, o.ID)
		}

		mine := handoff.HolderKey(body.Holder.Holder, body.Holder.Liveness)

		for _, id := range ids {
			sc, err := handoff.ReadSidecar(n.opts.StateDir, id)
			if err != nil || handoff.HolderKey(sc.Holder.Holder, sc.Holder.Liveness) != mine {
				continue
			}

			reply.Sidecars = append(reply.Sidecars, sc)

			_ = n.withLock(func(l *state.Locked) error { return handoff.RemoveSidecar(l, id) })

			n.book.mu.Lock()
			delete(n.book.leases, id)
			n.book.mu.Unlock()
		}

		n.publishDeadlinesNow()
	}

	return json.Marshal(reply)
}

func (n *Node) forwardBreak(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body handoff.BreakBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	var res wire.Result

	err := n.cl.call(ctx, wire.PathBreak, wire.Break{
		Version: wire.Current, RequestID: req.ID, ID: body.ID, Force: body.Force,
		By: wire.Holder{Agent: body.By.Agent, SessionID: body.By.SessionID, Handle: body.By.Handle, Name: body.By.Name},
	}, &res)
	if err != nil {
		return nil, err
	}

	return json.Marshal(res)
}

func (n *Node) forwardRenew(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body handoff.RenewBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	items := make([]wire.RenewItem, len(body.IDs))
	live := map[string]handoff.Sidecar{}

	for i, id := range body.IDs {
		items[i] = wire.RenewItem{ID: id, Liveness: wire.LiveAlive}

		if sc, err := handoff.ReadSidecar(n.opts.StateDir, id); err == nil {
			live[id] = sc
		}
	}

	sent := n.clockNow()

	var out wire.Renewed
	if err := n.cl.call(ctx, wire.PathRenew, wire.Renew{Version: wire.Current, Items: items}, &out); err != nil {
		return nil, err
	}

	n.applyRenewed(out, sent, live)
	n.publishDeadlinesNow()

	return json.Marshal(out)
}

func (n *Node) forwardList(ctx context.Context, _ handoff.Request) (json.RawMessage, error) {
	var out wire.Listed
	if err := n.cl.call(ctx, wire.PathList, wire.ListRequest{Version: wire.Current}, &out); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

func (n *Node) forwardWhoHolds(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body wire.WhoHolds
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	body.Version = wire.Current

	var out wire.WhoHoldsAnswer
	if err := n.cl.call(ctx, wire.PathWhoHolds, body, &out); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

func (n *Node) forwardWhoIs(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body wire.WhoIs
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	body.Version = wire.Current

	var out wire.WhoIsAnswer
	if err := n.cl.call(ctx, wire.PathWhoIs, body, &out); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

func (n *Node) forwardFence(ctx context.Context, req handoff.Request) (json.RawMessage, error) {
	var body handoff.FenceBody
	if err := decodeBodyOf(req, &body); err != nil {
		return nil, err
	}

	var out wire.FenceVerdict
	if err := n.cl.call(ctx, wire.PathFence, wire.FenceReport{Version: wire.Current, Token: body.Token}, &out); err != nil {
		return nil, err
	}

	return json.Marshal(out)
}

// recordToken stores the broker's token in a lease's sidecar. It never creates a sidecar, so a lease
// released meanwhile stays gone.
func (n *Node) recordToken(sc handoff.Sidecar, token uint64) {
	if token == 0 || sc.ID == "" || sc.Token == token {
		return
	}

	id := sc.ID

	err := n.withLock(func(l *state.Locked) error {
		sc, err := handoff.ReadSidecar(n.opts.StateDir, id)
		if err != nil || sc.Token == token {
			return nil
		}

		sc.Token = token

		return handoff.WriteSidecar(l, sc)
	})
	if err != nil {
		n.warn(fmt.Sprintf("recording the token of lease %s: %v", id, err))
	}
}

func (n *Node) publishDeadlinesNow() {
	scs, _, err := handoff.Sidecars(n.opts.StateDir)
	if err == nil {
		n.publishDeadlines(scs)
	}
}

func (n *Node) holdOverlaps(id string, held []string) {
	n.book.mu.Lock()
	defer n.book.mu.Unlock()

	if lr, ok := n.book.late[id]; ok {
		lr.overlaps = held
		n.book.late[id] = lr
	}
}

// overlapAwaitsReader reports whether a claim reply that carries the overlap is still waiting for its
// CLI. A reply that was read means the CLI printed the warning, so the pair counts as reported. A reply
// left unread past the grace is a CLI that never printed it, so the holder gets the notice.
func (n *Node) overlapAwaitsReader(key string) bool {
	n.book.mu.Lock()

	var ids []string

	for id, lr := range n.book.late {
		if slices.Contains(lr.overlaps, key) {
			ids = append(ids, id)
		}
	}

	n.book.mu.Unlock()

	if len(ids) == 0 {
		return false
	}

	ages, err := handoff.Answered(n.opts.StateDir, time.Now())
	if err != nil {
		return true
	}

	for _, id := range ids {
		age, waiting := ages[id]

		switch {
		case !waiting:
			n.book.once(key)

			return true
		case age < n.replyGrace():
			return true
		}
	}

	return false
}

func (n *Node) replyGrace() time.Duration {
	if n.opts.ReplyGrace == 0 {
		return DefaultReplyGrace
	}

	return n.opts.ReplyGrace
}

func (n *Node) rememberLate(id string, lr lateReply) {
	n.book.mu.Lock()
	n.book.late[id] = lr
	n.book.mu.Unlock()
}

// deliverLate turns a reply that nobody read within the grace into a notice. A CLI that timed out after
// the node claimed its request learns the outcome that way, and a retry of the same command returns
// the same answer from the broker.
func (n *Node) deliverLate() {
	ages, err := handoff.Answered(n.opts.StateDir, time.Now())
	if err != nil {
		return
	}

	n.book.mu.Lock()
	pending := make(map[string]lateReply, len(n.book.late))

	for id, lr := range n.book.late {
		pending[id] = lr
	}
	n.book.mu.Unlock()

	grace := n.replyGrace()

	for id, lr := range pending {
		age, waiting := ages[id]

		switch {
		case !waiting:
			for _, key := range lr.overlaps {
				n.book.once(key)
			}

			n.forgetLate(id)
		case age >= grace:
			n.lateNotice(id, lr)
			handoff.Consume(n.opts.StateDir, id)
			n.forgetLate(id)
		}
	}
}

func (n *Node) forgetLate(id string) {
	n.book.mu.Lock()
	delete(n.book.late, id)
	n.book.mu.Unlock()
}

func (n *Node) lateNotice(id string, lr lateReply) {
	rep, err := handoff.ReadReply(n.opts.StateDir, id)
	if err != nil {
		return
	}

	var text string

	switch {
	case rep.Error != nil:
		text = fmt.Sprintf("your %s request %s failed after nitipan stopped waiting: %s. Run the command again, which reuses the request",
			lr.kind, id, rep.Error.Message)
	case lr.kind == handoff.KindClaim:
		text = lateClaimText(id, lr, rep.Body)
	default:
		text = fmt.Sprintf("your %s request %s finished after nitipan stopped waiting. Run list to see the result", lr.kind, id)
	}

	if lr.anchor.Created && lr.kind == handoff.KindClaim && refusedClaim(rep.Body) {
		err := n.withLock(func(l *state.Locked) error {
			return handoff.WriteOrphan(l, handoff.Orphan{ID: id, Anchor: lr.anchor, Holder: lr.holder})
		})
		if err != nil {
			n.warn(fmt.Sprintf("recording the anchor of the refused claim %s: %v", id, err))
		}
	}

	n.notice(lr.holder, id, "request-outcome", 0, text)
}

func lateClaimText(id string, lr lateReply, body json.RawMessage) string {
	var res wire.Result
	if json.Unmarshal(body, &res) != nil {
		return fmt.Sprintf("your claim %s finished after nitipan stopped waiting. Run list to see the result", id)
	}

	if res.Code == wire.CodeGranted {
		return fmt.Sprintf("your claim %s was granted after nitipan stopped waiting. Lease %s now holds it", id, id)
	}

	text := fmt.Sprintf("your claim %s was refused after nitipan stopped waiting: %s", id, res.Message)
	if lr.anchor.Created {
		text += fmt.Sprintf(". The claim took the machine lease %s new, and the next nitipan call releases it", lr.anchor.ID)
	}

	return text
}

// classify turns a broker call failure into the reply code the CLI keys on.
func classify(err error) string {
	var refused *wire.Error

	if errors.Is(err, ErrBrokerTooOld) || (errors.As(err, &refused) && refused.Code == wire.CodeVersion) {
		return handoff.CodeBrokerTooOld
	}

	return handoff.CodeBroker
}

// refusedClaim reports whether a claim reply body says the broker refused the claim.
func refusedClaim(body json.RawMessage) bool {
	var res wire.Result

	return json.Unmarshal(body, &res) == nil && res.Code != wire.CodeGranted
}
