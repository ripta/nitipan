package listing

import (
	"fmt"
	"io"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// WriteText prints the leases for a person. The handle and the agent name are labeled apart: the
// handle is assigned by nitipan, and the agent name is whatever the session reported.
func WriteText(w io.Writer, out Output) error {
	if len(out.Leases) == 0 {
		if _, err := fmt.Fprintln(w, "no leases"); err != nil {
			return fmt.Errorf("writing the lease list: %w", err)
		}

		return writeSlotConfigPath(w, out)
	}

	for i, l := range out.Leases {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return fmt.Errorf("writing the lease list: %w", err)
			}
		}

		if _, err := io.WriteString(w, leaseText(l)); err != nil {
			return fmt.Errorf("writing the lease list: %w", err)
		}
	}

	return writeSlotConfigPath(w, out)
}

func writeSlotConfigPath(w io.Writer, out Output) error {
	if out.SlotConfigPath == "" {
		return nil
	}

	if _, err := fmt.Fprintf(w, "\nslot counts are configured in %s\n", out.SlotConfigPath); err != nil {
		return fmt.Errorf("writing the lease list: %w", err)
	}

	return nil
}

func leaseText(l Lease) string {
	var b strings.Builder

	if !l.Usable {
		fmt.Fprintf(&b, "%s  unknown\n", l.File)
		fmt.Fprintf(&b, "  unusable record (%s); upgrade nitipan or ask the user\n", l.UnusableWhy)

		return b.String()
	}

	fmt.Fprintf(&b, "%s  %s\n", l.ID, l.Liveness.Status)

	if l.Liveness.Reason != "" && l.Liveness.Status != "alive" {
		fmt.Fprintf(&b, "  liveness: %s\n", l.Liveness.Reason)
	}

	if l.Hint != "" {
		fmt.Fprintf(&b, "  hint: %s\n", l.Hint)
	}

	if l.Domain != nil {
		fmt.Fprintf(&b, "  domain: %s\n", domainText(l))
	} else if l.Footprint != nil {
		fmt.Fprintf(&b, "  kind: %s, a record this nitipan reads by its footprint only\n", l.Kind)
	}

	if l.EffectiveSlots != nil {
		fmt.Fprintf(&b, "  slots: %d of %d in use%s\n", len(l.SlotHolders), *l.EffectiveSlots, slotHoldersText(l))
	}

	if h := l.Holder; h != nil {
		fmt.Fprintf(&b, "  handle: %s\n", orNone(h.Handle))
		fmt.Fprintf(&b, "  agent name: %s%s\n", orNone(h.Name), sharedText(l))
		fmt.Fprintf(&b, "  holder: %s", orNone(h.Agent))

		if h.SessionID != "" {
			fmt.Fprintf(&b, " session %s", h.SessionID)
		}

		fmt.Fprintf(&b, ", pid %d\n", h.PID)
	}

	if l.Mode != "" {
		fmt.Fprintf(&b, "  permission mode: %s\n", l.Mode)
	}

	if l.Description != "" {
		fmt.Fprintf(&b, "  description: %s\n", l.Description)
	}

	return b.String()
}

func slotHoldersText(l Lease) string {
	if len(l.SlotHolders) == 0 {
		return ""
	}

	return ", held by " + strings.Join(l.SlotHolders, ", ")
}

func domainText(l Lease) string {
	d := l.Domain

	if d.Kind == record.KindResource {
		return resourceText(l)
	}

	if len(d.Scopes) == 0 {
		return d.Checkout
	}

	return fmt.Sprintf("%s, scopes %s", d.Checkout, strings.Join(d.Scopes, ", "))
}

func resourceText(l Lease) string {
	text := "resource " + l.Domain.Name

	if l.Remote && l.Machine != nil {
		text += fmt.Sprintf(" (remote, on machine %s)", orNone(l.Machine.Host))
	}

	return text
}

func sharedText(l Lease) string {
	if !l.NameShared {
		return ""
	}

	return " (shared with another live holder)"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}
