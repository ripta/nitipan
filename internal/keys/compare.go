package keys

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Equal reports whether two keys name the same path.
func Equal(a, b Key) bool {
	fold, normalize := comparison(a, b)

	return equalComponents(components(a.Path, fold, normalize), components(b.Path, fold, normalize))
}

// Contains reports whether anc is equal to desc or is its ancestor by whole path components.
//
// A git checkout boundary strictly below anc and at or above desc breaks containment. A nil finder
// means no boundary exists.
func Contains(anc, desc Key, finder BoundaryFinder) bool {
	fold, normalize := comparison(anc, desc)

	ac := components(anc.Path, fold, normalize)
	dc := components(desc.Path, fold, normalize)
	if len(ac) > len(dc) {
		return false
	}

	if !equalComponents(ac, dc[:len(ac)]) {
		return false
	}

	if finder == nil {
		return true
	}

	raw := splitPath(desc.Path)
	for i := len(ac) + 1; i <= len(raw); i++ {
		if finder.IsBoundary("/" + strings.Join(raw[:i], "/")) {
			return false
		}
	}

	return true
}

// Overlap reports whether either key contains the other.
func Overlap(a, b Key, finder BoundaryFinder) bool {
	return Contains(a, b, finder) || Contains(b, a, finder)
}

// Covered reports whether held contains key. Several held keys cover a key one at a time, never
// by union.
func Covered(key Key, held []Key, finder BoundaryFinder) bool {
	for _, h := range held {
		if Contains(h, key, finder) {
			return true
		}
	}

	return false
}

// comparison folds or normalizes when either key's volume calls for it. Two keys on different
// volumes share their ancestor chain only above the volume boundary, so the more insensitive reading
// can only add overlaps, never hide one.
func comparison(a, b Key) (fold, normalize bool) {
	return a.FoldCase || b.FoldCase, a.Normalize || b.Normalize
}

func components(path string, fold, normalize bool) []string {
	parts := splitPath(path)
	if !fold && !normalize {
		return parts
	}

	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = foldPart(part, fold, normalize)
	}

	return out
}

func foldPart(part string, fold, normalize bool) string {
	if normalize {
		part = norm.NFC.String(part)
	}

	if fold {
		part = cases.Fold().String(part)
	}

	if normalize {
		part = norm.NFC.String(part)
	}

	return part
}

func splitPath(path string) []string {
	var parts []string

	for _, part := range strings.Split(path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}

	return parts
}

func equalComponents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
