package repoid

import (
	"errors"
	"testing"
)

const (
	rootA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rootB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rootC = "cccccccccccccccccccccccccccccccccccccccc"
)

type overlapTest struct {
	Name string
	A, B Name
	Want bool
}

var overlapTests = []overlapTest{
	{"same root, same path", New([]string{rootA}, "svc", false, false), New([]string{rootA}, "svc", false, false), true},
	{"set intersection", New([]string{rootA, rootB}, "svc", false, false), New([]string{rootB, rootC}, "svc/x", false, false), true},
	{"disjoint roots", New([]string{rootA}, "svc", false, false), New([]string{rootC}, "svc", false, false), false},
	{"sibling paths", New([]string{rootA}, "svc", false, false), New([]string{rootA}, "svc2", false, false), false},
	{"whole repo covers a path", New([]string{rootA}, "", false, false), New([]string{rootA}, "a/b", false, false), true},
	{"case folding", New([]string{rootA}, "Svc", true, false), New([]string{rootA}, "svc", false, false), true},
	{"unicode normalization", New([]string{rootA}, "café", false, true), New([]string{rootA}, "café", false, false), true},
}

func TestOverlap(t *testing.T) {
	for _, test := range overlapTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := Overlap(test.A, test.B); got != test.Want {
				t.Fatalf("Overlap = %v, want %v", got, test.Want)
			}
		})
	}
}

type parseTest struct {
	Name    string
	In      string
	Path    string
	Wantbad bool
}

var parseTests = []parseTest{
	{"whole repo", "repo:" + rootA + "#", "", false},
	{"with path", "repo:" + rootA + "#services/billing/", "services/billing", false},
	{"no prefix", rootA + "#x", "", true},
	{"short sha", "repo:abc#x", "", true},
	{"dotdot", "repo:" + rootA + "#a/../b", "", true},
}

func TestParse(t *testing.T) {
	for _, test := range parseTests {
		t.Run(test.Name, func(t *testing.T) {
			n, err := Parse(test.In)
			if test.Wantbad {
				if !errors.Is(err, ErrBadName) {
					t.Fatalf("Parse err = %v, want ErrBadName", err)
				}

				return
			}

			if err != nil || n.Path != test.Path || !n.HasRoot(rootA) {
				t.Fatalf("Parse = %+v, %v", n, err)
			}
		})
	}
}

func TestTypedNameMatchesHolderSet(t *testing.T) {
	typed, _ := Parse("repo:" + rootB + "#svc")
	holder := New([]string{rootA, rootB}, "svc/api", false, false)

	if !Overlap(typed, holder) {
		t.Fatal("a typed name must meet a holder whose root set contains its root")
	}
}
