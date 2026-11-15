// Package repoid defines the repo-relative name a wider-reach lease carries.
//
// A name is a set of root commits and a path inside the repo. Two names overlap when their root sets
// intersect and their paths overlap by whole components. Names live in sidecars and broker messages,
// never in a directory a V1 binary scans.
package repoid

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/keys"
)

// Prefix opens the text form of a name.
const Prefix = "repo:"

// ErrBadName means a typed name is not repo:<root-sha>#<path>.
var ErrBadName = errors.New("not a repo name")

// Name is one repo-relative entry. Roots is sorted and never empty. Path is slash-separated, relative
// to the repo root, and empty for the whole repo.
type Name struct {
	Roots []string `json:"roots"`
	Path  string   `json:"path"`

	// FoldCase and Normalize carry the claiming volume's comparison rules, as a path key does.
	FoldCase  bool `json:"fold_case,omitempty"`
	Normalize bool `json:"normalize,omitempty"`
}

// New builds a name with a sorted, de-duplicated root set and a cleaned path.
func New(roots []string, path string, fold, normalize bool) Name {
	rs := slices.Clone(roots)
	slices.Sort(rs)

	return Name{Roots: slices.Compact(rs), Path: cleanPath(path), FoldCase: fold, Normalize: normalize}
}

func cleanPath(p string) string {
	var parts []string

	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}

	return strings.Join(parts, "/")
}

// Parse reads the typed form repo:<root-sha>#<path>. The result has one root.
func Parse(s string) (Name, error) {
	rest, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return Name{}, fmt.Errorf("%q: want repo:<root-sha>#<path>: %w", s, ErrBadName)
	}

	root, path, _ := strings.Cut(rest, "#")
	if !validSHA(root) {
		return Name{}, fmt.Errorf("%q: the root must be a full hexadecimal commit ID: %w", s, ErrBadName)
	}

	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return Name{}, fmt.Errorf("%q: the path must not contain a .. component: %w", s, ErrBadName)
		}
	}

	return New([]string{strings.ToLower(root)}, path, false, false), nil
}

func validSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}

	return true
}

// Text prints the form people type and read, with the first-parent root the caller derived.
func (n Name) Text(root string) string {
	return Prefix + root + "#" + n.Path
}

// String prints the whole root set, which is the stored form.
func (n Name) String() string {
	return Prefix + strings.Join(n.Roots, "+") + "#" + n.Path
}

// HasRoot reports whether the root set contains root.
func (n Name) HasRoot(root string) bool {
	return slices.Contains(n.Roots, strings.ToLower(root))
}

// Equal reports whether two names have the same roots and the same path under their folding rules.
func Equal(a, b Name) bool {
	return slices.Equal(a.Roots, b.Roots) && keys.Equal(a.key(), b.key())
}

func (n Name) key() keys.Key {
	return keys.Key{Path: "/" + n.Path, FoldCase: n.FoldCase, Normalize: n.Normalize}
}

// Overlap reports whether the root sets intersect and the paths overlap by whole components.
func Overlap(a, b Name) bool {
	if !intersects(a.Roots, b.Roots) {
		return false
	}

	return keys.Overlap(a.key(), b.key(), nil)
}

// OverlapAny reports whether any name in as overlaps any name in bs.
func OverlapAny(as, bs []Name) bool {
	for _, a := range as {
		for _, b := range bs {
			if Overlap(a, b) {
				return true
			}
		}
	}

	return false
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}

	return false
}
