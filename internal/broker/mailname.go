package broker

import (
	"fmt"
	"strings"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/wire"
)

// MailNameDerivation versions the mail name derivation. A session record stores it, so a later
// derivation can keep old names valid.
const MailNameDerivation = 1

// mailDomain keeps a Claude Code mail name from equalling the lease handle of any holder.
const mailDomain = "nitipan-mail-name-v1:"

// Agent names the mail derivation knows. They match the adapter names.
const (
	agentClaude   = "claude"
	agentCodex    = "codex"
	agentOpencode = "opencode"
)

// DeriveMailName returns the four-word mail name of a session on a machine. Only the broker derives
// it, as it does for labels, so no node or sender can disagree about a name.
//
// Codex and opencode key their lease handle on the session ID alone, so their mail name is that
// handle: the lease handle's identity string, unchanged. A Claude Code handle is keyed by PID and process
// start, which changes on every resume. Its mail name is keyed by the session ID under its own domain
// separator, so it survives a resume.
func DeriveMailName(agent, sessionID, machineID string) (string, error) {
	switch agent {
	case agentClaude:
		return handle.Derive(mailDomain+agent+"\x00"+sessionID, machineID), nil
	case agentCodex, agentOpencode:
		return handle.Derive(strings.Join([]string{agent, sessionID}, "\x00"), machineID), nil
	default:
		return "", fmt.Errorf("agent %q has no mail name", agent)
	}
}

// mailAddress returns the <mail name>@<label> address of a holder, or why it has none. A holder with
// no session ID is not mailable, and never falls back to its handle.
func (b *Broker) mailAddress(h wire.Holder, label string) (addr, why string) {
	if h.SessionID == "" {
		return "", "the holder reports no session ID, so it has no mail address"
	}

	row, ok := b.store.Data().Labels[label]
	if !ok {
		return "", "the broker has no record of the holder's machine"
	}

	name, err := DeriveMailName(h.Agent, h.SessionID, row.MachineID)
	if err != nil {
		return "", err.Error()
	}

	return FormatAddress(name, label), ""
}

// holderMailName is mailAddress without the label, for matching a name a caller typed.
func (b *Broker) holderMailName(h wire.Holder, label string) string {
	addr, _ := b.mailAddress(h, label)
	name, _, _ := strings.Cut(addr, "@")

	return name
}
