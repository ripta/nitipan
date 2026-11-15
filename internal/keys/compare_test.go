package keys

import (
	"testing"
)

type fakeBoundaries map[string]bool

func (f fakeBoundaries) IsBoundary(dir string) bool {
	return f[dir]
}

type overlapTest struct {
	Name       string
	A          Key
	B          Key
	Boundaries BoundaryFinder
	Want       bool
}

var overlapTests = []overlapTest{
	{
		Name: "equal",
		A:    Key{Path: "/a/b"},
		B:    Key{Path: "/a/b"},
		Want: true,
	},
	{
		Name: "ancestor",
		A:    Key{Path: "/a/b"},
		B:    Key{Path: "/a/b/c"},
		Want: true,
	},
	{
		Name: "descendant",
		A:    Key{Path: "/a/b/c"},
		B:    Key{Path: "/a/b"},
		Want: true,
	},
	{
		Name: "sibling",
		A:    Key{Path: "/a/b"},
		B:    Key{Path: "/a/c"},
		Want: false,
	},
	{
		Name: "shared prefix is not an ancestor",
		A:    Key{Path: "/a/b"},
		B:    Key{Path: "/a/bc"},
		Want: false,
	},
	{
		Name: "root contains everything",
		A:    Key{Path: "/"},
		B:    Key{Path: "/a"},
		Want: true,
	},
	{
		Name: "case differs on a sensitive volume",
		A:    Key{Path: "/a/Docs"},
		B:    Key{Path: "/a/docs"},
		Want: false,
	},
	{
		Name: "case differs on an insensitive volume",
		A:    Key{Path: "/a/Docs", FoldCase: true},
		B:    Key{Path: "/a/docs/x", FoldCase: true},
		Want: true,
	},
	{
		Name: "normalization differs on an insensitive volume",
		A:    Key{Path: "/a/café", Normalize: true},
		B:    Key{Path: "/a/café/x", Normalize: true},
		Want: true,
	},
	{
		Name: "normalization differs on a sensitive volume",
		A:    Key{Path: "/a/café"},
		B:    Key{Path: "/a/café"},
		Want: false,
	},
	{
		Name:       "boundary strictly below the ancestor separates them",
		A:          Key{Path: "/repo"},
		B:          Key{Path: "/repo/w/src"},
		Boundaries: fakeBoundaries{"/repo/w": true},
		Want:       false,
	},
	{
		Name:       "boundary at the descendant separates them",
		A:          Key{Path: "/repo"},
		B:          Key{Path: "/repo/w"},
		Boundaries: fakeBoundaries{"/repo/w": true},
		Want:       false,
	},
	{
		Name:       "boundary at the ancestor does not separate them",
		A:          Key{Path: "/repo/w"},
		B:          Key{Path: "/repo/w/src"},
		Boundaries: fakeBoundaries{"/repo/w": true},
		Want:       true,
	},
	{
		Name:       "equal keys at a boundary still overlap",
		A:          Key{Path: "/repo/w"},
		B:          Key{Path: "/repo/w"},
		Boundaries: fakeBoundaries{"/repo/w": true},
		Want:       true,
	},
	{
		Name:       "boundary beside the path is ignored",
		A:          Key{Path: "/repo"},
		B:          Key{Path: "/repo/src"},
		Boundaries: fakeBoundaries{"/repo/other": true},
		Want:       true,
	},
}

func TestOverlap(t *testing.T) {
	for _, test := range overlapTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := Overlap(test.A, test.B, test.Boundaries); got != test.Want {
				t.Errorf("Overlap(%v, %v) = %v, want %v", test.A, test.B, got, test.Want)
			}

			if got := Overlap(test.B, test.A, test.Boundaries); got != test.Want {
				t.Errorf("Overlap(%v, %v) = %v, want %v", test.B, test.A, got, test.Want)
			}
		})
	}
}

type coveredTest struct {
	Name       string
	Key        Key
	Held       []Key
	Boundaries BoundaryFinder
	Want       bool
}

var coveredTests = []coveredTest{
	{
		Name: "covered by an ancestor",
		Key:  Key{Path: "/repo/src"},
		Held: []Key{{Path: "/repo"}},
		Want: true,
	},
	{
		Name: "covered by an equal key among several",
		Key:  Key{Path: "/repo/src"},
		Held: []Key{{Path: "/other"}, {Path: "/repo/src"}},
		Want: true,
	},
	{
		Name: "a descendant does not cover its ancestor",
		Key:  Key{Path: "/repo"},
		Held: []Key{{Path: "/repo/src"}},
		Want: false,
	},
	{
		Name:       "a nested worktree is not covered by the repo",
		Key:        Key{Path: "/repo/.claude/worktrees/w"},
		Held:       []Key{{Path: "/repo"}},
		Boundaries: fakeBoundaries{"/repo/.claude/worktrees/w": true},
		Want:       false,
	},
	{
		Name: "no held keys",
		Key:  Key{Path: "/repo"},
		Want: false,
	},
}

func TestCovered(t *testing.T) {
	for _, test := range coveredTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := Covered(test.Key, test.Held, test.Boundaries); got != test.Want {
				t.Errorf("Covered(%v, %v) = %v, want %v", test.Key, test.Held, got, test.Want)
			}
		})
	}
}

func TestEqualIgnoresTrailingSeparators(t *testing.T) {
	if !Equal(Key{Path: "/a/b"}, Key{Path: "/a/b/"}) {
		t.Error("trailing separator changed equality")
	}

	if Equal(Key{Path: "/a/b"}, Key{Path: "/a/c"}) {
		t.Error("different paths compared equal")
	}
}
