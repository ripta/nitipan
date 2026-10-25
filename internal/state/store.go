package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// idPattern is a ULID: 26 characters of Crockford base32. Lease IDs become file names, so anything
// else is refused before it can reach the filesystem.
var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidID reports whether id is a well-formed lease ID.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// maxNameLen bounds a named file, so a temp file's name still fits the 255-byte limit of common
// file systems.
const maxNameLen = 200

// ValidName reports whether name is safe as a named file in a state subdirectory. It keeps a name
// from escaping the directory or from hiding as a temp file. The caller owns the grammar beyond that.
func ValidName(name string) bool {
	if name == "" || len(name) > maxNameLen || strings.HasPrefix(name, ".") {
		return false
	}

	return !strings.ContainsAny(name, "/\x00")
}

// ListNames lists the published files in a subdirectory of the state directory at root, in name
// order. Temp files and directories are skipped. A missing subdirectory is empty. It needs no lock.
func ListNames(root, sub string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, sub))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", sub, err)
	}

	var names []string

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) || e.IsDir() {
			continue
		}

		names = append(names, e.Name())
	}

	return names, nil
}

// ReadName reads one named file. A missing file returns an error matching fs.ErrNotExist.
func ReadName(root, sub, name string) ([]byte, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%q: %w", name, ErrBadName)
	}

	data, err := os.ReadFile(filepath.Join(root, sub, name))
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", sub, name, err)
	}

	return data, nil
}

// CreateMark creates an empty file with O_EXCL and no lock. It reports whether this call created
// it. An existing mark counts as success, because the mark's name carries everything it says.
func CreateMark(root, sub, name string) (created bool, err error) {
	if !ValidName(name) {
		return false, fmt.Errorf("%q: %w", name, ErrBadName)
	}

	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", sub, err)
	}

	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("creating the mark %s/%s: %w", sub, name, err)
	}

	if err := f.Close(); err != nil {
		return true, fmt.Errorf("closing the mark %s/%s: %w", sub, name, err)
	}

	return true, nil
}

// File is one published record read back from disk.
type File struct {
	// ID is the file name without its suffix. BadName is true when it is not a well-formed lease ID.
	ID      string
	BadName bool
	Data    []byte
}

// Scan reads every published record in a subdirectory, skipping temp files. It needs no lock,
// because records are published atomically.
func (d *Dir) Scan(sub string) ([]File, error) {
	entries, err := os.ReadDir(filepath.Join(d.path, sub))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", sub, err)
	}

	var files []File

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, fileSuffix) || e.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(d.path, sub, name))
		if errors.Is(err, fs.ErrNotExist) {
			// Removed between the listing and the read.
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", sub, name, err)
		}

		id := strings.TrimSuffix(name, fileSuffix)
		files = append(files, File{ID: id, BadName: !ValidID(id), Data: data})
	}

	return files, nil
}

// Read returns one published record. A missing record returns an error matching fs.ErrNotExist.
func (d *Dir) Read(sub, id string) ([]byte, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%q: %w", id, ErrBadID)
	}

	data, err := os.ReadFile(filepath.Join(d.path, sub, id+fileSuffix))
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", sub, id, err)
	}

	return data, nil
}

// Publish writes a new record to a temp file, syncs it, and links it into place. The link fails
// with ErrExists the way O_EXCL would, and it appears all at once.
func (l *Locked) Publish(sub, id string, data []byte) error {
	if !ValidID(id) {
		return fmt.Errorf("%q: %w", id, ErrBadID)
	}

	return l.publish(sub, id+fileSuffix, data)
}

// Replace publishes a record over an existing one with rename(2), which link(2) cannot do. A
// crash leaves the old record or the new one, never neither.
func (l *Locked) Replace(sub, id string, data []byte) error {
	if !ValidID(id) {
		return fmt.Errorf("%q: %w", id, ErrBadID)
	}

	return l.replace(sub, id+fileSuffix, data)
}

// Remove deletes a published record. A missing record returns an error matching fs.ErrNotExist.
func (l *Locked) Remove(sub, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q: %w", id, ErrBadID)
	}

	return l.remove(sub, id+fileSuffix)
}

// PublishNamed is Publish for a file whose name is not a lease ID, such as a session file. The
// caller owns the name grammar. Only a name that could escape the directory or collide with a temp
// file is refused here.
func (l *Locked) PublishNamed(sub, name string, data []byte) error {
	if !ValidName(name) {
		return fmt.Errorf("%q: %w", name, ErrBadName)
	}

	return l.publish(sub, name, data)
}

// ReplaceNamed is Replace for a named file. It also creates the file when it is missing.
func (l *Locked) ReplaceNamed(sub, name string, data []byte) error {
	if !ValidName(name) {
		return fmt.Errorf("%q: %w", name, ErrBadName)
	}

	return l.replace(sub, name, data)
}

// RemoveNamed is Remove for a named file.
func (l *Locked) RemoveNamed(sub, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q: %w", name, ErrBadName)
	}

	return l.remove(sub, name)
}

func (l *Locked) publish(sub, file string, data []byte) error {
	tmp, final, err := l.prepare(sub, file, data)
	if err != nil {
		return err
	}

	defer os.Remove(tmp)

	if err := os.Link(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("publishing %s/%s: %w", sub, file, ErrExists)
		}

		return fmt.Errorf("publishing %s/%s: %w", sub, file, err)
	}

	return l.syncDir(sub)
}

func (l *Locked) replace(sub, file string, data []byte) error {
	tmp, final, err := l.prepare(sub, file, data)
	if err != nil {
		return err
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("replacing %s/%s: %w", sub, file, err)
	}

	return l.syncDir(sub)
}

func (l *Locked) remove(sub, file string) error {
	if err := l.writable(); err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(l.dir.path, sub, file)); err != nil {
		return fmt.Errorf("removing %s/%s: %w", sub, file, err)
	}

	return l.syncDir(sub)
}

// prepare writes data to a synced temp file beside its final name.
func (l *Locked) prepare(sub, file string, data []byte) (tmp, final string, err error) {
	if err := l.writable(); err != nil {
		return "", "", err
	}

	dir := filepath.Join(l.dir.path, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", sub, err)
	}

	tmp = filepath.Join(dir, tempPrefix+file)
	final = filepath.Join(dir, file)

	// Lock sweeps temp files left by crashed writers when it takes the lock. A temp file that still
	// exists at this point is unexpected, so O_EXCL surfaces it as an error.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("creating the temp file for %s/%s: %w", sub, file, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)

		return "", "", fmt.Errorf("writing %s/%s: %w", sub, file, err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)

		return "", "", fmt.Errorf("syncing %s/%s: %w", sub, file, err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)

		return "", "", fmt.Errorf("closing %s/%s: %w", sub, file, err)
	}

	return tmp, final, nil
}

// syncDir flushes the directory entry, so a published name survives power loss with its data.
func (l *Locked) syncDir(sub string) error {
	d, err := os.Open(filepath.Join(l.dir.path, sub))
	if err != nil {
		return fmt.Errorf("opening %s to sync it: %w", sub, err)
	}

	defer d.Close()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", sub, err)
	}

	return nil
}

// sweepTemp removes temp files left by a writer that crashed. Every write happens under the lock,
// so a temp file seen by the lock holder cannot belong to a live writer.
func (l *Locked) sweepTemp() error {
	subs := []string{LeasesDir, TombstonesDir, RequestsDir, NoticesDir, MarksDir, RegisteredDir, PresenceDir, ExecutingDir, ConfigDir}

	for _, sub := range subs {
		entries, err := os.ReadDir(filepath.Join(l.dir.path, sub))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return fmt.Errorf("sweeping %s: %w", sub, err)
		}

		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), tempPrefix) {
				continue
			}

			if err := os.Remove(filepath.Join(l.dir.path, sub, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("sweeping %s/%s: %w", sub, e.Name(), err)
			}
		}
	}

	return nil
}
