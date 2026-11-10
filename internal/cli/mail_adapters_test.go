package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/state"
)

// The CLI helper owns atomic claims across plugin reloads; session.prompt transport is exercised by
// the live opencode harness rather than mocked here.
func TestOpencodeMailPollClaimsOnlyItsSessionAndFailureCanRetry(t *testing.T) {
	w := newWorld(t)
	shell, tui, server := opencodeSetup(t, w)
	wantStatus(t, w.hook(tui, "presence", "ses_1", present(true)), exitcode.OK)
	id := state.NewID()
	if err := mailbox.Store(w.state, mailbox.Message{ID: id, ToSession: "ses_1", Kind: mailbox.KindMail, Body: "private"}); err != nil {
		t.Fatal(err)
	}
	other := state.NewID()
	if err := mailbox.Store(w.state, mailbox.Message{ID: other, ToSession: "ses_2", Kind: mailbox.KindMail, Body: "other-session-secret"}); err != nil {
		t.Fatal(err)
	}
	before := w.hook(server, "mail-reminder", "ses_1", nil)
	wantStatus(t, before, exitcode.OK)
	wantContains(t, "stdout", before.Stdout, "nitipan mail read")
	first := w.hook(server, "mail-poll", "ses_1", map[string]any{"sessions": []string{"ses_1", "ses_2"}})
	wantStatus(t, first, exitcode.OK)
	var rings []pluginRing
	if err := json.Unmarshal([]byte(first.Stdout), &rings); err != nil {
		t.Fatal(err)
	}
	if len(rings) != 1 || rings[0].SessionID != "ses_1" || len(rings[0].Keys) != 1 || rings[0].Keys[0] != mailbox.MailKey(id) || strings.Contains(first.Stdout, "private") || strings.Contains(first.Stdout, "other-session-secret") {
		t.Fatalf("ring output %q", first.Stdout)
	}
	reminder := w.hook(server, "mail-reminder", "ses_1", nil)
	wantStatus(t, reminder, exitcode.OK)
	wantContains(t, "stdout", reminder.Stdout, "nitipan mail read")
	wantAbsent(t, "stdout", reminder.Stdout, "private", "other-session-secret")
	second := w.hook(server, "mail-poll", "ses_1", nil)
	wantStatus(t, second, exitcode.OK)
	if strings.TrimSpace(second.Stdout) != "[]" {
		t.Fatalf("duplicate ring %q", second.Stdout)
	}
	wantStatus(t, w.hook(server, "mail-retry", "ses_1", map[string]any{"keys": rings[0].Keys}), exitcode.OK)
	retry := w.hook(server, "mail-poll", "ses_1", nil)
	wantStatus(t, retry, exitcode.OK)
	wantContains(t, "stdout", retry.Stdout, mailbox.MailKey(id))
	read := w.run(shell, "mail", "read")
	wantStatus(t, read, exitcode.OK)
	wantContains(t, "stdout", read.Stdout, "private")
	wantAbsent(t, "stdout", read.Stdout, "other-session-secret")
	pending, _, err := mailbox.Pending(w.state, []string{"ses_2"})
	if err != nil || len(pending) != 1 {
		t.Fatalf("other session mail = %+v, %v", pending, err)
	}
}

func TestCodexMailTurnRemindsWithoutConsumingOrClaimingAndStatusExplainsHomeMismatch(t *testing.T) {
	w := newWorld(t)
	shell, hook := codexSession(t, w)
	id := state.NewID()
	if err := mailbox.Store(w.state, mailbox.Message{ID: id, ToSession: "t1", Kind: mailbox.KindMail, Body: "private"}); err != nil {
		t.Fatal(err)
	}
	turn := w.hook(hook, "mail-turn", "t1", map[string]any{"hook_event_name": "PostToolUse"})
	wantStatus(t, turn, exitcode.OK)
	wantContains(t, "stdout", turn.Stdout, "PostToolUse", "additionalContext", "nitipan mail read")
	wantAbsent(t, "stdout", turn.Stdout, "private")
	regs, _, err := mailbox.Registrations(w.state)
	if err != nil || len(regs) != 1 {
		t.Fatalf("registrations %v, %v", regs, err)
	}
	if len(mailbox.Rung(w.state, regs[0].Identity)) != 0 {
		t.Fatal("turn reminder claimed a waking ring")
	}
	if err := handoff.WritePresence(w.state, handoff.Presence{CodexPath: "/trusted/codex", CodexHome: "/different/home"}); err != nil {
		t.Fatal(err)
	}
	status := w.run(shell, "mail", "status")
	wantStatus(t, status, exitcode.OK)
	wantContains(t, "stdout", status.Stdout, "CODEX_HOME differs", "mail waiting: 1")
	read := w.run(shell, "mail", "read")
	wantStatus(t, read, exitcode.OK)
	wantContains(t, "stdout", read.Stdout, "private")
}
