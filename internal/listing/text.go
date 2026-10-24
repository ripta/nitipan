package listing

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// WriteText prints the leases for a person. The handle and the agent name are labeled apart: the
// handle is assigned by nitipan, and the agent name is whatever the session reported.
//
// A sub-lease prints indented under its parent, and a pending request under the lease it targets.
// Whatever cannot be placed prints at the top level, so a sub-lease whose parent is gone or dead
// reads as an ordinary lease.
func WriteText(w io.Writer, out Output) error {
	if len(out.Leases) == 0 && len(out.Requests) == 0 {
		if _, err := fmt.Fprintln(w, "no leases"); err != nil {
			return fmt.Errorf("writing the lease list: %w", err)
		}

		return writeSlotConfigPath(w, out)
	}

	groups := groupText(out)

	for i, g := range groups {
		if i > 0 {
			g = "\n" + g
		}

		if _, err := io.WriteString(w, g); err != nil {
			return fmt.Errorf("writing the lease list: %w", err)
		}
	}

	return writeSlotConfigPath(w, out)
}

// groupText renders one group per top-level lease, then one for the requests whose lease is not
// listed.
func groupText(out Output) []string {
	nested := nestedUnder(out.Leases)

	placed := map[string]bool{}
	for _, l := range out.Leases {
		placed[l.ID] = true
	}

	var groups []string

	for _, l := range out.Leases {
		if nested[l.ID] {
			continue
		}

		var b strings.Builder

		b.WriteString(leaseText(l, false))

		for _, r := range out.Requests {
			if r.Parent == l.ID {
				b.WriteString(requestText(r))
			}
		}

		for _, kid := range out.Leases {
			if nested[kid.ID] && slices.ContainsFunc(l.Delegated, func(d Delegation) bool { return d.ID == kid.ID }) {
				b.WriteString(indent(leaseText(kid, true)))
			}
		}

		groups = append(groups, b.String())
	}

	var b strings.Builder

	for _, r := range out.Requests {
		if !placed[r.Parent] {
			b.WriteString(requestText(r))
		}
	}

	if b.Len() > 0 {
		groups = append(groups, b.String())
	}

	return groups
}

// nestedUnder returns the IDs of the leases that print under a parent that is itself listed. A lease
// that has a parent link cannot be a parent, since sub-leases do not nest.
func nestedUnder(leases []Lease) map[string]bool {
	listed := map[string]bool{}
	for _, l := range leases {
		listed[l.ID] = true
	}

	nested := map[string]bool{}

	for _, l := range leases {
		if l.Parent != "" {
			continue
		}

		for _, d := range l.Delegated {
			if listed[d.ID] {
				nested[d.ID] = true
			}
		}
	}

	return nested
}

func indent(text string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "  " + line
		}
	}

	return strings.Join(lines, "")
}

func requestText(r Request) string {
	var b strings.Builder

	fmt.Fprintf(&b, "  request %s for lease %s, %s\n", r.ID, r.Parent, r.Liveness.Status)
	fmt.Fprintf(&b, "    wants: %s\n", strings.Join(r.Keys, ", "))
	fmt.Fprintf(&b, "    requester handle: %s, agent name: %s\n", orNone(r.Requester.Handle), orNone(r.Requester.Name))

	if r.Description != "" {
		fmt.Fprintf(&b, "    description: %s\n", r.Description)
	}

	return b.String()
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

// leaseText renders one lease. A sub-lease printed under its parent needs no parent line. One printed
// at the top level shows its stored link, so a reader sees it was a sub-lease whose parent is gone,
// dead, or not listed.
func leaseText(l Lease, underParent bool) string {
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

	if l.Parent != "" && !underParent {
		fmt.Fprintf(&b, "  parent link: %s\n", l.Parent)
	}

	if l.Owner != nil {
		fmt.Fprintf(&b, "  owner of the match: %s\n", yesNo(*l.Owner))
	}

	if len(l.GrantorOf) > 0 {
		fmt.Fprintf(&b, "  grantor of: %s\n", strings.Join(l.GrantorOf, ", "))
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

func yesNo(v bool) string {
	if v {
		return "yes"
	}

	return "no"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}
