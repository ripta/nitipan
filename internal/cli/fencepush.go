package cli

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/fenceplan"
	"github.com/space-armada/nitipan/internal/fenceref"
	"github.com/space-armada/nitipan/internal/gitfence"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/wire"
)

// The push wrapper and the pre-push hook compute the required fence set from the same inputs with the
// same code in this file, so a wrapper that resolved refspecs differently from git is still caught.

// branchPrefix opens the refs a push fences. Tags and other refs pass.
const branchPrefix = "refs/heads/"

// refUpdate is one branch update of a push. Old is the value the remote advertised, empty for a
// creation. New is the commit pushed, empty for a deletion.
type refUpdate struct {
	Ref string
	Old string
	New string
}

// remoteView is what a remote advertised for this push.
type remoteView struct {
	// Name is how the remote was named on the command line, a configured remote or a URL.
	Name string
	URL  string

	Heads  map[string]string
	Fences map[string]string
}

// pushGit is git as one clone sees it.
type pushGit struct {
	run   gitfence.Runner
	dir   string
	fence *gitfence.Fence
	store fenceref.Store
}

func (a *app) newPushGit(clean bool, dir string) *pushGit {
	base := gitfence.ExecWith(clean)
	if clean {
		base = gitfence.ExecWithout(clean, fenceFallbackEnv, fenceNoticeEnv)
	}

	run := func(d string, stdin []byte, args ...string) ([]byte, error) {
		if d == "" {
			d = dir
		}

		return base(d, stdin, args...)
	}

	return &pushGit{
		run:   run,
		dir:   dir,
		fence: &gitfence.Fence{Git: run, GitIn: gitfence.ExecClean},
		store: fenceref.Store{Dir: dir, Clean: clean, Now: a.deps.Now},
	}
}

// lsRemote reads the advertised branches and fence refs.
func (g *pushGit) lsRemote(name, url string) (remoteView, error) {
	out, err := g.run("", nil, "ls-remote", url, "refs/heads/*", fenceref.Pattern)
	if err != nil {
		return remoteView{}, fmt.Errorf("listing the refs of %s: %w", name, err)
	}

	v := remoteView{Name: name, URL: url, Heads: map[string]string{}, Fences: map[string]string{}}

	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}

		switch {
		case fenceref.InNamespace(ref):
			v.Fences[ref] = sha
		case strings.HasPrefix(ref, branchPrefix):
			v.Heads[ref] = sha
		}
	}

	return v, nil
}

// fetchFences brings the remote's fence refs into the same local namespace, so their objects are
// here to read. It prunes local fence refs the remote no longer has, so a later --mirror cannot
// recreate a fence the user deleted.
func (g *pushGit) fetchFences(url string) error {
	_, err := g.run("", nil, "fetch", "--quiet", "--prune", "--no-tags", "--no-write-fetch-head", url,
		"+"+fenceref.Pattern+":"+fenceref.Pattern)
	if err != nil {
		return fmt.Errorf("fetching the fence refs: %w", err)
	}

	return nil
}

// hasObject reports whether this clone holds an object.
func (g *pushGit) hasObject(sha string) bool {
	_, err := g.run("", nil, "cat-file", "-e", sha)

	return err == nil
}

// analysis is the plan for one push, with the facts it came from.
type analysis struct {
	Fences      []fenceplan.Fence
	Undecodable []string
	Pushes      []fenceplan.RefPush
	Keys        []fenceplan.Key
	Plan        fenceplan.Plan
}

// loadFences reads every fence object the remote advertised.
func (g *pushGit) loadFences(view remoteView) (fences []fenceplan.Fence, undecodable []string) {
	names := make([]string, 0, len(view.Fences))
	for name := range view.Fences {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		ref, err := fenceref.Parse(name)
		if err != nil {
			undecodable = append(undecodable, name)

			continue
		}

		f := fenceplan.Fence{Name: name, SHA: view.Fences[name], Ref: ref}

		obj, err := g.store.Read(f.SHA, ref)

		switch {
		case errors.Is(err, fenceref.ErrHigherMajor):
			f.Status, f.Detail = fenceplan.HigherMajor, err.Error()
		case err != nil:
			f.Status, f.Detail = fenceplan.Unusable, err.Error()
		default:
			f.Token = obj.Token
		}

		fences = append(fences, f)
	}

	return fences, undecodable
}

// pusherKeys turns the holder's broker leases into the keys it pushes under.
func (a *app) pusherKeys(h adapter.Holder) ([]fenceplan.Key, string, error) {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolving the state directory: %w", err)
	}

	holderKey := handoff.HolderKey(h.Record(), h.Adapter.Liveness)

	scs, err := a.mySidecars(stateDir, holderKey)
	if err != nil {
		return nil, "", fmt.Errorf("reading the broker leases on file: %w", err)
	}

	var keys []fenceplan.Key

	for _, sc := range scs {
		for _, n := range sc.Entries {
			keys = append(keys, fenceplan.Key{Roots: n.Roots, Path: n.Path, Token: sc.Token, Lease: sc.ID, FoldCase: n.FoldCase, Normalize: n.Normalize})
		}
	}

	return keys, holderKey, nil
}

// analyze works out what a push touches and which fence refs it needs. A view with no fences is a
// push to a remote that cannot hold them.
func (a *app) analyze(g *pushGit, h adapter.Holder, view remoteView, ups []refUpdate) (*analysis, error) {
	an := &analysis{}

	an.Fences, an.Undecodable = g.loadFences(view)
	if len(an.Undecodable) > 0 {
		return an, nil
	}

	keys, _, err := a.pusherKeys(h)
	if err != nil {
		return nil, err
	}

	an.Keys = keys

	if len(an.Fences) == 0 && len(keys) == 0 {
		return an, nil
	}

	deriver := &rootset.Deriver{}
	dir := g.dir

	if dir == "" {
		dir = "."
	}

	for _, u := range ups {
		if !strings.HasPrefix(u.Ref, branchPrefix) {
			continue
		}

		paths, err := g.fence.RemoteTouched(u.Ref, u.Old, u.New, view.Heads)
		if err != nil {
			return nil, err
		}

		if len(paths) == 0 {
			continue
		}

		rp, err := rootsFor(deriver, dir, u, paths)
		if err != nil {
			return nil, err
		}

		an.Pushes = append(an.Pushes, rp)
	}

	an.Plan = fenceplan.Make(fenceplan.Input{Pushes: an.Pushes, Fences: an.Fences, Keys: keys})

	return an, nil
}

// rootsFor reads the roots of the old and new values of an update. A fence the pusher writes goes
// under the first-parent root of the new value, or of the old value for a deletion.
func rootsFor(d *rootset.Deriver, dir string, u refUpdate, paths []string) (fenceplan.RefPush, error) {
	rp := fenceplan.RefPush{Ref: u.Ref, Paths: paths}

	for _, sha := range []string{u.New, u.Old} {
		if sha == "" {
			continue
		}

		roots, first, err := d.RootsOf(dir, sha)
		if err != nil {
			return fenceplan.RefPush{}, fmt.Errorf("finding the root commits of %s: %w", sha, err)
		}

		rp.Roots = append(rp.Roots, roots...)

		if rp.WriteRoot == "" {
			rp.WriteRoot = first
		}
	}

	slices.Sort(rp.Roots)

	rp.Roots = slices.Compact(rp.Roots)

	return rp, nil
}

// verifyUncovered applies rule 6: a path the pusher holds no key for, with a fence above it, needs the
// broker to say no live lease covers it. It returns a refusal text for each path another holder
// covers. An answer it cannot get, or an unknown or stale one, is an error that wraps
// ErrCouldNotVerify.
func (a *app) verifyUncovered(holderKey string, queries []fenceplan.Query) ([]string, error) {
	if len(queries) == 0 {
		return nil, nil
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the state directory: %w", err)
	}

	var (
		refused []string
		unsure  []string
		said    = map[string]bool{}
	)

	for _, q := range queries {
		ans, err := a.askWhoHolds(stateDir, wire.WhoHolds{Names: []repoid.Name{repoid.New(q.Roots, q.Path, false, false)}})
		if err != nil {
			return refused, fmt.Errorf("%w: nitipan could not ask the broker whether a live lease covers %s: %w. This is not a lost lease. "+
				"Push again once the broker answers", exitcode.ErrCouldNotVerify, pathText(q.Path), err)
		}

		for _, f := range ans.Found {
			if f.Holder.Key == holderKey {
				continue
			}

			switch f.State {
			case wire.StateLive, wire.StateMirrored:
				if said[q.Path+"\x00"+holderText(f)] {
					continue
				}

				said[q.Path+"\x00"+holderText(f)] = true

				refused = append(refused, fmt.Sprintf("%s is covered by lease %s of %s, which is live. Ask that holder or the user",
					pathText(q.Path), f.Lease, holderText(f)))
			case wire.StateUnknown, wire.StateStale:
				unsure = append(unsure, fmt.Sprintf("lease %s of %s covers %s and is %s", f.Lease, holderText(f), pathText(q.Path), f.State))
			}
		}
	}

	if len(unsure) > 0 {
		return refused, fmt.Errorf("%w: the broker could not say whether %s. This is not a lost lease. Push again once it can",
			exitcode.ErrCouldNotVerify, strings.Join(unsure, "; "))
	}

	return refused, nil
}

func holderText(f wire.Found) string {
	if f.Qualified != "" {
		return f.Qualified
	}

	return "an unnamed holder on node " + f.Node
}

func pathText(p string) string {
	if p == "" {
		return "the whole repo"
	}

	return p
}

// leaseCheck is the push without fencing. It renews every lease of the pusher that covers a touched path
// and waits for the broker. It is a lease check, not fencing. cause says why the push has no fence, and
// leads the error so the host's reason is the first thing a user reads.
func (a *app) leaseCheck(h adapter.Holder, an *analysis, cause string) error {
	seen := map[string]bool{}

	var ids []string

	for _, rp := range an.Pushes {
		for _, p := range rp.Paths {
			for _, k := range fenceplan.Covering(an.Keys, rp, p) {
				if !seen[k.Lease] {
					seen[k.Lease] = true

					ids = append(ids, k.Lease)
				}
			}
		}
	}

	if len(ids) == 0 {
		return nil
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	local, err := a.deps.Machine()
	if err != nil {
		return fmt.Errorf("identifying this machine: %w", err)
	}

	out, err := a.renewNow(stateDir, holderEnv(h, local), ids)
	if err != nil {
		return fmt.Errorf("%s, so the push gets a lease check and not fencing. nitipan could not renew your lease with the broker, "+
			"which is not a lost lease: %w: %w. Push again once the broker answers", cause, exitcode.ErrCouldNotVerify, err)
	}

	var lost []string

	for _, r := range out.Results {
		switch r.Code {
		case wire.CodeRenewed, wire.CodeRevived:
			a.saveToken(stateDir, r.ID, r.Lease.Token)
		case wire.CodeTaken:
			lost = append(lost, fmt.Sprintf("lease %s was lost to %s", r.ID, r.TakenBy))
		case wire.CodeEnded:
			lost = append(lost, fmt.Sprintf("lease %s ended%s", r.ID, endedBy(r.EndedBy)))
		default:
			lost = append(lost, fmt.Sprintf("the broker has no record of lease %s", r.ID))
		}
	}

	if len(lost) > 0 {
		return &refusedError{text: "git refused the push by a lease check, which is not fencing, because " + cause + ". " +
			strings.Join(lost, "; ") + ". Stop pushing in this domain and report to the user"}
	}

	return nil
}

// refusalTexts describes the refusals of a plan, one text each. A higher token also tells the broker,
// and names the holder when the broker answers.
func (a *app) refusalTexts(view remoteView, plan fenceplan.Plan) []string {
	var out []string

	for _, r := range plan.Refusals {
		switch r.Kind {
		case fenceplan.HigherToken:
			out = append(out, a.higherTokenText(view, r))
		case fenceplan.NoToken:
			out = append(out, fmt.Sprintf("git refused to push %s. Lease %s covers %s and has no fence token, as a lease granted before "+
				"tokens existed. Release it, claim it again, and push again", r.Ref, r.Lease, pathText(r.Path)))
		case fenceplan.FenceHigherMajor:
			out = append(out, fmt.Sprintf("git refused to push %s. The fence ref %s on %s was written by a newer nitipan. Upgrade nitipan",
				r.Ref, r.Fence.Name, view.Name))
		case fenceplan.UnnamableKey:
			out = append(out, fmt.Sprintf("git refused to push %s. The lease %s covers %s, and no fence ref can be named for it: %s. "+
				"Report this to the user", r.Ref, r.Lease, pathText(r.Path), r.Detail))
		case fenceplan.FenceUnusable:
			out = append(out, fmt.Sprintf("git refused to push %s. The fence ref %s on %s cannot be used: %s. Report this to the user",
				r.Ref, r.Fence.Name, view.Name, r.Fence.Detail))
		}
	}

	return out
}

func (a *app) higherTokenText(view remoteView, r fenceplan.Refusal) string {
	var b strings.Builder

	fmt.Fprintf(&b, "git refused to push %s. The lease that covered %s was granted to another session: %s holds the fence token %d "+
		"and yours is %d.", r.Ref, pathText(r.Path), view.Name, r.Fence.Token, r.Own)

	verdict, err := a.reportFence(r.Fence.Token)

	switch {
	case err != nil:
		b.WriteString(" The broker could not be asked about it.")
	case verdict.Verdict == wire.FenceUnusable:
		fmt.Fprintf(&b, " That token is above any the broker could have issued, so the fence ref %s looks like the result of a broker "+
			"restore or of damage. Ask the user.", r.Fence.Name)

		return b.String()
	}

	if stateDir, err := a.deps.StateDir(); err == nil {
		name := repoid.New([]string{r.Fence.Ref.Root}, r.Fence.Ref.Path, false, false)

		if ans, err := a.askWhoHolds(stateDir, wire.WhoHolds{Names: []repoid.Name{name}}); err == nil {
			for _, f := range ans.Found {
				if f.State == wire.StateLive || f.State == wire.StateMirrored {
					fmt.Fprintf(&b, " The broker names %s as a holder of %s.", holderText(f), pathText(r.Fence.Ref.Path))
				}
			}
		}
	}

	b.WriteString(" Your commits are still on your branch. Stop protected actions in this domain and report to the user.")

	return b.String()
}

// undecodableText is the refusal for a fence ref name this binary cannot read.
func undecodableText(view remoteView, names []string) string {
	return fmt.Sprintf("git refused the push. %s holds fence refs whose names this nitipan cannot read: %s. A newer nitipan wrote them. "+
		"Upgrade nitipan", view.Name, strings.Join(names, ", "))
}
