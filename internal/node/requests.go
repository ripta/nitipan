package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/wire"
)

// KindPing is the request kind that proves the handoff reaches the broker. It echoes the node's
// label.
const KindPing = "ping"

func (n *Node) kinds() []string {
	out := make([]string, 0, len(n.forwrd))
	for k := range n.forwrd {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func (n *Node) forwardPing(ctx context.Context, _ handoff.Request) (json.RawMessage, error) {
	var pong wire.Pong

	if err := n.cl.call(ctx, wire.PathPing, wire.Ping{Version: wire.Current}, &pong); err != nil {
		return nil, err
	}

	return json.Marshal(pong)
}

// serveRequests claims every pending request and answers it. It never holds the state lock, and a
// broker round trip happens outside any lock.
func (n *Node) serveRequests(ctx context.Context) {
	ids, err := handoff.Pending(n.opts.StateDir)
	if err != nil {
		n.warn(fmt.Sprintf("listing requests: %v", err))

		return
	}

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}

		req, err := handoff.Claim(n.opts.StateDir, id)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			n.warn(fmt.Sprintf("claiming request %s: %v", id, err))

			continue
		}

		rep := n.answer(ctx, req)

		if err := handoff.Answer(n.opts.StateDir, rep); err != nil {
			n.warn(fmt.Sprintf("answering request %s: %v", id, err))
		}
	}
}

// answer judges a claimed request and forwards it. An old request is refused before it is forwarded,
// so a retry from a long-gone CLI cannot grant anything.
func (n *Node) answer(ctx context.Context, req handoff.Request) handoff.Reply {
	rep := handoff.Reply{ID: req.ID, Requester: req.Requester}

	fail := func(code, msg string) handoff.Reply {
		rep.Error = &handoff.ReplyError{Code: code, Message: msg}

		return rep
	}

	switch {
	case handoff.Expired(req, n.opts.Now()):
		return fail(handoff.CodeExpired, handoff.ErrRequestExpired.Error())
	case req.Version > handoff.RequestVersion:
		return fail(handoff.CodeTooOld, fmt.Sprintf("%v: the node reads request version %d, this request is version %d",
			handoff.ErrNodeTooOld, handoff.RequestVersion, req.Version))
	}

	fwd, ok := n.forwrd[req.Kind]
	if !ok {
		return fail(handoff.CodeTooOld, fmt.Sprintf("%v: the node does not know the %q request", handoff.ErrNodeTooOld, req.Kind))
	}

	if req.Version < handoff.KindSince(req.Kind) {
		return fail(handoff.CodeTooOld, fmt.Sprintf("%v: the %q request needs request version %d, this request is version %d",
			handoff.ErrNodeTooOld, req.Kind, handoff.KindSince(req.Kind), req.Version))
	}

	body, err := fwd(ctx, req)
	if err != nil {
		return fail(classify(err), err.Error())
	}

	rep.Body = body

	return rep
}
