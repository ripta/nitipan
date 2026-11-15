package cli

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/fenceref"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
)

func (a *app) fenceCmd() *cobra.Command {
	fence := &cobra.Command{
		Use:   "fence",
		Short: "Manage the fence refs that push leaves on a remote",
	}

	del := &cobra.Command{
		Use:   "delete <remote> <key>",
		Short: "Delete one fence ref from a remote. For the user's own terminal only",
		Long: `Delete one fence ref from a remote. A fence ref is the record that lets a push be refused when another
session holds a newer lease. Deleting a live one lets a stale push land, so this command refuses any caller
that has agent variables or an agent above it.

The key is a fence ref name, <root-sha>/<encoded-path>, or a repo name as acquire prints it,
repo:<root-sha>#<path>. The ref is deleted only if it still holds the value this command read.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return a.wrapErr(a.runFenceDelete(args[0], args[1]))
		},
	}

	fence.AddCommand(del)

	return fence
}

func (a *app) runFenceDelete(remote, key string) error {
	res := a.resolver().Resolve()
	if res.Holder != nil || res.Failure != nil {
		return fmt.Errorf("%w: this command is for the user's own terminal. Ask the user", exitcode.ErrRefused)
	}

	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("%w: %q is not a remote", exitcode.ErrUsage, remote)
	}

	cwd, err := a.deps.Cwd()
	if err != nil {
		return fmt.Errorf("reading the working directory: %w", err)
	}

	name, err := fenceRefOf(key, firstParentRootIn(cwd))
	if err != nil {
		return err
	}

	g := a.newPushGit(true, cwd)
	url := pushURL(g, remote)

	view, err := g.lsRemote(remote, url)
	if err != nil {
		return err
	}

	sha, ok := view.Fences[name]
	if !ok {
		return fmt.Errorf("%w: %s holds no fence ref %s%s", exitcode.ErrNoSuchLease, remote, name, otherRoots(view, name))
	}

	if err := a.syncFences(g, view, true); err != nil {
		return err
	}

	cmd := exec.Command("git", "push", "--force-with-lease="+name+":"+sha, remote, ":"+name)
	cmd.Dir = cwd
	cmd.Env = append(keys.CleanGitEnv(os.Environ()), fenceDeleteEnv+"="+name)
	cmd.Stdout = a.deps.Stdout
	cmd.Stderr = a.deps.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deleting %s from %s: %w", name, remote, err)
	}

	_, _ = g.run("", nil, "update-ref", "-d", name)

	fmt.Fprintf(a.deps.Stdout, "deleted %s from %s (it held %s)\n", name, remote, sha)

	return nil
}

// firstParentRootIn returns a function that maps a root commit of the repo in dir to the first-parent
// root of its HEAD. Fence refs are named by the first-parent root, and a merged history has several roots.
func firstParentRootIn(dir string) func(string) string {
	return func(typed string) string {
		roots, first, err := (&rootset.Deriver{}).RootsOf(dir, "HEAD")
		if err != nil || !slices.Contains(roots, typed) {
			return typed
		}

		return first
	}
}

// otherRoots lists the fence refs of the same path under other roots, so a wrong root is easy to see.
func otherRoots(view remoteView, name string) string {
	ref, err := fenceref.Parse(name)
	if err != nil {
		return ""
	}

	var others []string

	for have := range view.Fences {
		if got, err := fenceref.Parse(have); err == nil && got.Path == ref.Path {
			others = append(others, have)
		}
	}

	slices.Sort(others)

	if len(others) == 0 {
		return ""
	}

	return ". The same path has fence refs under other roots: " + strings.Join(others, ", ")
}

// fenceRefOf reads the key a user typed. A repo name carries one root, and firstOf, when set, maps it to
// the first-parent root the fence ref is named by.
func fenceRefOf(key string, firstOf func(string) string) (string, error) {
	switch {
	case strings.HasPrefix(key, repoid.Prefix):
		n, err := repoid.Parse(key)
		if err != nil {
			return "", fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
		}

		root := n.Roots[0]
		if firstOf != nil {
			root = firstOf(root)
		}

		name, err := fenceref.Name(root, n.Path)
		if err != nil {
			return "", fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
		}

		return name, nil
	case strings.HasPrefix(key, fenceref.Namespace):
	default:
		key = fenceref.Namespace + key
	}

	if _, err := fenceref.Parse(key); err != nil {
		return "", fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	return key, nil
}
