package keys

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}

	return dir
}

func mkdir(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

type scopeTest struct {
	Name  string
	Scope string
	Want  error
}

var scopeTests = []scopeTest{
	{Name: "relative", Scope: "docs/api", Want: nil},
	{Name: "dot", Scope: ".", Want: nil},
	{Name: "absolute", Scope: "/etc", Want: ErrScopeAbsolute},
	{Name: "leading parent", Scope: "../other", Want: ErrScopeParent},
	{Name: "inner parent", Scope: "docs/../src", Want: ErrScopeParent},
	{Name: "empty", Scope: "", Want: ErrEmptyScope},
}

func TestValidateScope(t *testing.T) {
	for _, test := range scopeTests {
		t.Run(test.Name, func(t *testing.T) {
			err := ValidateScope(test.Scope)
			if !errors.Is(err, test.Want) {
				t.Errorf("ValidateScope(%q) = %v, want %v", test.Scope, err, test.Want)
			}
		})
	}
}

func TestDomainResolvesCheckoutSymlink(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "real"))

	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := NewCanonicalizer().Domain(filepath.Join(base, "link"), nil)
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	if len(got.Keys) != 1 || !Equal(got.Keys[0], Key{Path: filepath.Join(base, "real")}) {
		t.Errorf("keys = %v, want the resolved checkout", got.Keys)
	}
}

func TestDomainResolvesSymlinkedScope(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo", "docs"))

	if err := os.Symlink(filepath.Join(base, "repo", "docs"), filepath.Join(base, "repo", "d")); err != nil {
		t.Fatal(err)
	}

	got, err := NewCanonicalizer().Domain(filepath.Join(base, "repo"), []string{"d", "docs"})
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	if len(got.Keys) != 1 {
		t.Fatalf("keys = %v, want the two spellings collapsed to one key", got.Keys)
	}

	if !Equal(got.Keys[0], Key{Path: filepath.Join(base, "repo", "docs")}) {
		t.Errorf("key = %v, want the docs directory", got.Keys[0])
	}
}

func TestDomainRejectsSymlinkEscapingCheckout(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo"))
	mkdir(t, filepath.Join(base, "elsewhere"))

	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(base, "repo", "out")); err != nil {
		t.Fatal(err)
	}

	_, err := NewCanonicalizer().Domain(filepath.Join(base, "repo"), []string{"out"})
	if !errors.Is(err, ErrOutsideCheckout) {
		t.Errorf("err = %v, want ErrOutsideCheckout", err)
	}
}

func TestDomainCollectsScopeErrors(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo"))

	_, err := NewCanonicalizer().Domain(filepath.Join(base, "repo"), []string{"/abs", "../up", "ok"})
	if !errors.Is(err, ErrScopeAbsolute) || !errors.Is(err, ErrScopeParent) {
		t.Errorf("err = %v, want both the absolute and the parent error", err)
	}
}

func TestDomainRejectsRelativeCheckout(t *testing.T) {
	_, err := NewCanonicalizer().Domain("repo", nil)
	if !errors.Is(err, ErrNotAbsolute) {
		t.Errorf("err = %v, want ErrNotAbsolute", err)
	}
}

func TestDomainAppendsMissingTail(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo"))

	got, err := NewCanonicalizer().Domain(filepath.Join(base, "repo"), []string{"build/out"})
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	want := filepath.Join(base, "repo", "build", "out")
	if !Equal(got.Keys[0], Key{Path: want}) {
		t.Errorf("key = %v, want %s", got.Keys[0], want)
	}
}

func TestDomainTailsCompareFoldedOnInsensitiveVolume(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo"))

	canon := Canonicalizer{Volumes: forcedVolumes{sens: Sensitivity{FoldCase: true, Normalize: true}}}

	got, err := canon.Domain(filepath.Join(base, "repo"), []string{"Build/Out", "build/out"})
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	if len(got.Keys) != 1 {
		t.Errorf("keys = %v, want the two spellings to compare equal", got.Keys)
	}
}

func TestDomainResolvesCaseOnInsensitiveVolume(t *testing.T) {
	base := resolvedTempDir(t)
	mkdir(t, filepath.Join(base, "repo", "Docs"))

	if (SystemVolumes{}).Sensitivity(base).FoldCase == false {
		t.Skip("the temp directory is on a case-sensitive volume")
	}

	canon := NewCanonicalizer()

	upper, err := canon.Domain(filepath.Join(base, "repo"), []string{"Docs"})
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	lower, err := canon.Domain(filepath.Join(base, "repo"), []string{"docs"})
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}

	if upper.Keys[0].Path != lower.Keys[0].Path {
		t.Errorf("keys differ: %q and %q", upper.Keys[0].Path, lower.Keys[0].Path)
	}

	if filepath.Base(lower.Keys[0].Path) != "Docs" {
		t.Errorf("key %q does not take the stored case", lower.Keys[0].Path)
	}

	if !upper.Keys[0].FoldCase {
		t.Error("key does not record its volume's case insensitivity")
	}
}

type forcedVolumes struct {
	sens Sensitivity
}

func (f forcedVolumes) Sensitivity(string) Sensitivity {
	return f.sens
}

func (forcedVolumes) CanonicalCase(resolved string) (string, error) {
	return resolved, nil
}
