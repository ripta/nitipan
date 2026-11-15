// Package fenceplan decides which fence refs a push must carry, and whether the push is refused.
//
// The rules are the six of the fencing design. The plan is a pure function of what was fetched and
// what the pusher holds, so `nitipan push` and the pre-push hook compute the same required set from
// the same inputs.
package fenceplan

import (
	"slices"

	"github.com/space-armada/nitipan/internal/fenceref"
	"github.com/space-armada/nitipan/internal/keys"
)

// Status says whether this binary can use a fetched fence ref.
type Status int

// Fence statuses.
const (
	Usable Status = iota

	// HigherMajor is a fence object written by a newer nitipan.
	HigherMajor

	// Unusable is a fence object that cannot be read, or whose key does not match its ref name.
	Unusable
)

// Fence is one fence ref of the remote, as fetched.
type Fence struct {
	Name   string
	SHA    string
	Ref    fenceref.Ref
	Token  uint64
	Status Status

	// Detail is the reason a fence is not usable.
	Detail string
}

// Key is one key the pusher holds. A broker lease with several repo names gives one Key for each.
type Key struct {
	Roots []string
	Path  string
	Token uint64
	Lease string

	// FoldCase and Normalize are the claiming volume's comparison rules, as a key carries them.
	FoldCase  bool
	Normalize bool
}

// RefPush is one branch update of the push, with what it touches.
type RefPush struct {
	// Ref is the remote ref being updated.
	Ref string

	// Paths are the repo-relative paths the update touches.
	Paths []string

	// Roots are the root commits of the old and new values. Only fence refs under one of them count.
	Roots []string

	// WriteRoot is the first-parent root of the new value, or of the old value for a deletion. A fence
	// the pusher creates goes under it.
	WriteRoot string
}

// Input is everything a plan reads.
type Input struct {
	Pushes []RefPush
	Fences []Fence
	Keys   []Key
}

// Write is a fence ref the push must carry.
type Write struct {
	Name string
	Ref  fenceref.Ref

	// Prior is the fetched value. It is empty for a fence being created.
	Prior string

	// Token is what the new fence object carries.
	Token uint64

	// Lease is the lease the token came from. It is empty for a refresh.
	Lease string

	// Refresh keeps the prior object. A ref strictly above every key the pusher holds keeps its stored
	// token, and still gets a new commit so the compare-and-swap runs.
	Refresh bool
}

// Kind names why a push is refused.
type Kind int

// Refusal kinds.
const (
	// HigherToken means a fence in the check set holds a token above the pusher's.
	HigherToken Kind = iota

	// NoToken means a lease that covers the path has no token on record.
	NoToken

	// FenceHigherMajor means a fence in the check set was written by a newer nitipan.
	FenceHigherMajor

	// FenceUnusable means a fence in the check set cannot be read.
	FenceUnusable

	// UnnamableKey means a key the pusher holds has no valid fence ref name, as when its encoded path is
	// longer than a ref component can be.
	UnnamableKey
)

// Refusal is one reason the push cannot go.
type Refusal struct {
	Kind Kind

	// Ref is the pushed ref, and Path the touched path.
	Ref  string
	Path string

	// Fence is the fence that refuses, for the kinds that name one.
	Fence Fence

	// Own is the pusher's token for the path.
	Own uint64

	// Lease is the covering lease with no token, or the lease whose key cannot be named.
	Lease string

	// Detail says why a key cannot be named.
	Detail string
}

// Query asks the broker whether a live lease covers a path the pusher holds no key for. See rule 6.
type Query struct {
	Ref   string
	Roots []string
	Path  string
}

// Plan is the outcome.
type Plan struct {
	Writes     []Write
	Refusals   []Refusal
	Unverified []Query
}

// Make applies the rules. A refused plan still lists the writes it would have made, but the caller
// sends none of them.
func Make(in Input) Plan {
	b := builder{in: in, writes: map[string]*Write{}}

	for _, rp := range in.Pushes {
		for _, path := range rp.Paths {
			b.path(rp, path)
		}
	}

	return b.plan()
}

type builder struct {
	in     Input
	writes map[string]*Write
	order  []string
	plan0  Plan
}

func (b *builder) plan() Plan {
	p := b.plan0

	for _, name := range b.order {
		p.Writes = append(p.Writes, *b.writes[name])
	}

	return p
}

// path applies rules 2 to 6 to one touched path of one pushed ref.
func (b *builder) path(rp RefPush, path string) {
	keys := b.coveringKeys(rp, path)
	check := b.checkSet(rp, path)

	if b.unreadable(rp, path, check) {
		return
	}

	if len(keys) == 0 {
		if len(check) > 0 {
			b.unverified(Query{Ref: rp.Ref, Roots: rp.Roots, Path: path})
		}

		return
	}

	own := uint64(0)

	for _, k := range keys {
		if k.Token == 0 {
			b.refuse(Refusal{Kind: NoToken, Ref: rp.Ref, Path: path, Lease: k.Lease})

			return
		}

		own = max(own, k.Token)
	}

	refused := false

	for _, f := range check {
		if f.Token > own {
			b.refuse(Refusal{Kind: HigherToken, Ref: rp.Ref, Path: path, Fence: f, Own: own})

			refused = true
		}
	}

	if refused {
		return
	}

	for _, k := range keys {
		b.want(rp, k)
	}

	for _, f := range check {
		b.touch(rp, f)
	}
}

// coveringKeys are the pusher's keys, of a repo this ref belongs to, that cover the path.
func (b *builder) coveringKeys(rp RefPush, path string) []Key {
	return Covering(b.in.Keys, rp, path)
}

// Covering returns the keys, of a repo the pushed ref belongs to, that cover the path.
func Covering(keys []Key, rp RefPush, path string) []Key {
	var out []Key

	for _, k := range keys {
		if sharesRoot(k.Roots, rp.Roots) && within(path, k.Path, k.FoldCase, k.Normalize) {
			out = append(out, k)
		}
	}

	return out
}

// checkSet is every fence ref whose key equals the path or is an ancestor of it, under a root of the
// pushed ref's old or new value.
func (b *builder) checkSet(rp RefPush, path string) []Fence {
	var out []Fence

	fold, norm := b.sensitivity(rp)

	for _, f := range b.in.Fences {
		if slices.Contains(rp.Roots, f.Ref.Root) && within(path, f.Ref.Path, fold, norm) {
			out = append(out, f)
		}
	}

	return out
}

// sensitivity is how a fence key is compared with a touched path. A fence ref records no volume rules,
// so the pusher's own rules apply. The more insensitive reading only adds names to the check set, never
// hides one, as ADR-03 says of keys on different volumes.
func (b *builder) sensitivity(rp RefPush) (fold, norm bool) {
	for _, k := range b.in.Keys {
		if sharesRoot(k.Roots, rp.Roots) {
			fold, norm = fold || k.FoldCase, norm || k.Normalize
		}
	}

	return fold, norm
}

func (b *builder) unreadable(rp RefPush, path string, check []Fence) bool {
	found := false

	for _, f := range check {
		switch f.Status {
		case HigherMajor:
			b.refuse(Refusal{Kind: FenceHigherMajor, Ref: rp.Ref, Path: path, Fence: f})
		case Unusable:
			b.refuse(Refusal{Kind: FenceUnusable, Ref: rp.Ref, Path: path, Fence: f})
		default:
			continue
		}

		found = true
	}

	return found
}

func (b *builder) refuse(r Refusal) {
	for _, have := range b.plan0.Refusals {
		if have.Kind == r.Kind && have.Ref == r.Ref && have.Path == r.Path && have.Fence.Name == r.Fence.Name {
			return
		}
	}

	b.plan0.Refusals = append(b.plan0.Refusals, r)
}

func (b *builder) unverified(q Query) {
	for _, have := range b.plan0.Unverified {
		if have.Ref == q.Ref && have.Path == q.Path {
			return
		}
	}

	b.plan0.Unverified = append(b.plan0.Unverified, q)
}

// tokenAt is the pusher's token for a fence key: the highest token among its keys that the key lies at
// or below. Zero means the key lies strictly above every key the pusher holds.
func (b *builder) tokenAt(rp RefPush, path string) (token uint64, lease string) {
	for _, k := range b.in.Keys {
		if sharesRoot(k.Roots, rp.Roots) && within(path, k.Path, k.FoldCase, k.Normalize) && k.Token >= token {
			token, lease = k.Token, k.Lease
		}
	}

	return token, lease
}

// want makes sure the pusher's own fence for a key it holds is written. It creates the ref when none
// exists, under the push's write root. A key with no valid ref name refuses the push.
func (b *builder) want(rp RefPush, k Key) {
	name, err := fenceref.Name(rp.WriteRoot, k.Path)
	if err != nil {
		b.refuse(Refusal{Kind: UnnamableKey, Ref: rp.Ref, Path: k.Path, Lease: k.Lease, Detail: err.Error()})

		return
	}

	token, _ := b.tokenAt(rp, k.Path)
	prior := b.fetched(name)

	b.write(Write{Name: name, Ref: fenceref.Ref{Root: rp.WriteRoot, Path: k.Path}, Prior: prior.SHA, Token: token, Lease: k.Lease}, prior)
}

// touch handles an existing fence ref of the check set: rule 4 for a ref at or below one of the
// pusher's keys, and rule 5 for a ref strictly above every one.
func (b *builder) touch(rp RefPush, f Fence) {
	token, lease := b.tokenAt(rp, f.Ref.Path)
	if token == 0 {
		b.put(Write{Name: f.Name, Ref: f.Ref, Prior: f.SHA, Token: f.Token, Refresh: true})

		return
	}

	b.write(Write{Name: f.Name, Ref: f.Ref, Prior: f.SHA, Token: token, Lease: lease}, f)
}

// write adds a write that carries the pusher's token. A fence never falls to a lower token than the one
// it replaces: the check compares the highest token over the touched path, and a ref under a lower key
// of the pusher can hold more than that key's token. Such a ref is refreshed and keeps its stored token.
func (b *builder) write(w Write, prior Fence) {
	if prior.Token > w.Token {
		w.Token, w.Lease, w.Refresh = prior.Token, "", true
	}

	b.put(w)
}

// put adds a write. A write that carries the pusher's token wins over a refresh of the same ref, and
// the higher of two tokens wins.
func (b *builder) put(w Write) {
	have, ok := b.writes[w.Name]
	if !ok {
		b.writes[w.Name] = &w
		b.order = append(b.order, w.Name)

		return
	}

	switch {
	case have.Refresh && !w.Refresh:
		*have = w
	case !have.Refresh && !w.Refresh && w.Token > have.Token:
		*have = w
	}
}

func (b *builder) fetched(name string) Fence {
	for _, f := range b.in.Fences {
		if f.Name == name {
			return f
		}
	}

	return Fence{}
}

func sharesRoot(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}

	return false
}

// within reports whether path equals key or lies below it, by whole components, under ADR-03's one
// containment rule. The empty key is the repo root and covers every path. A git checkout boundary needs
// no finder here, since a repo-relative path stays inside one repo.
func within(path, key string, fold, normalize bool) bool {
	return keys.Contains(
		keys.Key{Path: "/" + key, FoldCase: fold, Normalize: normalize},
		keys.Key{Path: "/" + path, FoldCase: fold, Normalize: normalize},
		nil,
	)
}
