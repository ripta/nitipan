package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// WhoSchemaVersion is the version of the JSON the who commands print.
const WhoSchemaVersion = "1.0"

// resourceLike matches the <namespace>:<name> shape of a resource lease.
var resourceLike = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*:[a-z0-9][a-z0-9_.-]*$`)

// whoOutput is the JSON the who commands print. Holders and Duplicate are set by who is.
type whoOutput struct {
	SchemaVersion string       `json:"schema_version"`
	Query         string       `json:"query"`
	Subject       string       `json:"subject"`
	Found         []wire.Found `json:"found"`
	Notes         []string     `json:"notes,omitempty"`
	Holders       int          `json:"holders,omitempty"`
	Duplicate     bool         `json:"duplicate,omitempty"`
}

// whoAmIOutput is the JSON that who am-i prints. The user label is empty for a node bound to nobody.
type whoAmIOutput struct {
	SchemaVersion string `json:"schema_version"`
	UserLabel     string `json:"user_label"`
}

func (a *app) whoCmd() *cobra.Command {
	who := &cobra.Command{
		Use:   "who",
		Short: "Find the holders of a name or a path, across machines",
		Long: `Ask the broker who holds leases. It needs the node and a broker, and it finds only sessions that
hold a lease.`,
	}

	holds := &cobra.Command{
		Use:   "holds <subject>",
		Short: "List every holder whose lease overlaps a repo name, path, resource, or qualified name",
		Long: `List every holder whose lease overlaps the subject, on every machine.

The subject is one of:
  repo:<root-sha>#<path>   a repo name, as acquire prints it
  <path>                   a path on this machine. Inside a checkout it becomes the repo name plus this
                           machine's path. Outside any checkout it searches only this machine's mirrored leases
  <namespace>:<name>       a resource name, matched on every machine
  <handle>@<label>         a qualified name, which lists that holder's leases

Each answer carries the holder's qualified name, reach, and state. A mirrored machine lease is mirrored,
stale when its node has gone quiet, or unknown when its repo entries could not be derived. Unknown never
means no overlap.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runWhoHolds(cmd, args[0]))
		},
	}

	is := &cobra.Command{
		Use:   "is <name>",
		Short: "List every holder with a name or handle",
		Long: `List every holder whose agent name or handle is exactly the name, on every machine. A name shared by
more than one holder is flagged and never resolved to one. Stop and ask the user which one they mean.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runWhoIs(cmd, args[0]))
		},
	}

	am := &cobra.Command{
		Use:   "am-i",
		Short: "Print this node's user label",
		Long: `Ask the broker for this node's user label. It needs the node and a broker.
The user label is what acting_for, cross-user mail, delegations, and grants use to name users.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.wrapErr(a.runWhoAmI(cmd))
		},
	}

	for _, c := range []*cobra.Command{holds, is, am} {
		c.Flags().Bool("json", false, "print JSON")
		who.AddCommand(c)
	}

	return who
}

func (a *app) runWhoHolds(cmd *cobra.Command, subject string) error {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	q, notes, err := a.holdsQuery(stateDir, subject)
	if err != nil {
		return err
	}

	ans, err := a.askWhoHolds(stateDir, q)
	if err != nil {
		return err
	}

	out := whoOutput{
		SchemaVersion: WhoSchemaVersion, Query: "holds", Subject: subject, Found: ans.Found, Notes: append(notes, ans.Notes...),
	}

	return a.printWho(cmd, out)
}

// askWhoHolds sends a who-holds query and returns the broker's answer.
func (a *app) askWhoHolds(stateDir string, q wire.WhoHolds) (wire.WhoHoldsAnswer, error) {
	if err := a.nodeFor(handoff.KindWhoHolds); err != nil {
		return wire.WhoHoldsAnswer{}, err
	}

	rep, err := a.roundTrip(stateDir, handoff.KindWhoHolds, state.NewID(), "", q)
	if err != nil {
		return wire.WhoHoldsAnswer{}, err
	}

	var ans wire.WhoHoldsAnswer
	if err := json.Unmarshal(rep.Body, &ans); err != nil {
		return wire.WhoHoldsAnswer{}, fmt.Errorf("decoding the who holds answer: %w", err)
	}

	return ans, nil
}

func (a *app) runWhoIs(cmd *cobra.Command, name string) error {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	if err := a.nodeFor(handoff.KindWhoIs); err != nil {
		return err
	}

	rep, err := a.roundTrip(stateDir, handoff.KindWhoIs, state.NewID(), "", wire.WhoIs{Name: name})
	if err != nil {
		return err
	}

	var ans wire.WhoIsAnswer
	if err := json.Unmarshal(rep.Body, &ans); err != nil {
		return fmt.Errorf("decoding the who is answer: %w", err)
	}

	out := whoOutput{
		SchemaVersion: WhoSchemaVersion, Query: "is", Subject: name, Found: ans.Found, Holders: ans.Holders, Duplicate: ans.Duplicate, Notes: ans.Notes,
	}

	return a.printWho(cmd, out)
}

// holdsQuery classifies the subject. The notes it returns say what the search could not cover.
func (a *app) holdsQuery(stateDir, subject string) (wire.WhoHolds, []string, error) {
	switch {
	case strings.HasPrefix(subject, repoid.Prefix):
		n, err := repoid.Parse(subject)
		if err != nil {
			return wire.WhoHolds{}, nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
		}

		return wire.WhoHolds{Names: []repoid.Name{n}}, nil, nil
	case !looksLikePath(subject) && resourceLike.MatchString(subject):
		return wire.WhoHolds{Resource: subject}, nil, nil
	case !looksLikePath(subject) && strings.Contains(subject, "@"):
		return wire.WhoHolds{Address: subject}, nil, nil
	}

	return a.pathQuery(stateDir, subject)
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~")
}

// pathQuery turns a local path into its repo name plus a machine-scoped path. A path outside any
// checkout, or in a checkout whose repo name cannot be derived, searches only this machine.
func (a *app) pathQuery(stateDir, subject string) (wire.WhoHolds, []string, error) {
	abs := subject

	if !filepath.IsAbs(abs) {
		cwd, err := a.deps.Cwd()
		if err != nil {
			return wire.WhoHolds{}, nil, fmt.Errorf("reading the working directory: %w", err)
		}

		abs = filepath.Join(cwd, abs)
	}

	if _, err := os.Lstat(abs); err != nil && !looksLikePath(subject) {
		return wire.WhoHolds{}, nil, fmt.Errorf("%w: %q is not a repo name, a resource name, a qualified name, or a path that exists. "+
			"To look a holder up by its name, run: nitipan who is %s", exitcode.ErrUsage, subject, subject)
	}

	dom, err := keys.NewCanonicalizer().Domain(abs, nil)
	if err != nil {
		return wire.WhoHolds{}, nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	key := dom.Checkout
	q := wire.WhoHolds{Path: &key}

	res, err := a.deriver(stateDir).Derive(key)

	switch {
	case err == nil:
		q.Names = rootset.Union(res)

		return q, nil, nil
	case errors.Is(err, rootset.ErrNotRepo):
		return q, []string{"The path is outside any checkout, so it has no repo name. Only this machine's mirrored leases were " +
			"searched. To find a holder elsewhere, look it up by its name: nitipan who is <name>"}, nil
	}

	return q, []string{fmt.Sprintf("The repo name of this path could not be derived (%v), so only this machine's mirrored leases "+
		"were searched. To find a holder elsewhere, look it up by its name: nitipan who is <name>", err)}, nil
}

func (a *app) printWho(cmd *cobra.Command, out whoOutput) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	w := a.deps.Stdout

	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")

		return enc.Encode(out)
	}

	if len(out.Found) == 0 {
		fmt.Fprintf(w, "nobody holds a lease that matches %s\n", out.Subject)
	}

	for _, f := range out.Found {
		fmt.Fprintf(w, "%s  %s  %s  lease %s\n", whoName(f), f.Reach, whoState(f), f.Lease)

		for _, n := range f.Names {
			fmt.Fprintf(w, "    name: %s\n", n)
		}

		for _, p := range f.Paths {
			fmt.Fprintf(w, "    path: %s\n", p)
		}

		if f.Resource != "" {
			fmt.Fprintf(w, "    resource: %s\n", f.Resource)
		}

		if f.Why != "" {
			fmt.Fprintf(w, "    could not be judged: %s\n", f.Why)
		}

		if f.Description != "" {
			fmt.Fprintf(w, "    %s\n", f.Description)
		}
	}

	if out.Duplicate {
		fmt.Fprintf(w, "\nwarning: %d different holders are named %q. Do not pick one. Stop and ask the user which one they mean\n",
			out.Holders, out.Subject)
	}

	for _, n := range out.Notes {
		fmt.Fprintf(w, "\nnote: %s\n", n)
	}

	return nil
}

func whoName(f wire.Found) string {
	if f.Qualified != "" {
		return f.Qualified
	}

	return "(unidentified holder)@" + f.Node
}

func whoState(f wire.Found) string {
	if f.Stale && f.State != wire.StateStale {
		return f.State + ", node gone quiet"
	}

	return f.State
}

func (a *app) runWhoAmI(cmd *cobra.Command) error {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	if err := a.nodeFor(handoff.KindWhoAmI); err != nil {
		return err
	}

	rep, err := a.roundTrip(stateDir, handoff.KindWhoAmI, state.NewID(), "", wire.WhoAmI{Version: wire.Current})
	if err != nil {
		return err
	}

	var ans wire.WhoAmIAnswer
	if err := json.Unmarshal(rep.Body, &ans); err != nil {
		return fmt.Errorf("decoding the whoami answer: %w", err)
	}

	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")

		return enc.Encode(whoAmIOutput{SchemaVersion: WhoSchemaVersion, UserLabel: ans.UserLabel})
	}

	if ans.UserLabel == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "This node is not bound to a user.")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "User label: %s\n", ans.UserLabel)
	}

	return nil
}
