package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/fenceplan"
	"github.com/space-armada/nitipan/internal/fenceref"
	"github.com/space-armada/nitipan/internal/gitfence"
	"github.com/space-armada/nitipan/internal/keys"
)

// pushAttempts bounds how often a push that lost a compare-and-swap on a fence ref is tried again.
// Two sibling pushes both refresh a shared ancestor, and one of them loses.
const pushAttempts = 5

var (
	errLostCAS  = errors.New("a fence ref changed during the push")
	errNoAtomic = errors.New("the remote does not support atomic push")
)

func (a *app) pushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <remote> [[+]<src>[:<dst>] ...]",
		Short: "Push branches with the fence refs that guard your broker leases",
		Long: `Push branches to a remote in one atomic git push that also updates the fence refs of the broker leases
you hold. A fence ref holds the highest token pushed for its key. A push whose token is lower than the one
stored is refused, and so is a push of a path that another session's lease covers.

With no refspec, push the current branch. A refspec is a branch name, <src>:<dst>, or :<dst> to delete a
branch, and a leading + forces the branch. Patterns are not supported.

A lease check renews your leases with the broker before the push. That checks the lease. It is not fencing.
You choose when a remote gets one with git config nitipan.<remote>.fence:

  unset  try the fence. When the host refuses --atomic or the fence ref, this one push gets a lease check
         and a warning. The next push tries the fence again, and nitipan records nothing
  true   always fence. A host refusal is an error
  false  never try the fence. Every push gets a lease check, because the key is false

The old key nitipan.<remote>.atomic=false acts as fence=false while fence is unset.

Run as your own terminal, with no agent above it, this is a plain git push.`,
		RunE: func(_ *cobra.Command, args []string) error {
			return a.wrapErr(a.runPush(args))
		},
	}
}

// pushSpec is one refspec as the user typed it.
type pushSpec struct {
	Text string
	Ref  string
	Src  string
}

func (a *app) runPush(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: want a remote", exitcode.ErrUsage)
	}

	remote, rawSpecs := args[0], args[1:]
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("%w: %q is not a remote", exitcode.ErrUsage, remote)
	}

	cwd, err := a.deps.Cwd()
	if err != nil {
		return fmt.Errorf("reading the working directory: %w", err)
	}

	res := a.resolver().Resolve()
	g := a.newPushGit(true, cwd)

	if res.Holder == nil && res.Failure == nil {
		return a.gitPush(cwd, append([]string{remote}, rawSpecs...), nil)
	}

	if res.Failure != nil {
		return &refusedError{text: fmt.Sprintf("nitipan could not identify the calling agent: %v. Ask the user", res.Failure)}
	}

	h := *res.Holder

	specs, err := a.resolveSpecs(g, rawSpecs)
	if err != nil {
		return err
	}

	setting, err := readFenceSetting(g, remote)
	if err != nil {
		return err
	}

	if setting.LegacyOrigin != "" {
		fmt.Fprintln(a.deps.Stderr, setting.legacyNotice())
	}

	url := pushURL(g, remote)

	if setting.Mode == fenceFalse {
		return a.pushWithoutFence(g, h, remote, url, specs, leaseWhy{Cause: setting.offCause()})
	}

	for range pushAttempts {
		err := a.pushOnce(g, h, remote, url, specs)
		if errors.Is(err, errLostCAS) {
			continue
		}

		if refusal, ok := hostRefusalOf(err, remote); ok {
			if setting.Mode == fenceTrue {
				return fmt.Errorf("%s, and %s is true, so the push is an error and not a lease check. "+
					"Set %s to false to skip the fence, or unset it to fall back to a lease check", refusal, fenceKey(remote), fenceKey(remote))
			}

			return a.pushWithoutFence(g, h, remote, url, specs, leaseWhy{
				Cause:    refusal,
				Fallback: true,
				Warning: fmt.Sprintf("nitipan: %s. This push gets a lease check and not fencing. Set %s to false to skip the fenced attempt, "+
					"or to true to make this refusal an error", refusal, fenceKey(remote)),
			})
		}

		return err
	}

	return fmt.Errorf("%w: other pushes kept updating the fence refs on %s, and this push lost the compare-and-swap %d times. "+
		"This is contention and not a lost lease. Push again", exitcode.ErrContention, remote, pushAttempts)
}

// resolveSpecs finds the commit each refspec pushes. With none given, the current branch goes.
func (a *app) resolveSpecs(g *pushGit, raw []string) ([]pushSpec, error) {
	if len(raw) == 0 {
		out, err := g.run("", nil, "symbolic-ref", "--short", "-q", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%w: HEAD is not on a branch. Name the branch to push", exitcode.ErrUsage)
		}

		raw = []string{strings.TrimSpace(string(out))}
	}

	specs := make([]pushSpec, 0, len(raw))

	for _, text := range raw {
		spec, err := parseSpec(text)
		if err != nil {
			return nil, err
		}

		if spec.Src != "" {
			out, err := g.run("", nil, "rev-parse", "--verify", "-q", spec.Src+"^{commit}")
			if err != nil {
				return nil, fmt.Errorf("%w: %q does not name a commit", exitcode.ErrUsage, spec.Src)
			}

			spec.Src = strings.TrimSpace(string(out))
		}

		specs = append(specs, spec)
	}

	return specs, nil
}

func parseSpec(text string) (pushSpec, error) {
	if strings.ContainsAny(text, "*") || strings.HasPrefix(strings.TrimPrefix(text, "+"), "-") {
		return pushSpec{}, fmt.Errorf("%w: %q is not a refspec nitipan push supports. Name branches, <src>:<dst>, or :<dst>",
			exitcode.ErrUsage, text)
	}

	body := strings.TrimPrefix(text, "+")
	src, dst, found := strings.Cut(body, ":")

	if !found {
		dst = src
	}

	if dst == "" {
		return pushSpec{}, fmt.Errorf("%w: %q has no destination", exitcode.ErrUsage, text)
	}

	ref := dst
	if !strings.HasPrefix(ref, "refs/") {
		ref = branchPrefix + ref
	}

	return pushSpec{Text: text, Ref: ref, Src: src}, nil
}

// pushURL is the URL a push to the remote goes to, which a pushurl or pushInsteadOf may change.
func pushURL(g *pushGit, remote string) string {
	out, err := g.run("", nil, "remote", "get-url", "--push", remote)
	if err != nil {
		return remote
	}

	if url := strings.TrimSpace(string(out)); url != "" {
		return url
	}

	return remote
}

func updatesOf(specs []pushSpec, view remoteView) []refUpdate {
	ups := make([]refUpdate, 0, len(specs))

	for _, s := range specs {
		ups = append(ups, refUpdate{Ref: s.Ref, Old: view.Heads[s.Ref], New: s.Src})
	}

	return ups
}

// pushOnce is one attempt of the fenced push.
func (a *app) pushOnce(g *pushGit, h adapter.Holder, remote, url string, specs []pushSpec) error {
	view, err := g.lsRemote(remote, url)
	if err != nil {
		return err
	}

	if err := a.syncFences(g, view, true); err != nil {
		return err
	}

	an, err := a.analyze(g, h, view, updatesOf(specs, view))
	if err != nil {
		return a.failClosedPush(err)
	}

	if len(an.Undecodable) > 0 {
		return &refusedError{text: undecodableText(view, an.Undecodable)}
	}

	if len(an.Plan.Refusals) > 0 {
		return &refusedError{text: strings.Join(a.refusalTexts(view, an.Plan), "\n")}
	}

	_, holderKey, err := a.pusherKeys(h)
	if err != nil {
		return err
	}

	refused, err := a.verifyUncovered(holderKey, an.Plan.Unverified)
	if len(refused) > 0 {
		return &refusedError{text: "git refused to push. " + strings.Join(refused, "\n")}
	}

	if err != nil {
		return err
	}

	args, err := a.fencedArgs(g, view, specs, an.Plan)
	if err != nil {
		return err
	}

	if err := a.gitPush(g.dir, args.git, args.classify); err != nil {
		return err
	}

	// The clone's fence namespace follows the remote, which keeps the hook's sync guard quiet for the
	// user's next plain push.
	for name, sha := range args.carried {
		if _, err := g.run("", nil, "update-ref", name, sha); err != nil {
			a.warn(fmt.Errorf("recording the fence ref %s locally: %w", name, err))
		}
	}

	return nil
}

// syncFences fetches the remote's fence objects when it has any, or when this clone still holds
// fence refs the remote lost.
func (a *app) syncFences(g *pushGit, view remoteView, always bool) error {
	local, err := g.run("", nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/nitipan/fence/")
	if err != nil {
		return fmt.Errorf("listing the local fence refs: %w", err)
	}

	if len(view.Fences) == 0 && len(bytes.TrimSpace(local)) == 0 {
		return nil
	}

	missing := false

	for _, sha := range view.Fences {
		missing = missing || !g.hasObject(sha)
	}

	if always || missing {
		if err := g.fetchFences(view.URL); err != nil {
			return err
		}
	}

	for _, sha := range view.Fences {
		if !g.hasObject(sha) {
			return fmt.Errorf("%w: the fence commit %s is not in this clone after the fetch, so a fence moved. Push again", errLostCAS, sha)
		}
	}

	return nil
}

func (a *app) failClosedPush(err error) error {
	var missing *gitfence.MissingObjectError
	if errors.As(err, &missing) {
		return fmt.Errorf("git refused to push, because the objects it needs to check are missing: %w", err)
	}

	return fmt.Errorf("git refused to push, because nitipan could not work out which paths it touches: %w", err)
}

type fencedPush struct {
	git      []string
	classify func(stdout, stderr string) error

	// carried maps each fence ref the push writes to its new commit.
	carried map[string]string
}

// fencedArgs writes the fence objects the plan calls for and builds the git push that carries them.
func (a *app) fencedArgs(g *pushGit, view remoteView, specs []pushSpec, plan fenceplan.Plan) (fencedPush, error) {
	remote := view.Name

	args := []string{"--porcelain"}

	if len(plan.Writes) > 0 {
		args = append(args, "--atomic")
	}

	var carried []string

	landed := map[string]string{}

	for _, w := range plan.Writes {
		var (
			sha string
			err error
		)

		if w.Refresh {
			sha, err = g.store.Refresh(w.Prior)
		} else {
			sha, err = g.store.Create(fenceref.Object{Token: w.Token, Path: fenceref.EncodePath(w.Ref.Path), Lease: w.Lease}, w.Prior)
		}

		if err != nil {
			return fencedPush{}, err
		}

		args = append(args, "--force-with-lease="+w.Name+":"+w.Prior)
		carried = append(carried, sha+":"+w.Name)
		landed[w.Name] = sha
	}

	args = append(args, remote)

	for _, s := range specs {
		args = append(args, s.Text)
	}

	args = append(args, carried...)

	classify := func(stdout, stderr string) error {
		failure := pushFailure{Remote: remote, Stdout: stdout, Stderr: stderr}

		if len(plan.Writes) > 0 {
			failure.Moved = func() (bool, error) { return fencesMoved(g, view, plan) }
		}

		return classifyPushFailure(failure)
	}

	return fencedPush{git: args, classify: classify, carried: landed}, nil
}

// fencesMoved reports whether a fence ref this push meant to update holds a value other than the one
// the push expected. Hosts word a lost compare-and-swap in their own ways, so the remote is asked.
func fencesMoved(g *pushGit, view remoteView, plan fenceplan.Plan) (bool, error) {
	now, err := g.lsRemote(view.Name, view.URL)
	if err != nil {
		return false, err
	}

	for _, w := range plan.Writes {
		if now.Fences[w.Name] != view.Fences[w.Name] {
			return true, nil
		}
	}

	return false, nil
}

// leaseWhy says why a push goes without fence refs, and what the push owes the user.
type leaseWhy struct {
	// Cause finishes "the push gets a lease check because ..." in every message about the lease check.
	Cause string

	// Warning is printed once the lease check passes, just before the push.
	Warning string

	// Fallback marks the push that follows a host refusal. It carries the variable that tells the hook so,
	// and a failure of it reports the host's refusal as well.
	Fallback bool
}

// hostRefusalOf says whether err is a host refusal of atomic push or of the fence ref, and words it as
// the cause of a lease check.
func hostRefusalOf(err error, remote string) (string, bool) {
	var refused *fenceRefusedError

	switch {
	case errors.Is(err, errNoAtomic):
		return fmt.Sprintf("%s does not support atomic push", remote), true
	case errors.As(err, &refused):
		return fmt.Sprintf("%s refused the fence ref %s: %s", remote, refused.Ref, refused.Reason), true
	}

	return "", false
}

// hookTooOldHint is what a push without fence refs adds to its refusal when the pre-push hook still
// demands fence refs. A hook installed by an older nitipan does not read the fence key and does not
// know the fallback push.
const hookTooOldHint = "The pre-push hook in this clone refuses a push with no fence refs. It may be older than this nitipan. " +
	"Run nitipan setup git --install, then push again"

// pushWithoutFence is the push without fence refs, as a lease check. The leases are renewed first, and
// the hook renews them again.
func (a *app) pushWithoutFence(g *pushGit, h adapter.Holder, remote, url string, specs []pushSpec, why leaseWhy) error {
	view, err := g.lsRemote(remote, url)
	if err != nil {
		return err
	}

	view.Fences = nil

	an, err := a.analyze(g, h, view, updatesOf(specs, view))
	if err != nil {
		return a.failClosedPush(err)
	}

	if err := a.leaseCheck(h, an, why.Cause); err != nil {
		return err
	}

	if why.Warning != "" {
		fmt.Fprintln(a.deps.Stderr, why.Warning)
	}

	args := []string{remote}
	for _, s := range specs {
		args = append(args, s.Text)
	}

	var env []string
	if why.Fallback {
		env = append(env, fenceFallbackEnv+"="+remote)
	}

	err = a.gitPush(g.dir, args, hookDemandsFences, env...)
	if err != nil && why.Fallback {
		return fmt.Errorf("%s, and the lease-check push that followed failed too: %w", why.Cause, err)
	}

	return err
}

// hookDemandsFences recognizes the refusal of a hook that wants fence refs on a push that has none.
func hookDemandsFences(_, stderr string) error {
	if strings.Contains(stderr, missingFencesPhrase) {
		return &refusedError{text: "git did not push. The reason is above. " + hookTooOldHint}
	}

	return nil
}

// withoutFenceMarks drops the caller's own copies of the fallback and notice variables. A push that
// nitipan runs sets them itself, so an inherited one must never reach git or the hook.
func withoutFenceMarks(env []string) []string {
	return slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, fenceFallbackEnv+"=") || strings.HasPrefix(kv, fenceNoticeEnv+"=")
	})
}

// gitPush runs git push in dir and shows its output. classify, when set, reads what git said on a
// failure. Stdout is copied as stderr is, because a porcelain push reports each ref there.
//
// The caller's own copies of the fallback and notice variables are dropped. Only the push that nitipan
// sanctions as a fallback carries the first, and it passes it in env. Every push here carries the second.
func (a *app) gitPush(dir string, args []string, classify func(stdout, stderr string) error, env ...string) error {
	cmd := exec.Command("git", append([]string{"push"}, args...)...)
	cmd.Dir = dir
	cmd.Env = withoutFenceMarks(keys.CleanGitEnv(os.Environ()))
	cmd.Env = append(cmd.Env, "LC_ALL=C", "LANGUAGE=C", fenceNoticeEnv+"=1")
	cmd.Env = append(cmd.Env, env...)

	var (
		seen    bytes.Buffer
		seenOut bytes.Buffer
	)

	cmd.Stdout = io.MultiWriter(a.deps.Stdout, &seenOut)
	cmd.Stderr = io.MultiWriter(a.deps.Stderr, &seen)

	err := cmd.Run()
	if err == nil {
		return nil
	}

	if classify != nil {
		if kind := classify(seenOut.String(), seen.String()); kind != nil {
			return kind
		}
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if strings.Contains(seen.String(), "nitipan:") {
			return &refusedError{text: "git did not push. The reason is above"}
		}

		return fmt.Errorf("git push failed with status %d", exit.ExitCode())
	}

	return fmt.Errorf("running git push: %w", err)
}
