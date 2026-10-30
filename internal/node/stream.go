package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

// stream holds the one long-lived GET, handles each event, and acks it. It returns when the
// connection ends. A quiet stream is cut after three keepalive intervals, which catches a half-open
// connection after sleep.
func (n *Node) stream(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := n.cl.request(sctx, http.MethodGet, wire.PathStream, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Del("Content-Type")

	resp, err := n.cl.http.Do(req)
	if err != nil {
		return fmt.Errorf("opening the stream: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return decodeError(wire.PathStream, resp)
	}

	idle := 3 * n.keep
	if idle <= 0 {
		idle = 3 * time.Minute
	}

	watchdog := time.AfterFunc(idle, cancel)
	defer watchdog.Stop()

	p := wire.NewParser(resp.Body)
	p.OnLine = func() { watchdog.Reset(idle) }

	for {
		sse, err := p.Next()
		if errors.Is(err, io.EOF) {
			return io.EOF
		}

		if err != nil {
			return fmt.Errorf("reading the stream: %w", err)
		}

		ev := wire.Event{ID: sse.ID, Kind: sse.Event, Data: json.RawMessage(sse.Data)}

		if err := n.handle(sctx, ev); err != nil {
			return err
		}
	}
}

// handle runs an event once and then acks it. An event the broker resends after a reconnect, because
// an ack was lost, is acked again without running twice. An event whose handler failed is not acked,
// so it stays queued.
func (n *Node) handle(ctx context.Context, ev wire.Event) error {
	if _, dup := n.seen[ev.ID]; !dup {
		if err := n.handleEvent(ev); err != nil {
			n.warn(fmt.Sprintf("handling event %s (%s): %v", ev.ID, ev.Kind, err))

			return nil
		}

		n.remember(ev.ID)
	}

	return n.cl.call(ctx, wire.PathAck, wire.Ack{Version: wire.Current, EventID: ev.ID}, nil)
}

func (n *Node) remember(id string) {
	n.seen[id] = struct{}{}
	n.seenQ = append(n.seenQ, id)

	if len(n.seenQ) > dedupeWindow {
		delete(n.seen, n.seenQ[0])
		n.seenQ = n.seenQ[1:]
	}
}
