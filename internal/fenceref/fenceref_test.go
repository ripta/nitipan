package fenceref

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const rootA = "1111111111111111111111111111111111111111"

type encodeTest struct {
	Name string
	Path string
	Want string
}

var encodeTests = []encodeTest{
	{"repo root", "", "%00"},
	{"plain", "src", "src"},
	{"nested path flattens", "src/app/main.go", "src%2Fapp%2Fmain.go"},
	{"leading dot", ".github", "%2Egithub"},
	{"leading dot nested", ".github/workflows", "%2Egithub%2Fworkflows"},
	{"uppercase letters escape", "README.md", "%52%45%41%44%4D%45.md"},
	{"double dot", "a..b", "a%2E.b"},
	{"triple dot", "a...b", "a%2E%2E.b"},
	{"lock suffix", "x.lock", "x%2Elock"},
	{"lock in the middle", "x.lock/y", "x.lock%2Fy"},
	{"trailing dot", "a.", "a%2E"},
	{"percent", "50%", "50%25"},
	{"space and tilde", "a b~", "a%20b%7E"},
	{"multibyte", "é", "%C3%A9"},
	{"at brace", "a@{b", "a%40%7Bb"},
	{"dash underscore", "a-b_c", "a-b_c"},
}

func TestEncodePathRoundTripsAndIsCanonical(t *testing.T) {
	for _, test := range encodeTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := EncodePath(test.Path); got != test.Want {
				t.Fatalf("EncodePath(%q) = %q, want %q", test.Path, got, test.Want)
			}

			back, err := DecodePath(test.Want)
			if err != nil || back != test.Path {
				t.Fatalf("DecodePath(%q) = %q, %v, want %q", test.Want, back, err, test.Path)
			}
		})
	}
}

// Two paths that differ only in case must not share a ref on a case-insensitive filesystem holding
// loose refs.
func TestNamesNeverCollideOnACaseInsensitiveFilesystem(t *testing.T) {
	paths := []string{"Src", "src", "SRC", "sRc", "Src/A", "src/a", "src/A"}
	seen := map[string]string{}

	for _, p := range paths {
		name, err := Name(rootA, p)
		if err != nil {
			t.Fatal(err)
		}

		folded := strings.ToLower(name)
		if other, ok := seen[folded]; ok {
			t.Errorf("%q and %q fold to the same ref %q", p, other, folded)
		}

		seen[folded] = p
	}
}

type undecodableTest struct {
	Name string
	Ref  string
}

var undecodableTests = []undecodableTest{
	{"outside the namespace", "refs/heads/main"},
	{"no path component", "refs/nitipan/fence/" + rootA},
	{"root is not a commit ID", "refs/nitipan/fence/main/src"},
	{"uppercase root", "refs/nitipan/fence/1111111111111111111111111111111111111111/%2e"},
	{"raw uppercase letter is not canonical", "refs/nitipan/fence/" + rootA + "/Src"},
	{"lowercase hex escape is not canonical", "refs/nitipan/fence/" + rootA + "/%2egithub"},
	{"needless escape is not canonical", "refs/nitipan/fence/" + rootA + "/%73rc"},
	{"truncated escape", "refs/nitipan/fence/" + rootA + "/src%2"},
	{"bad escape", "refs/nitipan/fence/" + rootA + "/src%zz"},
	{"empty path", "refs/nitipan/fence/" + rootA + "/"},
	{"raw slash", "refs/nitipan/fence/" + rootA + "/src/a"},
	{"root mixed with text", "refs/nitipan/fence/" + rootA + "/a%00"},
}

func TestAFenceRefNameThatIsNotCanonicalDoesNotDecode(t *testing.T) {
	for _, test := range undecodableTests {
		t.Run(test.Name, func(t *testing.T) {
			if _, err := Parse(test.Ref); !errors.Is(err, ErrNotDecodable) {
				t.Fatalf("Parse(%q) error = %v, want ErrNotDecodable", test.Ref, err)
			}
		})
	}
}

func TestNameAndParseAreInverse(t *testing.T) {
	for _, test := range encodeTests {
		name, err := Name(rootA, test.Path)
		if err != nil {
			t.Fatal(err)
		}

		got, err := Parse(name)
		if err != nil || got != (Ref{Root: rootA, Path: test.Path}) {
			t.Errorf("Parse(%q) = %v, %v", name, got, err)
		}
	}
}

func TestAPathWhoseEncodingOutgrowsAComponentIsRefused(t *testing.T) {
	if _, err := Name(rootA, strings.Repeat("a/", 100)); err == nil {
		t.Fatal("a 200-byte encoding was accepted")
	}
}

type decodeTest struct {
	Name string
	Data string
	Want error
}

var decodeTests = []decodeTest{
	{"current", `{"version":"1.0","token":7,"path":"src","lease":"L"}`, nil},
	{"newer minor with an extra field", `{"version":"1.4","token":7,"path":"src","lease":"L","extra":{"a":1}}`, nil},
	{"higher major", `{"version":"2.0","token":"seven","path":42}`, ErrHigherMajor},
	{"key does not match the ref name", `{"version":"1.0","token":7,"path":"docs","lease":"L"}`, ErrUnusable},
	{"zero token", `{"version":"1.0","token":0,"path":"src","lease":"L"}`, ErrUnusable},
	{"not json", `nope`, ErrUnusable},
	{"no version", `{"token":7,"path":"src"}`, ErrUnusable},
	{"token is not a number", `{"version":"1.0","token":"7","path":"src"}`, ErrUnusable},
}

func TestADecodedObjectIsCheckedAgainstItsRefName(t *testing.T) {
	for _, test := range decodeTests {
		t.Run(test.Name, func(t *testing.T) {
			obj, err := Decode([]byte(test.Data), Ref{Root: rootA, Path: "src"})
			if !errors.Is(err, test.Want) {
				t.Fatalf("error = %v, want %v", err, test.Want)
			}

			if test.Want == nil && obj.Token != 7 {
				t.Errorf("token = %d, want 7", obj.Token)
			}
		})
	}
}

func newRepo(t *testing.T) Store {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()

	out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "someone")
	t.Setenv("GIT_AUTHOR_EMAIL", "someone@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "someone")
	t.Setenv("GIT_COMMITTER_EMAIL", "someone@example.com")

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	return Store{Dir: dir, Clean: true, Now: func() time.Time { return now }}
}

func TestAFenceCommitCarriesTheTokenAndNamesNoPerson(t *testing.T) {
	s := newRepo(t)
	ref := Ref{Root: rootA, Path: "src"}

	sha, err := s.Create(Object{Token: 9, Path: EncodePath("src"), Lease: "L1"}, "")
	if err != nil {
		t.Fatal(err)
	}

	obj, err := s.Read(sha, ref)
	if err != nil || obj.Token != 9 || obj.Lease != "L1" || obj.Version != WriteVersion {
		t.Fatalf("Read = %+v, %v", obj, err)
	}

	out, err := exec.Command("git", "-C", s.Dir, "log", "-1", "--format=%an <%ae> %cn <%ce> %at", sha).Output()
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(string(out)); got != "nitipan <nitipan@invalid> nitipan <nitipan@invalid> 1794139200" {
		t.Errorf("identity and time = %q", got)
	}
}

// A refresh keeps the prior tree, so a writer never drops a field it does not know.
func TestARefreshKeepsTheTreeAndAnUnknownField(t *testing.T) {
	s := newRepo(t)

	newer := `{"version":"1.3","token":5,"path":"src","lease":"L","future":{"k":true}}`

	blob := mustGit(t, s, newer, "hash-object", "-w", "--stdin")
	tree := mustGit(t, s, "100644 blob "+blob+"\t"+FileName+"\n", "mktree")
	first := mustGit(t, s, "", "commit-tree", tree, "-m", "future writer")

	s.Now = func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) }

	next, err := s.Refresh(first)
	if err != nil {
		t.Fatal(err)
	}

	if got := mustGit(t, s, "", "rev-parse", next+"^{tree}"); got != tree {
		t.Errorf("tree = %s, want %s", got, tree)
	}

	if got := mustGit(t, s, "", "rev-parse", next+"^"); got != first {
		t.Errorf("parent = %s, want %s", got, first)
	}

	if got := mustGit(t, s, "", "cat-file", "blob", next+":"+FileName); !strings.Contains(got, `"future"`) {
		t.Errorf("the unknown field was dropped: %s", got)
	}

	if _, err := s.Read(next, Ref{Root: rootA, Path: "src"}); err != nil {
		t.Errorf("the refreshed newer-minor object is unreadable: %v", err)
	}
}

func TestACommitWithoutTheFileIsUnusable(t *testing.T) {
	s := newRepo(t)

	tree := mustGit(t, s, "", "mktree")
	sha := mustGit(t, s, "", "commit-tree", tree, "-m", "empty")

	if _, err := s.Read(sha, Ref{Root: rootA, Path: "src"}); !errors.Is(err, ErrUnusable) {
		t.Fatalf("error = %v, want ErrUnusable", err)
	}
}

func mustGit(t *testing.T, s Store, stdin string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", s.Dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}

	return strings.TrimSpace(string(out))
}
