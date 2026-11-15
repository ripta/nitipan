package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/fenceplan"
	"github.com/space-armada/nitipan/internal/fenceref"
	"github.com/space-armada/nitipan/internal/gitfence"
)

// fenceDeleteEnv names the one fence ref a `fence delete` run is allowed to delete. The pre-push hook
// lets only the user's own caller delete a fence ref, and only the ref named here.
const fenceDeleteEnv = "NITIPAN_FENCE_DELETE"

// missingFencesPhrase is what the hook says when a push lacks the fence refs it needs. The pusher reads
// the same words to tell that a hook older than itself refused its fence-less push. The words are specific
// enough that the hook's mirror-push refusal, which also mentions fence refs, does not match.
const missingFencesPhrase = "and it lacks the fence refs"

// pushedRef is one line of the pre-push hook's input.
type pushedRef struct {
	LocalRef  string
	LocalSHA  string
	RemoteRef string
	RemoteSHA string
}

func (p pushedRef) deletes() bool { return isZeroID(p.LocalSHA) }

func isZeroID(id string) bool { return strings.Trim(id, "0") == "" }

// hookPrePush is the command git's pre-push hook runs, through the script that `setup git --install`
// writes. It is the authority on fencing: it recomputes the fence refs the push needs and refuses a
// push that lacks them. It may write handoff request files, since a lease check asks the broker.
func (a *app) hookPrePush() *cobra.Command {
	return &cobra.Command{
		Use:    "pre-push <remote> <url>",
		Hidden: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("%w: unrecognized arguments", exitcode.ErrUsage)
			}

			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return a.runPrePush(args[0], args[1])
		},
	}
}

func (a *app) readPushedRefs() ([]pushedRef, error) {
	var data []byte

	if a.deps.Stdin != nil {
		var err error

		data, err = io.ReadAll(io.LimitReader(a.deps.Stdin, transactionLimit))
		if err != nil {
			return nil, fmt.Errorf("reading the pushed refs: %w", err)
		}
	}

	var out []pushedRef

	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		f := strings.Fields(line)
		if len(f) != 4 {
			return nil, fmt.Errorf("git refused the push, because nitipan could not read the line %q: want four fields", line)
		}

		out = append(out, pushedRef{LocalRef: f[0], LocalSHA: f[1], RemoteRef: f[2], RemoteSHA: f[3]})
	}

	return out, nil
}

func (a *app) runPrePush(remote, url string) error {
	pushed, err := a.readPushedRefs()
	if err != nil {
		return err
	}

	res := a.resolver().Resolve()
	user := res.Holder == nil && res.Failure == nil
	g := a.newPushGit(false, "")

	view, err := g.lsRemote(remote, url)
	if err != nil {
		return fmt.Errorf("%w: git refused the push, because nitipan could not read the fence refs of %s: %w",
			exitcode.ErrCouldNotVerify, remote, err)
	}

	if err := a.guardFenceSync(g, remote, view); err != nil {
		return err
	}

	if err := a.guardFenceNamespace(g, remote, pushed, user); err != nil {
		return err
	}

	if user {
		return nil
	}

	setting, err := readFenceSetting(g, remote)
	if err != nil {
		return fmt.Errorf("git refused the push, because nitipan could not read how %s is fenced: %w", remote, err)
	}

	if setting.LegacyOrigin != "" && a.deps.Getenv(fenceNoticeEnv) == "" {
		fmt.Fprintln(a.deps.Stderr, setting.legacyNotice())
	}

	var ups []refUpdate

	for _, p := range pushed {
		if !strings.HasPrefix(p.RemoteRef, branchPrefix) {
			continue
		}

		u := refUpdate{Ref: p.RemoteRef, New: p.LocalSHA}
		if p.deletes() {
			u.New = ""
		}

		if !isZeroID(p.RemoteSHA) {
			u.Old = p.RemoteSHA
		}

		ups = append(ups, u)
	}

	carried := carriedFences(pushed)

	if len(ups) == 0 && len(carried) == 0 {
		return nil
	}

	if res.Failure != nil {
		return &refusedError{text: fmt.Sprintf("git refused the push, because nitipan could not identify the calling agent: %v. "+
			"Ask the user", res.Failure)}
	}

	h := *res.Holder

	// A push with no fence refs is a lease check when the user turned fencing off for the remote. It is
	// also one when nitipan push sanctioned it as the fallback after a host refusal, which only an unset
	// mode allows. The variable picks the lease-check path and never skips the check.
	sanctioned := a.deps.Getenv(fenceFallbackEnv) == remote && setting.Mode == fenceUnset && len(carried) == 0

	if setting.Mode == fenceFalse || sanctioned {
		cause := "nitipan push chose a lease check for this push after " + remote + " refused the fence"
		if setting.Mode == fenceFalse {
			cause = setting.offCause()
		}

		return a.leaseCheckPush(g, h, remote, view, ups, len(carried) > 0, cause)
	}

	if err := a.syncFences(g, view, false); err != nil {
		return err
	}

	an, err := a.analyze(g, h, view, ups)
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

	return a.requireFences(g, remote, carried, an.Plan)
}

// carriedFences lists the fence refs a push creates or updates, by ref name.
func carriedFences(pushed []pushedRef) map[string]string {
	out := map[string]string{}

	for _, p := range pushed {
		if fenceref.InNamespace(p.RemoteRef) && !p.deletes() {
			out[p.RemoteRef] = p.LocalSHA
		}
	}

	return out
}

// requireFences refuses a push that does not carry the fence refs the plan calls for, and an agent's
// push that carries any fence ref the plan does not call for. An agent could otherwise push a fence ref
// with an arbitrary token and lock out every other holder. The user's own terminal is not held to the
// plan, since it holds no lease and is never refused for lack of a fence.
func (a *app) requireFences(g *pushGit, remote string, carried map[string]string, plan fenceplan.Plan) error {
	var (
		lacking []string
		extra   []string
	)

	planned := map[string]bool{}

	for _, w := range plan.Writes {
		planned[w.Name] = true

		sha, ok := carried[w.Name]
		if !ok {
			lacking = append(lacking, w.Name)

			continue
		}

		obj, err := g.store.Read(sha, w.Ref)
		if err != nil || obj.Token != w.Token {
			lacking = append(lacking, w.Name)
		}
	}

	for name := range carried {
		if !planned[name] {
			extra = append(extra, name)
		}
	}

	slices.Sort(extra)

	switch {
	case len(extra) > 0:
		return &refusedError{text: fmt.Sprintf("git refused to push to %s. The push carries fence refs that the leases you hold do not "+
			"call for: %s. Only nitipan push writes fence refs, and only for your own keys", remote, strings.Join(extra, ", "))}
	case len(lacking) > 0:
		return &refusedError{text: fmt.Sprintf("git refused to push to %s. The push touches paths that a broker lease fences, "+missingFencesPhrase+
			": %s. Push with: nitipan push %s <branch>", remote, strings.Join(lacking, ", "), remote)}
	}

	return nil
}

// guardFenceSync refuses any push to a remote that holds a fence ref this clone lacks, or holds at
// another value. It does not depend on seeing what the push deletes: git lists no ref that a pruning
// push, such as --mirror, deletes, and a flag can be abbreviated or hidden in an alias. A pruning push
// from a clone without the fence refs would delete them, so no push goes through until the clone has
// them. The hook fetches them, so pushing again passes.
func (a *app) guardFenceSync(g *pushGit, remote string, view remoteView) error {
	names := make([]string, 0, len(view.Fences))
	for name := range view.Fences {
		names = append(names, name)
	}

	slices.Sort(names)

	var differ []string

	for _, name := range names {
		out, err := g.run("", nil, "rev-parse", "--verify", "-q", name)
		if err != nil || strings.TrimSpace(string(out)) != view.Fences[name] {
			differ = append(differ, name)
		}
	}

	if len(differ) == 0 {
		return nil
	}

	fetched := "nitipan fetched them just now, so push again."
	if err := g.fetchFences(view.URL); err != nil {
		fetched = "nitipan could not fetch them: " + err.Error() + "."
	}

	return &refusedError{text: fmt.Sprintf("git refused to push to %s. This clone's fence refs differ from the remote's: %s. A push from a clone that "+
		"lacks them could delete them, for example with --mirror. %s To fetch them by hand: "+
		"git fetch %s '+%s:%s'", remote, strings.Join(differ, ", "), fetched, remote, fenceref.Pattern, fenceref.Pattern)}
}

// guardFenceNamespace applies the structural rule to every caller. A ref under the fence namespace may
// be created or fast-forwarded. A deletion is the user's alone, through `fence delete`. A rewind or a
// diverged update is refused, which covers `git push --mirror`.
func (a *app) guardFenceNamespace(g *pushGit, remote string, pushed []pushedRef, user bool) error {
	for _, p := range pushed {
		if !fenceref.InNamespace(p.RemoteRef) {
			continue
		}

		if _, err := fenceref.Parse(p.RemoteRef); err != nil {
			return &refusedError{text: fmt.Sprintf("git refused to push %s. It is under %s but is not a name nitipan writes", p.RemoteRef, fenceref.Namespace)}
		}

		switch {
		case p.deletes():
			if user && a.deps.Getenv(fenceDeleteEnv) == p.RemoteRef {
				continue
			}

			return &refusedError{text: mirrorText(remote, p.RemoteRef, "deletes it")}
		case isZeroID(p.RemoteSHA):
			continue
		}

		_, err := g.run("", nil, "merge-base", "--is-ancestor", p.RemoteSHA, p.LocalSHA)
		if err == nil {
			continue
		}

		if gitfence.ExitCode(err) == 1 {
			return &refusedError{text: mirrorText(remote, p.RemoteRef, "rewinds it or replaces it with a diverged commit")}
		}

		return &refusedError{text: mirrorText(remote, p.RemoteRef, "cannot be checked here, since this clone lacks the commit the remote holds")}
	}

	return nil
}

func mirrorText(remote, ref, what string) string {
	return fmt.Sprintf("git refused to push %s, because the push %s. A fence ref may only be created or fast-forwarded. "+
		"If this is a --mirror push from a clone that lacks the fence refs, fetch them first: "+
		"git fetch %s '+%s:%s'", ref, what, remote, fenceref.Pattern, fenceref.Pattern)
}

// leaseCheckPush is the hook's check on a push that carries no fence refs, and may not carry any.
func (a *app) leaseCheckPush(g *pushGit, h adapter.Holder, remote string, view remoteView, ups []refUpdate, carried bool, cause string) error {
	if carried {
		return &refusedError{text: fmt.Sprintf("git refused to push fence refs to %s, because %s, so it holds no fence refs from this clone",
			remote, cause)}
	}

	view.Fences = nil

	an, err := a.analyze(g, h, view, ups)
	if err != nil {
		return a.failClosedPush(err)
	}

	return a.leaseCheck(h, an, cause)
}
