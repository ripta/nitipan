// Package fenceref defines the fence refs a remote carries and the objects behind them.
//
// A fence ref is named refs/nitipan/fence/<root-sha>/<encoded-path>. The root keeps apart histories
// that never meet, such as main and an orphan gh-pages. The encoded path flattens a repo-relative
// path into one ref component, since git cannot create refs/x/src/a while refs/x/src exists.
//
// The namespace, the name encoding, and the object format are stored data on remotes the user may
// not control. Every nitipan binary that pushes reads them. A change to any of them is a one-way
// door, so a reader refuses what it does not understand and says why.
package fenceref

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// Namespace holds every fence ref.
const Namespace = "refs/nitipan/fence/"

// Pattern is the glob that lists the fence refs of a remote.
const Pattern = Namespace + "*"

// FileName is the one file in a fence commit's tree.
const FileName = "fence.json"

// MajorVersion is the highest object major this binary reads.
const MajorVersion = 1

// WriteVersion is what a writer stamps. A minor may add a field only if an older reader that ignores
// it, or an older writer that drops it from a ref it owns, never lets through a push that a newer
// reader would refuse. Any other field needs a new major.
var WriteVersion = record.Version{Major: MajorVersion, Minor: 0}

// maxComponent is the longest ref component filesystems that hold loose refs allow.
const maxComponent = 255

// repoRoot is the encoding of the repo root. No path can contain a NUL byte, so no path encodes to it.
const repoRoot = "%00"

// Failures a reader reports. Each has its own next step.
var (
	// ErrNotDecodable means a ref name in the namespace is not one this binary could have written.
	ErrNotDecodable = errors.New("the fence ref name does not decode")

	// ErrHigherMajor means the object was written by a newer nitipan.
	ErrHigherMajor = errors.New("the fence object is of a newer major version")

	// ErrUnusable means the object cannot be read, or does not match its ref name.
	ErrUnusable = errors.New("the fence object is unusable")
)

const upperHex = "0123456789ABCDEF"

func safe(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
}

// EncodePath turns a repo-relative path into one ref component. A byte outside [a-z0-9._-] becomes %XX
// with uppercase hex, uppercase letters included, so two names never differ only in case. A dot that
// git would reject in a component is escaped too: a leading dot, a trailing dot, the first dot of "..",
// and the dot of a ".lock" suffix. The repo root encodes as %00.
func EncodePath(path string) string {
	if path == "" {
		return repoRoot
	}

	var b strings.Builder

	for i := range len(path) {
		c := path[i]

		switch {
		case !safe(c):
			escape(&b, c)
		case c == '.' && dotNeedsEscape(path, i):
			escape(&b, c)
		default:
			b.WriteByte(c)
		}
	}

	return b.String()
}

func escape(b *strings.Builder, c byte) {
	b.WriteByte('%')
	b.WriteByte(upperHex[c>>4])
	b.WriteByte(upperHex[c&0xf])
}

// dotNeedsEscape decides on a dot of the raw path. A dot is kept raw only when git would accept it at
// that place in the encoded component. Only safe bytes stay raw, and every other byte is escaped, so
// the neighbors can be judged on the raw path.
func dotNeedsEscape(path string, i int) bool {
	if i == 0 || i == len(path)-1 {
		return true
	}

	if path[i+1] == '.' {
		return true
	}

	return strings.HasPrefix(path[i:], ".lock") && i+len(".lock") == len(path)
}

// DecodePath reverses EncodePath. It accepts only the canonical form, so one path has one name.
func DecodePath(enc string) (string, error) {
	if enc == repoRoot {
		return "", nil
	}

	if enc == "" {
		return "", fmt.Errorf("%w: the path is empty", ErrNotDecodable)
	}

	var b strings.Builder

	for i := 0; i < len(enc); i++ {
		if enc[i] != '%' {
			b.WriteByte(enc[i])

			continue
		}

		if i+2 >= len(enc) {
			return "", fmt.Errorf("%w: truncated escape in %q", ErrNotDecodable, enc)
		}

		hi, lo := hexVal(enc[i+1]), hexVal(enc[i+2])
		if hi < 0 || lo < 0 {
			return "", fmt.Errorf("%w: bad escape in %q", ErrNotDecodable, enc)
		}

		b.WriteByte(byte(hi<<4 | lo))

		i += 2
	}

	path := b.String()
	if path == "" || strings.ContainsRune(path, 0) || EncodePath(path) != enc {
		return "", fmt.Errorf("%w: %q is not the canonical encoding of a path", ErrNotDecodable, enc)
	}

	return path, nil
}

// hexVal reads an uppercase hex digit. Lowercase is not canonical.
func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}

	return -1
}

// Ref is a fence ref name taken apart. Path is repo-relative and empty for the repo root.
type Ref struct {
	Root string
	Path string
}

// Name is the full ref name.
func (r Ref) Name() (string, error) {
	return Name(r.Root, r.Path)
}

// Name builds a fence ref name from a root commit and a repo-relative path.
func Name(root, path string) (string, error) {
	if !validRoot(root) {
		return "", fmt.Errorf("%w: %q is not a root commit ID", ErrNotDecodable, root)
	}

	enc := EncodePath(path)
	if len(enc) > maxComponent {
		return "", fmt.Errorf("the path %q is too long for a fence ref name. Its encoding is %d bytes and the limit is %d",
			path, len(enc), maxComponent)
	}

	return Namespace + root + "/" + enc, nil
}

// Parse takes a fence ref name apart. A name that is not exactly what Name writes is not decodable.
func Parse(name string) (Ref, error) {
	rest, ok := strings.CutPrefix(name, Namespace)
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q is outside %s", ErrNotDecodable, name, Namespace)
	}

	root, enc, found := strings.Cut(rest, "/")
	if !found || !validRoot(root) {
		return Ref{}, fmt.Errorf("%w: %q is not <root>/<path>", ErrNotDecodable, name)
	}

	path, err := DecodePath(enc)
	if err != nil {
		return Ref{}, err
	}

	return Ref{Root: root, Path: path}, nil
}

// InNamespace reports whether a ref name is a fence ref name, decodable or not.
func InNamespace(name string) bool {
	return strings.HasPrefix(name, Namespace)
}

func validRoot(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// Object is the content of a fence commit's fence.json. It names no holder, machine, or user, since a
// fence ref may sit on a public remote.
type Object struct {
	Version record.Version `json:"version"`

	// Token is the highest fence token pushed for the key. A JSON number below 2^53 keeps it exact for
	// readers such as jq.
	Token uint64 `json:"token"`

	// Path is the key's repo-relative path in the ref-name encoding.
	Path string `json:"path"`

	// Lease is the broker lease the token came from.
	Lease string `json:"lease"`
}

// Marshal encodes the object at the version a writer stamps.
func (o Object) Marshal() ([]byte, error) {
	o.Version = WriteVersion

	data, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("encoding the fence object: %w", err)
	}

	return append(data, '\n'), nil
}

// Decode reads a fence object and checks it against the ref it was found under. A higher major is
// reported before any other field is read, since a newer major may change every field. Fields this
// binary does not know are ignored.
func Decode(data []byte, ref Ref) (Object, error) {
	var head struct {
		Version *record.Version `json:"version"`
	}

	if err := json.Unmarshal(data, &head); err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrUnusable, err)
	}

	if head.Version == nil {
		return Object{}, fmt.Errorf("%w: the version is missing", ErrUnusable)
	}

	if head.Version.Major > MajorVersion {
		return Object{Version: *head.Version}, fmt.Errorf("%w: version %s", ErrHigherMajor, head.Version)
	}

	var o Object
	if err := json.Unmarshal(data, &o); err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrUnusable, err)
	}

	if o.Token == 0 {
		return Object{}, fmt.Errorf("%w: the token is missing or zero", ErrUnusable)
	}

	if o.Path != EncodePath(ref.Path) {
		return Object{}, fmt.Errorf("%w: the key %q does not match the ref name", ErrUnusable, o.Path)
	}

	return o, nil
}
