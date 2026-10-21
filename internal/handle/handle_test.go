package handle

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var lowerASCII = regexp.MustCompile(`^[a-z]+$`)

func TestWordListIsTheBIP39EnglishShape(t *testing.T) {
	got := Words()

	if len(got) != WordCount {
		t.Fatalf("%d words, want %d", len(got), WordCount)
	}

	if !slices.IsSorted(got) {
		t.Error("word list is not sorted")
	}

	seen := map[string]bool{}

	for _, w := range got {
		if !lowerASCII.MatchString(w) {
			t.Errorf("word %q is not lowercase ASCII", w)
		}

		if seen[w] {
			t.Errorf("word %q appears twice", w)
		}

		seen[w] = true
	}

	for _, w := range []string{"abandon", "zoo", "satoshi", "zebra"} {
		if !seen[w] {
			t.Errorf("list is missing %q", w)
		}
	}
}

func TestDeriveIsStableAndSeparatesInputs(t *testing.T) {
	a := Derive("claude\x00100\x001-0", "machine-1")

	if a != Derive("claude\x00100\x001-0", "machine-1") {
		t.Error("the same inputs gave two handles")
	}

	if len(strings.Split(a, "-")) != 4 {
		t.Errorf("handle %q is not four words", a)
	}

	for _, other := range []string{
		Derive("claude\x00101\x001-0", "machine-1"),
		Derive("claude\x00100\x001-0", "machine-2"),
	} {
		if other == a {
			t.Errorf("a different input gave the same handle %q", a)
		}
	}

	if Derive("ab", "c") == Derive("a", "bc") {
		t.Error("identity and machine are not separated")
	}
}
