package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/space-armada/nitipan/plugins"
)

// ErrWouldOverwrite means a plugin file differs from the one nitipan ships. Pass replace to
// overwrite it.
var ErrWouldOverwrite = errors.New("a plugin file differs from the one nitipan ships")

// pluginRoot is where the embedded opencode plugin lives inside plugins.FS.
const pluginRoot = "opencode/nitipan"

// pathToken is the placeholder in the plugin sources for nitipan's absolute path. It sits inside a
// string literal, and install replaces the whole literal with a JSON-quoted path.
const pathToken = `"@@NITIPAN@@"`

// stateToken is the same for the state directory, which holds the plugins' log.
const stateToken = `"@@STATEDIR@@"`

// Installed is one plugin file install looked at.
type Installed struct {
	Path string

	// Action is "wrote", "unchanged", or "replaced".
	Action string
}

// InstallOpencode writes the opencode plugin files into <dir>/nitipan, with nitipan's absolute path
// baked in. It writes files and nothing else: no settings file is touched.
//
// It refuses to overwrite a file whose content differs from what it would write, unless replace is
// set, and it checks every file before it writes any.
func InstallOpencode(dir, executable, stateDir string, replace bool) ([]Installed, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("plugins directory %q: want an absolute path", dir)
	}

	if !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("nitipan path %q: want an absolute path", executable)
	}

	if !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("state directory %q: want an absolute path", stateDir)
	}

	files, err := render(executable, stateDir)
	if err != nil {
		return nil, err
	}

	target := filepath.Join(dir, "nitipan")

	var plan []Installed

	for _, f := range files {
		dest := filepath.Join(target, f.name)

		existing, err := os.ReadFile(dest)

		switch {
		case errors.Is(err, fs.ErrNotExist):
			plan = append(plan, Installed{Path: dest, Action: "wrote"})
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", dest, err)
		case bytes.Equal(existing, f.data):
			plan = append(plan, Installed{Path: dest, Action: "unchanged"})
		case replace:
			plan = append(plan, Installed{Path: dest, Action: "replaced"})
		default:
			return nil, fmt.Errorf("%s: %w; pass --replace to overwrite it", dest, ErrWouldOverwrite)
		}
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", target, err)
	}

	for i, f := range files {
		if plan[i].Action == "unchanged" {
			continue
		}

		if err := writeAtomic(plan[i].Path, f.data); err != nil {
			return nil, err
		}
	}

	return plan, nil
}

type pluginFile struct {
	name string
	data []byte
}

// render reads the embedded plugin files and fills in the path.
func render(executable, stateDir string) ([]pluginFile, error) {
	quoted, err := json.Marshal(executable)
	if err != nil {
		return nil, fmt.Errorf("quoting the nitipan path: %w", err)
	}

	quotedState, err := json.Marshal(stateDir)
	if err != nil {
		return nil, fmt.Errorf("quoting the state directory: %w", err)
	}

	entries, err := fs.ReadDir(plugins.FS, pluginRoot)
	if err != nil {
		return nil, fmt.Errorf("reading the embedded plugins: %w", err)
	}

	var out []pluginFile

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		data, err := fs.ReadFile(plugins.FS, path.Join(pluginRoot, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading the embedded %s: %w", e.Name(), err)
		}

		text := strings.ReplaceAll(string(data), pathToken, string(quoted))
		text = strings.ReplaceAll(text, stateToken, string(quotedState))

		out = append(out, pluginFile{name: e.Name(), data: []byte(text)})
	}

	return out, nil
}

func writeAtomic(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".nitipan-install-")
	if err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}

	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing %s: %w", dest, err)
	}

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing %s: %w", dest, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}

	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}

	return nil
}
