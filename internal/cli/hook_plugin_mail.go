package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/session"
)

type pluginRing struct {
	SessionID string   `json:"session_id"`
	Ring      string   `json:"ring"`
	Keys      []string `json:"keys"`
}

func (a *app) pluginMail(in hookInput, host adapter.Holder, args []string, action string) error {
	if err := a.requireHost(host, session.AgentOpencode); err != nil {
		return err
	}
	if !adapter.OpencodeIsServe(args) {
		return errors.New("mail watcher calls come from the opencode service")
	}
	root, err := a.deps.StateDir()
	if err != nil {
		return err
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	ids := in.Sessions
	if len(ids) == 0 {
		ids = []string{in.SessionID}
	}
	if len(ids) > 256 {
		return errors.New("mail poll accepts at most 256 sessions")
	}
	out := make([]pluginRing, 0, len(ids))
	emitted := false
	defer func() {
		if emitted {
			return
		}
		for _, ring := range out {
			if len(ring.Keys) == 0 {
				continue
			}
			_ = mailbox.Unclaim(root, mailbox.RegistrationKey(host.Adapter.Name, host.PID, host.Start, ring.SessionID), ring.Keys)
		}
	}()
	var recipientErrors []error
	for _, id := range ids {
		host.SessionID = id
		ring, err := a.pluginMailRecipient(root, host, in, action, e.Checker)
		if err != nil {
			err = fmt.Errorf("mail for session %s: %w", id, err)
			fmt.Fprintln(a.deps.Stderr, err)
			recipientErrors = append(recipientErrors, err)
			continue
		}
		if ring.Ring != "" {
			out = append(out, ring)
		}
	}
	if action != "poll" && action != "reminder" {
		return errors.Join(recipientErrors...)
	}
	if err := json.NewEncoder(a.deps.Stdout).Encode(out); err != nil {
		return err
	}
	emitted = true
	return nil
}

func (a *app) pluginMailRecipient(root string, host adapter.Holder, in hookInput, action string, checker *liveness.Checker) (pluginRing, error) {
	if !session.ValidSession(host.SessionID) {
		return pluginRing{}, errors.New("mail poll has an invalid session ID")
	}
	r, err := a.registerMail(root, host)
	if err != nil {
		return pluginRing{}, err
	}
	w := mailbox.WaiterPresence{Identity: r.Identity, PID: host.PID, Start: host.Start, Machine: r.Machine, ArmedAt: a.deps.Now()}
	switch action {
	case "stop":
		mailbox.Disarm(root, w)
		return pluginRing{}, nil
	case "retry":
		return pluginRing{}, mailbox.Unclaim(root, r.Identity, in.Keys)
	}
	if err := mailbox.Arm(root, w); err != nil {
		return pluginRing{}, err
	}
	if !mailbox.Live(r, checker) {
		return pluginRing{}, nil
	}
	msgs, unknown, err := mailbox.Pending(root, r.SessionIDs)
	if err != nil {
		return pluginRing{}, err
	}
	if text := mailbox.UnknownText(unknown); text != "" {
		fmt.Fprintln(a.deps.Stderr, text)
	}
	if action == "reminder" {
		if len(msgs) == 0 {
			return pluginRing{}, nil
		}
		return pluginRing{SessionID: host.SessionID, Ring: mailbox.RingText(len(msgs))}, nil
	}
	keys := make([]string, len(msgs))
	for i, m := range msgs {
		keys[i] = mailbox.MailKey(m.ID)
	}
	fresh, err := mailbox.Claim(root, r.Identity, keys, a.deps.Now())
	if err != nil {
		return pluginRing{}, err
	}
	if len(fresh) == 0 {
		return pluginRing{}, nil
	}
	return pluginRing{SessionID: host.SessionID, Keys: fresh, Ring: mailbox.RingText(len(fresh))}, nil
}

func (a *app) runCodexMailTurn(in hookInput, host adapter.Holder, _ []string) error {
	if err := a.requireHost(host, session.AgentCodex); err != nil {
		return err
	}
	host.SessionID = in.SessionID
	root, err := a.deps.StateDir()
	if err != nil {
		return err
	}
	if _, err := a.registerMail(root, host); err != nil {
		return err
	}
	n, unknown := a.pendingFor(root, host)
	if n == 0 && len(unknown) == 0 {
		return nil
	}
	text := mailbox.RingText(n)
	if n == 0 {
		text = ""
	}
	text += mailbox.UnknownText(unknown)
	return json.NewEncoder(a.deps.Stdout).Encode(mailHookOutput{Specific: mailHookContext{Event: in.Event, Context: text}})
}

type mailHookContext struct {
	Event   string `json:"hookEventName"`
	Context string `json:"additionalContext"`
}

type mailHookOutput struct {
	Specific mailHookContext `json:"hookSpecificOutput"`
}
