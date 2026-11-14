package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/session"
)

// DefaultMailPoll is how often the waiter checks that it is still the current waiter, that no scope
// has begun to refuse messages, and that its session has not been renamed by a /clear.
const DefaultMailPoll = 2 * time.Second

// wakeStatus is the exit status that wakes an idle Claude Code session from an asyncRewake hook. It
// shares usage's number, because Claude Code fixes it.
const wakeStatus = 2

// errWake is how the waiter says it rang. Execute turns it into wakeStatus without printing anything
// more, since the ring on standard error is the hook's output.
var errWake = errors.New("pending mail rung")

// Claude Code events a ring hook serves.
var ringEvents = []string{"UserPromptSubmit", "PostToolUse"}

func (a *app) hookMailCmds() []*cobra.Command {
	register := &cobra.Command{
		Use:    "mail-register",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(*cobra.Command, []string) error { return a.runMailRegister() },
	}

	wait := &cobra.Command{
		Use:    "mail-wait",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   func(*cobra.Command, []string) error { return a.runMailWait() },
	}

	ring := &cobra.Command{
		Use:    "mail-ring",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			event, _ := cmd.Flags().GetString("event")

			return a.runMailRing(event)
		},
	}

	ring.Flags().String("event", "", "the Claude Code event: UserPromptSubmit or PostToolUse")

	return []*cobra.Command{register, wait, ring}
}

// runMailRegister is the SessionStart hook. It registers the session's mailbox and prints nothing,
// since a SessionStart hook's output goes into the session's context.
func (a *app) runMailRegister() error {
	a.quietRing = true

	in, _ := a.readInput()

	h, err := a.holder()
	if err != nil {
		return err
	}

	if h.SessionID == "" && session.ValidSession(in.SessionID) {
		h.SessionID = in.SessionID
	}

	root, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	a.lockBudget = hookLockBudget

	_, err = a.registerMail(root, h)

	return err
}

// runMailWait is the waiter. It watches its session's mailbox and exits with status 2 and the ring on
// standard error when there is mail no wake path rang for. That wakes an idle session. It stands down
// when any readable Claude Code settings scope refuses cross-session messages, and when a newer waiter
// for the same session replaces it.
func (a *app) runMailWait() error {
	a.quietRing = true

	h, err := a.holder()
	if err != nil {
		return err
	}

	if why := a.crossSessionRefused(); why != "" {
		return nil
	}

	root, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	reg, err := a.registerMail(root, h)
	if err != nil {
		return err
	}

	self := a.deps.Procs.Self()
	start, _ := a.deps.Procs.Start(self)

	me := mailbox.WaiterPresence{Identity: reg.Identity, PID: self, Start: start, Machine: reg.Machine, ArmedAt: a.deps.Now().UTC()}
	// A presence file of a newer nitipan is not replaced. This session then has no waiter, and `mail status`
	// says why. The turn hooks and the next nitipan call still ring.
	if err := mailbox.Arm(root, me); errors.Is(err, mailbox.ErrNotRewritten) {
		return nil
	} else if err != nil {
		return err
	}

	defer mailbox.Disarm(root, me)

	ctx := a.deps.Context

	changes, err := a.deps.MailWatch(ctx, filepath.Join(root, mailbox.InboxDir))
	if err != nil {
		return fmt.Errorf("watching the mailbox: %w", err)
	}

	tick := time.NewTicker(a.deps.MailPoll)
	defer tick.Stop()

	for {
		if cur, ok := mailbox.Waiter(root, reg.Identity); !ok || cur.PID != self || !cur.Start.Equal(start) {
			return nil
		}

		if why := a.crossSessionRefused(); why != "" {
			return nil
		}

		if rung, err := a.ringUnrung(root, reg); err != nil || rung {
			if err != nil {
				return err
			}

			return errWake
		}

		select {
		case <-ctx.Done():
			return nil
		case <-changes:
		case <-tick.C:
		}
	}
}

// ringUnrung claims the messages no path rang for and prints the ring for them. It reports whether it
// rang.
func (a *app) ringUnrung(root string, reg mailbox.Registration) (bool, error) {
	if cur, err := mailbox.Find(root, reg.Identity); err == nil {
		reg = cur
	}

	msgs, _, err := mailbox.Pending(root, reg.SessionIDs)
	if err != nil {
		return false, err
	}

	keys := make([]string, len(msgs))
	for i, m := range msgs {
		keys[i] = mailbox.MailKey(m.ID)
	}

	rings := a.registrationRings(reg)
	keys = append(keys, lease.RingKeys(rings)...)

	// A ledger that cannot be rewritten still returns the keys it does not show as rung. A repeated ring
	// loses nothing.
	fresh, err := mailbox.Claim(root, reg.Identity, keys, a.deps.Now())
	if err != nil && !errors.Is(err, mailbox.ErrNotRewritten) {
		return false, err
	}

	if len(fresh) == 0 {
		return false, nil
	}

	fmt.Fprintln(a.deps.Stderr, mailbox.RingTextForKeys(fresh))

	return true, nil
}

// registrationRings lists the notice and request rings for the holder a registration stands for.
func (a *app) registrationRings(reg mailbox.Registration) []lease.Ring {
	h, ok := reg.Holder()
	if !ok {
		return nil
	}

	return a.leaseRings(h)
}

// runMailRing is the UserPromptSubmit and PostToolUse hook. It tells a busy session that mail is
// pending, through additionalContext. It never wakes anything and never records a ring, so it may
// repeat.
func (a *app) runMailRing(event string) error {
	a.quietRing = true

	if event != ringEvents[0] && event != ringEvents[1] {
		return fmt.Errorf("%w: --event is %s or %s", exitcode.ErrUsage, ringEvents[0], ringEvents[1])
	}

	h, err := a.holder()
	if err != nil {
		return err
	}

	root, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	n, _ := a.pendingFor(root, h)
	notices, requests := lease.CountRings(a.leaseRings(h))

	if n+notices+requests == 0 {
		return nil
	}

	out := struct {
		Specific struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}

	out.Specific.Event, out.Specific.Context = event, mailbox.PendingRingText(n, notices, requests)

	return json.NewEncoder(a.deps.Stdout).Encode(out)
}

// crossSessionRefused returns the file that sets crossSessionInbound to refuse, or the empty string.
// nitipan honors a user's or an org's refusal on both wake paths. It reads the scopes it can: user,
// project, local, and managed. A file it cannot read or parse says nothing.
func (a *app) crossSessionRefused() string {
	for _, path := range a.claudeSettingsFiles() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var s struct {
			CrossSessionInbound string `json:"crossSessionInbound"`
		}

		if json.Unmarshal(data, &s) == nil && s.CrossSessionInbound == "refuse" {
			return path
		}
	}

	return ""
}

// Default locations of Claude Code's managed settings.
var defaultManagedDirs = []string{"/Library/Application Support/ClaudeCode", "/etc/claude-code"}

func (a *app) claudeSettingsFiles() []string {
	var files []string

	if home, err := a.deps.UserHome(); err == nil {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}

	if cwd, err := a.deps.Cwd(); err == nil {
		files = append(files, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
	}

	dirs := a.deps.ManagedSettingsDirs
	if dirs == nil {
		dirs = defaultManagedDirs
	}

	for _, d := range dirs {
		files = append(files, filepath.Join(d, "managed-settings.json"))

		if drop, err := filepath.Glob(filepath.Join(d, "managed-settings.d", "*.json")); err == nil {
			files = append(files, drop...)
		}
	}

	return files
}
