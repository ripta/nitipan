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
		Short: "Pin the key with the fingerprint to the label, and discard its other pending keys",
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

	return []*cobra.Command{pending, approve, forget}
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
		_, err = fmt.Fprintf(out, "approved %s with key %s.\n\n%s\n", label, fingerprint, approvalWarning)

		return err
	}

	_, err = fmt.Fprintf(out, "recorded the approval of key %[2]s for %[1]s, but no key with that fingerprint has reached the broker.\n"+
		"The label pins the first key that presents it. Nothing is pinned until then. If you mistyped the fingerprint,\n"+
		"check it against the machine's own: nitipan node fingerprint\n\n%[3]s\n", label, fingerprint, approvalWarning)

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
