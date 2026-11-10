package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/node"
	"github.com/space-armada/nitipan/internal/wire"
)

// approvalWarning is printed with every approval. The broker grants mail only on a proof no session can
// make, and a command run on the broker host proves it only while no agent session runs as that OS user.
const approvalWarning = `Approval is a command run on the broker host. It is a proof only while no agent session runs as this OS
user on this host. A session that did could approve its own machine.`

func (a *app) brokerAdminCmds() []*cobra.Command {
	pending := &cobra.Command{
		Use:   "pending",
		Short: "List the labels waiting for approval, with the fingerprints of their keys",
		Long: `List every label that is not approved, and the key fingerprints waiting under it. Run this on the broker
host. Compare a fingerprint with the one the machine's node prints (nitipan node fingerprint) before you
approve it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runBrokerPending(cmd) },
	}

	approve := &cobra.Command{
		Use:   "approve <label> <fingerprint>",
		Short: "Pin the key with the fingerprint to the label, bind the label to the broker owner, and discard its other pending keys",
		Args:  cobra.ExactArgs(2),
		RunE:  func(cmd *cobra.Command, args []string) error { return a.runBrokerApprove(cmd, args[0], args[1]) },
	}

	forget := &cobra.Command{
		Use:   "forget <label>",
		Short: "Unpin a label whose machine was reinstalled or wiped",
		Long: `Unpin the label and drop its node's key. Mail still queued for the label waits for its next approved key and
is never handed to an unapproved one. Mail already stored on the machine and never read bounces to its
senders without its body.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runBrokerAdmin(cmd, broker.AdminPathForget, map[string]string{"label": args[0]},
				fmt.Sprintf("forgot %s. Approve the machine's new key with: nitipan broker approve %s <fingerprint>\n", args[0], args[0]))
		},
	}

	confirmUpgrade := &cobra.Command{
		Use:   "confirm-upgrade",
		Short: "Delete the kept previous store file, making a rollback impossible",
		Long: `Delete the broker.json.v1 file kept beside the store after the migration to store 2.0. After this runs,
a rollback to a broker that reads only store 1.x is impossible. The pre-upgrade keys and every key forgotten since the
upgrade are refused after a rollback.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runBrokerAdmin(cmd, broker.AdminPathConfirmUpgrade, nil,
				"confirmed the upgrade. A rollback is now impossible.\n")
		},
	}

	breakLease := &cobra.Command{
		Use:   "break <lease-id>",
		Short: "End any broker lease from the broker host",
		Long: `End a broker lease as the broker owner. This breaks any lease, including another user's live shared lease.
Run this on the broker host. The holder's node writes them a notice that names the broker owner.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runBrokerAdmin(cmd, broker.AdminPathBreak, map[string]string{"lease_id": args[0]},
				fmt.Sprintf("broke lease %s\n", args[0]))
		},
	}

	return []*cobra.Command{pending, approve, forget, confirmUpgrade, breakLease}
}

// adminClient talks to the broker's admin socket. No network path reaches it.
func (a *app) adminClient() (*http.Client, error) {
	path := a.deps.AdminSocket

	if path == "" {
		home, err := a.deps.UserHome()
		if err != nil {
			return nil, err
		}

		cfg, err := config.LoadBroker(home, os.Getuid())
		if err != nil {
			return nil, err
		}

		store := cfg.Store
		if store == "" {
			store = broker.DefaultStorePath(home)
		}

		path = broker.AdminSocketPath(store)
	}

	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
			if err != nil {
				return nil, fmt.Errorf("reaching the broker's admin socket %s. Run this on the broker host, as the OS user the broker "+
					"runs as, with the broker running: %w", filepath.Clean(path), err)
			}

			return conn, nil
		},
	}}, nil
}

func (a *app) adminCall(method, path string, body any) (*http.Response, error) {
	cl, err := a.adminClient()
	if err != nil {
		return nil, err
	}

	var rd io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding the request: %w", err)
		}

		rd = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, "http://broker-admin"+path, rd)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (a *app) runBrokerAdmin(cmd *cobra.Command, path string, body any, done string) error {
	resp, err := a.adminCall(http.MethodPost, path, body)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		var e wire.Error

		_ = json.NewDecoder(resp.Body).Decode(&e)

		return fmt.Errorf("the broker refused: %s", e.Message)
	}

	_, err = fmt.Fprint(cmd.OutOrStdout(), done)

	return err
}

func (a *app) runBrokerApprove(cmd *cobra.Command, label, fingerprint string) error {
	resp, err := a.adminCall(http.MethodPost, broker.AdminPathApprove, map[string]string{"label": label, "fingerprint": fingerprint})
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		var e wire.Error

		_ = json.NewDecoder(resp.Body).Decode(&e)

		return fmt.Errorf("the broker refused: %s", e.Message)
	}

	var ans broker.AdminApproved
	if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		return fmt.Errorf("decoding the approval answer: %w", err)
	}

	out := cmd.OutOrStdout()

	if ans.Pinned {
		bound := ""
		if ans.Owner != "" {
			bound = fmt.Sprintf(" It is bound to the broker owner, %s.", ans.Owner)
		}

		_, err = fmt.Fprintf(out, "approved %s with key %s.%s\n\n%s\n", label, fingerprint, bound, approvalWarning)

		return err
	}

	binds := ""
	if ans.Owner != "" {
		binds = fmt.Sprintf(", and binds to the broker owner, %s", ans.Owner)
	}

	_, err = fmt.Fprintf(out, "recorded the approval of key %[2]s for %[1]s, but no key with that fingerprint has reached the broker.\n"+
		"The label pins the first key that presents it%[4]s. Nothing is pinned until then. If you mistyped the fingerprint,\n"+
		"check it against the machine's own: nitipan node fingerprint\n\n%[3]s\n", label, fingerprint, approvalWarning, binds)

	return err
}

func (a *app) runBrokerPending(cmd *cobra.Command) error {
	resp, err := a.adminCall(http.MethodGet, broker.AdminPathPending, nil)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	var ans broker.AdminPending
	if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		return fmt.Errorf("decoding the pending list: %w", err)
	}

	out := cmd.OutOrStdout()

	if len(ans.Labels) == 0 {
		fmt.Fprintln(out, "every label is approved")

		return nil
	}

	for _, l := range ans.Labels {
		state := "pending approval"
		if l.Awaiting != "" {
			state = "approved in advance, waiting for key " + l.Awaiting
		}

		switch {
		case l.Pinned:
			state = "approved, with another key waiting"
		case l.Forgotten:
			state = "forgotten, waiting for a new key"
		}

		fmt.Fprintf(out, "%s  %s  %s  %s\n", l.Label, l.Host, l.User, state)

		for _, k := range l.Keys {
			fmt.Fprintf(out, "    key %s  first seen %s\n", k.Fingerprint, k.At.Format("2006-01-02 15:04:05 MST"))
		}
	}

	return nil
}

func (a *app) nodeFingerprintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fingerprint",
		Short: "Print the fingerprint of this node's key, for the broker's approve command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fp, err := a.nodeFingerprint()
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(cmd.OutOrStdout(), fp)

			return err
		},
	}
}

func (a *app) nodeFingerprint() (string, error) {
	dir, err := a.deps.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving the state directory: %w", err)
	}

	fp, err := node.Fingerprint(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	return fp, nil
}

func (a *app) nodePairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Print the fingerprint and a fresh pairing code for binding this node on the broker page",
		Long: `Print the fingerprint of this node's key and a fresh four-word pairing code. The code expires in 10 minutes.
Bind the node on the broker's sign-in page by entering the label and this code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := a.deps.UserHome()
			if err != nil {
				return err
			}

			cfg, err := config.LoadNode(home, os.Getuid())
			if err != nil {
				return err
			}

			dir, err := a.deps.StateDir()
			if err != nil {
				return fmt.Errorf("resolving the state directory: %w", err)
			}

			fp, err := node.Fingerprint(dir)
			if err != nil {
				return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
			}

			code := broker.GeneratePairingCode()
			hash := broker.HashPairingCode(code)

			cl := &http.Client{}
			reqBody := map[string]string{
				"pairing_hash": hash,
			}

			data, err := json.Marshal(reqBody)
			if err != nil {
				return fmt.Errorf("encoding the request: %w", err)
			}

			req, err := http.NewRequest(http.MethodPost, cfg.Broker+wire.PathPair, bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("building the request: %w", err)
			}

			key, err := node.LoadKey(dir)
			if err != nil {
				return err
			}

			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := cl.Do(req)
			if err != nil {
				return fmt.Errorf("sending pairing code to broker: %w", err)
			}

			defer resp.Body.Close()

			if resp.StatusCode/100 != 2 {
				return fmt.Errorf("broker refused the pairing code: %d", resp.StatusCode)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Fingerprint: %s\n", fp)
			fmt.Fprintf(out, "Pairing code: %s\n\n", code)
			fmt.Fprintln(out, "This code expires in 10 minutes. Bind the node on the broker's sign-in page with:")
			fmt.Fprintf(out, "  Label: (your machine's label from the broker)\n")
			fmt.Fprintf(out, "  Code: %s\n", code)

			return nil
		},
	}
}

func (a *app) nodeRekeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rekey",
		Short: "Generate a new instance key for a forgotten label",
		Long: `Remove the old instance key and generate a fresh random one. Use this after the broker owner ran
'nitipan broker forget' on your label. After rekeying, approve the new key on the broker host or bind
it through the sign-in page with a fresh pairing code from 'nitipan node pair'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := a.deps.StateDir()
			if err != nil {
				return fmt.Errorf("resolving the state directory: %w", err)
			}

			if err := node.Rekey(dir); err != nil {
				return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
			}

			fp, err := node.Fingerprint(dir)
			if err != nil {
				return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Generated a new instance key.")
			fmt.Fprintf(out, "New fingerprint: %s\n\n", fp)
			fmt.Fprintln(out, "Approve the new key with:")
			fmt.Fprintln(out, "  On the broker host: nitipan broker approve <label> "+fp)
			fmt.Fprintln(out, "  Or on the sign-in page: nitipan node pair, then bind with the code")

			return nil
		},
	}
}
