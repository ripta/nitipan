package fenceplan

import (
	"slices"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/fenceref"
)

const (
	rootMain  = "1111111111111111111111111111111111111111"
	rootPages = "2222222222222222222222222222222222222222"
)

func fence(root, path, sha string, token uint64) Fence {
	name, err := fenceref.Name(root, path)
	if err != nil {
		panic(err)
	}

	return Fence{Name: name, SHA: sha, Ref: fenceref.Ref{Root: root, Path: path}, Token: token}
}

func unusable(f Fence, status Status) Fence {
	f.Status = status

	return f
}

func mainPush(paths ...string) RefPush {
	return RefPush{Ref: "refs/heads/main", Paths: paths, Roots: []string{rootMain}, WriteRoot: rootMain}
}

func key(path string, token uint64) Key {
	return Key{Roots: []string{rootMain}, Path: path, Token: token, Lease: "L-" + path}
}

type wantWrite struct {
	Path    string
	Prior   string
	Token   uint64
	Refresh bool
}

type planTest struct {
	Name  string
	Input Input

	Writes     []wantWrite
	Refusals   []Kind
	Unverified []string
}

var planTests = []planTest{
	{
		Name:   "a first push creates the fence of the key it holds",
		Input:  Input{Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)}},
		Writes: []wantWrite{{Path: "src", Token: 5}},
	},
	{
		Name: "an equal token still writes a fresh object so the compare-and-swap runs",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "src", "aaa", 5)},
		},
		Writes: []wantWrite{{Path: "src", Prior: "aaa", Token: 5}},
	},
	{
		Name: "nested: a stale holder of a child is refused by the new holder of the parent",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "", "r", 9)},
		},
		Refusals: []Kind{HigherToken},
	},
	{
		Name: "nested: the new holder of the parent raises the parent and the child below it",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("", 10)},
			Fences: []Fence{fence(rootMain, "", "r", 4), fence(rootMain, "src", "s", 3)},
		},
		Writes: []wantWrite{{Path: "", Prior: "r", Token: 10}, {Path: "src", Prior: "s", Token: 10}},
	},
	{
		Name: "sibling: the shared ancestor keeps its stored token and is refreshed",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "", "r", 4), fence(rootMain, "docs", "d", 9)},
		},
		Writes: []wantWrite{{Path: "src", Token: 5}, {Path: "", Prior: "r", Token: 4, Refresh: true}},
	},
	{
		Name: "sibling: another holder's higher token on a sibling key does not refuse",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "docs", "d", 99)},
		},
		Writes: []wantWrite{{Path: "src", Token: 5}},
	},
	{
		Name: "orphan branch: a holder of main is not blocked by a fence under another root",
		Input: Input{
			Pushes: []RefPush{{Ref: "refs/heads/gh-pages", Paths: []string{"index.html"}, Roots: []string{rootPages}, WriteRoot: rootPages}},
			Keys:   []Key{key("", 5)},
			Fences: []Fence{fence(rootMain, "", "r", 99)},
		},
	},
	{
		Name: "orphan branch: a holder of main that pushes gh-pages meets the fences under gh-pages' root",
		Input: Input{
			Pushes: []RefPush{{Ref: "refs/heads/gh-pages", Paths: []string{"index.html"}, Roots: []string{rootPages}, WriteRoot: rootPages}},
			Keys:   []Key{key("", 5)},
			Fences: []Fence{fence(rootPages, "", "p", 8)},
		},
		Unverified: []string{"index.html"},
	},
	{
		Name: "orphan branch: a lease over both roots writes its fence under the new value's first-parent root",
		Input: Input{
			Pushes: []RefPush{{Ref: "refs/heads/joined", Paths: []string{"a"}, Roots: []string{rootMain, rootPages}, WriteRoot: rootMain}},
			Keys:   []Key{{Roots: []string{rootMain, rootPages}, Path: "", Token: 5, Lease: "L"}},
			Fences: []Fence{fence(rootPages, "", "p", 3)},
		},
		Writes: []wantWrite{{Path: "", Token: 5}, {Path: "", Prior: "p", Token: 5}},
	},
	{
		Name: "a deletion is checked under the old value's roots and writes under its first-parent root",
		Input: Input{
			Pushes: []RefPush{{Ref: "refs/heads/old", Paths: []string{"src/a.go"}, Roots: []string{rootMain}, WriteRoot: rootMain}},
			Keys:   []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "src", "s", 7)},
		},
		Refusals: []Kind{HigherToken},
	},
	{
		Name: "a force-push that rewinds touches the paths it drops and is checked like a commit",
		Input: Input{
			Pushes: []RefPush{mainPush("docs/b.md")}, Keys: []Key{key("docs", 6)},
			Fences: []Fence{fence(rootMain, "docs", "d", 2)},
		},
		Writes: []wantWrite{{Path: "docs", Prior: "d", Token: 6}},
	},
	{
		Name:  "a lease without a token refuses",
		Input: Input{Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 0)}},

		Refusals: []Kind{NoToken},
	},
	{
		Name: "a fence of a newer major refuses the push that meets it",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{unusable(fence(rootMain, "", "r", 1), HigherMajor)},
		},
		Refusals: []Kind{FenceHigherMajor},
	},
	{
		Name: "a fence that cannot be read refuses the push that meets it",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{unusable(fence(rootMain, "src", "s", 1), Unusable)},
		},
		Refusals: []Kind{FenceUnusable},
	},
	{
		Name: "a fence of a newer major off the path is not met",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{unusable(fence(rootMain, "docs", "d", 1), HigherMajor)},
		},
		Writes: []wantWrite{{Path: "src", Token: 5}},
	},
	{
		Name: "a fence of a newer major refuses a lease-less pusher too",
		Input: Input{
			Pushes: []RefPush{mainPush("docs/b.md")},
			Fences: []Fence{unusable(fence(rootMain, "", "r", 1), HigherMajor)},
		},
		Refusals: []Kind{FenceHigherMajor},
	},
	{
		Name: "rule 6: a path outside every key with a fence above it needs the broker",
		Input: Input{
			Pushes: []RefPush{mainPush("docs/b.md")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "", "r", 4)},
		},
		Unverified: []string{"docs/b.md"},
	},
	{
		Name: "rule 6: a path with no fence at or above it never calls the broker",
		Input: Input{
			Pushes: []RefPush{mainPush("docs/b.md")}, Keys: []Key{key("src", 5)},
			Fences: []Fence{fence(rootMain, "src", "s", 4)},
		},
	},
	{
		Name:  "a repo nobody has fenced needs nothing from a lease-less pusher",
		Input: Input{Pushes: []RefPush{mainPush("docs/b.md")}},
	},
	{
		Name: "a fence is never written at a lower token than the one it replaces",
		Input: Input{
			Pushes: []RefPush{mainPush("src/x.go")}, Keys: []Key{key("", 5), key("src", 20)},
			Fences: []Fence{fence(rootMain, "", "r", 15)},
		},
		Writes: []wantWrite{{Path: "", Prior: "r", Token: 15, Refresh: true}, {Path: "src", Token: 20}},
	},
	{
		Name: "a key that cannot be named as a fence ref refuses the push",
		Input: Input{
			Pushes: []RefPush{mainPush(strings.Repeat("a", 300) + "/x")}, Keys: []Key{key(strings.Repeat("a", 300), 5)},
		},
		Refusals: []Kind{UnnamableKey},
	},
	{
		Name: "a fold-case lease covers a path spelled in another case, and so does the fence above it",
		Input: Input{
			Pushes: []RefPush{mainPush("SRC/a.go")},
			Keys:   []Key{{Roots: []string{rootMain}, Path: "src", Token: 3, Lease: "L", FoldCase: true}},
			Fences: []Fence{fence(rootMain, "src", "s", 9)},
		},
		Refusals: []Kind{HigherToken},
	},
	{
		Name: "the highest token among covering keys is the pusher's",
		Input: Input{
			Pushes: []RefPush{mainPush("src/a.go")}, Keys: []Key{key("src", 3), key("", 8)},
			Fences: []Fence{fence(rootMain, "src", "s", 7)},
		},
		Writes: []wantWrite{{Path: "src", Prior: "s", Token: 8}, {Path: "", Token: 8}},
	},
}

func TestPlanAppliesTheSixRules(t *testing.T) {
	for _, test := range planTests {
		t.Run(test.Name, func(t *testing.T) {
			plan := Make(test.Input)

			var got []wantWrite
			for _, w := range plan.Writes {
				got = append(got, wantWrite{Path: w.Ref.Path, Prior: w.Prior, Token: w.Token, Refresh: w.Refresh})
			}

			if len(plan.Refusals) == 0 && !sameWrites(got, test.Writes) {
				t.Errorf("writes = %+v, want %+v", got, test.Writes)
			}

			var kinds []Kind
			for _, r := range plan.Refusals {
				kinds = append(kinds, r.Kind)
			}

			if !slices.Equal(kinds, test.Refusals) {
				t.Errorf("refusals = %v, want %v", kinds, test.Refusals)
			}

			var paths []string
			for _, q := range plan.Unverified {
				paths = append(paths, q.Path)
			}

			if !slices.Equal(paths, test.Unverified) {
				t.Errorf("unverified = %v, want %v", paths, test.Unverified)
			}
		})
	}
}

func sameWrites(a, b []wantWrite) bool {
	if len(a) != len(b) {
		return false
	}

	for _, w := range a {
		if !slices.Contains(b, w) {
			return false
		}
	}

	return true
}

func TestAWriteNamesItsFenceRefUnderTheRootItWasPlannedFor(t *testing.T) {
	plan := Make(Input{
		Pushes: []RefPush{{Ref: "refs/heads/x", Paths: []string{"a"}, Roots: []string{rootMain, rootPages}, WriteRoot: rootPages}},
		Keys:   []Key{{Roots: []string{rootMain, rootPages}, Path: "", Token: 1, Lease: "L"}},
	})

	want, _ := fenceref.Name(rootPages, "")
	if len(plan.Writes) != 1 || plan.Writes[0].Name != want {
		t.Fatalf("writes = %+v, want one named %s", plan.Writes, want)
	}
}
