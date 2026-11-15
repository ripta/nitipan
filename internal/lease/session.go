package lease

import (
	"fmt"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

// hostSync says which host the caller runs under now, and whether its leases may follow it.
type hostSync struct {
	PID   int
	Start record.ProcessStart

	// Move is true when a lease of kind session recorded under another host may be rewritten to this
	// one.
	Move bool
}

// needsMove reports whether any of the caller's leases of kind session names another host.
func (c *call) needsMove(h adapter.Holder) bool {
	for _, en := range c.mine {
		r := en.Rec
		if r.Liveness == record.LivenessSession && (r.Holder.PID != h.PID || !r.Holder.Start.Equal(h.Start)) {
			return true
		}
	}

	return false
}

// hostSync decides whether the caller's leases of kind session follow it to its current host. After
// a Codex host restart a resumed session runs under a new host, and its leases move with it only
// when the session's own writer-lock file exists. Without it the leases stay on the old host and
// read unknown, and the call says why.
func (c *call) hostSync(h adapter.Holder) hostSync {
	if c.hs != nil {
		return *c.hs
	}

	hs := hostSync{PID: h.PID, Start: h.Start}

	if h.Adapter.Liveness == record.LivenessSession && c.needsMove(h) && c.e.Sessions != nil {
		ok, why := c.e.Sessions.MayMove(h)
		hs.Move = ok

		if !ok {
			c.held = why
		}
	}

	c.hs = &hs

	return hs
}

// admit runs the gate for an agent of liveness kind session. Such an agent cannot acquire without a
// report from its hook or plugin.
func (e *Engine) admit(h adapter.Holder) error {
	if h.Adapter.Liveness != record.LivenessSession {
		return nil
	}

	if e.Sessions == nil {
		return fmt.Errorf("session liveness for %s is not configured", h.Adapter.Name)
	}

	return e.Sessions.Admit(h)
}

// reportMoves tells the caller what happened to its leases when its host changed. The notice is
// reported now and never stored, because the caller is here to read it.
func (c *call) reportMoves(h adapter.Holder, w *writes) {
	if h.Adapter.Liveness != record.LivenessSession {
		return
	}

	var (
		moved []string
		from  = -1
	)

	for _, en := range c.mine {
		if res := w.result(en); res.Holder.PID != en.Rec.Holder.PID || !res.Holder.Start.Equal(en.Rec.Holder.Start) {
			moved = append(moved, fmt.Sprintf("%s on %s", en.ID, en.pathList()))

			if from < 0 {
				from = en.Rec.Holder.PID
			}
		}
	}

	now := c.e.Now().UTC()

	if len(moved) > 0 {
		detail := fmt.Sprintf("session %s runs under a new %s host, pid %d instead of pid %d. At %s, your %s moved with it: %s",
			h.SessionID, h.Adapter.Name, h.PID, from, now.Format(timeLayout), Plural(len(moved), "lease", "leases"),
			strings.Join(moved, "; "))

		c.e.Notify.delivered(c.e.newNotice(h, record.NoticeMoved, detail, now))
	}

	if c.held != "" {
		detail := fmt.Sprintf("session %s runs under a new %s host, pid %d, but your leases stay on the old host and read "+
			"unknown: %s", h.SessionID, h.Adapter.Name, h.PID, c.held)

		c.e.Notify.delivered(c.e.newNotice(h, record.NoticeMoved, detail, now))
	}
}

const timeLayout = "2006-01-02 15:04:05 MST"

// newNotice builds a tombstone-shaped message addressed to a session holder. It reports something
// other than a break.
func (e *Engine) newNotice(h adapter.Holder, kind, detail string, at time.Time) *record.Tombstone {
	holder := h.Record()
	holder.Handle = e.handleFor(h)

	return &record.Tombstone{
		Envelope: record.Envelope{
			Version:       record.Current,
			ID:            e.NewID(),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      h.Adapter.Liveness,
			Machine:       e.Machine,
			Holder:        holder,
		},
		BrokenAt: at,
		Kind:     kind,
		Detail:   detail,
	}
}

// RegisterCodex records a Codex SessionStart under the lock. A session that comes back after an
// end gets a stored notice, which its next nitipan call delivers.
func (e *Engine) RegisterCodex(in session.RegisterInput) error {
	l, err := e.Dir.Lock()
	if err != nil {
		return err
	}
	defer func() { _ = l.Unlock() }()

	if err := l.FormatErr(); err != nil {
		return err
	}

	// The notice is published before the registration removes the end it reports, so a failed
	// publish leaves the end in place and loses nothing.
	in.BeforeClear = func(res *session.RegisterResult) error {
		if !e.Sessions.HoldsLease(session.AgentCodex, in.Session) {
			return nil
		}

		return e.writeRevived(l, in, res)
	}

	_, err = e.Sessions.RegisterCodex(l, in)

	return err
}

func (e *Engine) writeRevived(l *state.Locked, in session.RegisterInput, res *session.RegisterResult) error {
	entries, err := e.load()
	if err != nil {
		return err
	}

	h := adapter.Holder{Adapter: adapter.Codex(), PID: in.Host.PID, Start: in.Host.Start, SessionID: in.Session,
		User: in.Host.User}

	var meanwhile []string

	for _, en := range entries {
		if en.Rec == nil || (en.Rec.Holder.Agent == session.AgentCodex && en.Rec.Holder.SessionID == in.Session) {
			continue
		}

		if en.Rec.AcquiredAt.After(res.EndedSince) {
			meanwhile = append(meanwhile, fmt.Sprintf("%s on %s, held by %s", en.ID, en.pathList(),
				describeHolder(en.Rec.Holder)))
		}
	}

	now := e.Now().UTC()

	detail := fmt.Sprintf("Codex recorded the end of session %s at %s, and the session registered again at %s. "+
		"The end is cleared and your leases stand.", in.Session, res.EndedSince.Format(timeLayout), now.Format(timeLayout))

	if len(meanwhile) > 0 {
		detail += fmt.Sprintf(" While it was marked ended, other sessions claimed %s: %s.",
			Plural(len(meanwhile), "this lease", "these leases"), strings.Join(meanwhile, "; "))
	}

	t := e.newNotice(h, record.NoticeRevived, detail, now)

	data, err := t.Encode()
	if err != nil {
		return fmt.Errorf("encoding the notice for session %s: %w", in.Session, err)
	}

	if err := l.Publish(state.TombstonesDir, t.ID, data); err != nil {
		return fmt.Errorf("publishing the notice for session %s: %w", in.Session, err)
	}

	return nil
}

// SetOpencodeMark writes or removes an opencode presence or execution mark under the lock.
func (e *Engine) SetOpencodeMark(dir string, host adapter.Holder, sessionID string, on bool) error {
	l, err := e.Dir.Lock()
	if err != nil {
		return err
	}
	defer func() { _ = l.Unlock() }()

	if err := l.FormatErr(); err != nil {
		return err
	}

	return e.Sessions.SetMark(l, dir, host, sessionID, on)
}
