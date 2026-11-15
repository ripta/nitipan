package node

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/wire"
)

// File names under the state directory. V1 binaries never scan either one.
const (
	lockName = "node.lock"
	keyName  = "node-key"
)

// ErrNodeRunning means another node holds the lock for this machine and user.
var ErrNodeRunning = errors.New("another node already runs for this machine and user")

// lockNode takes the node's flock, so a second node fails loudly. Two would keep replacing each
// other's snapshots.
func lockNode(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the node lock: %w", err)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()

		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, lockName), ErrNodeRunning)
		}

		return nil, fmt.Errorf("locking the node: %w", err)
	}

	return f, nil
}

// loadKey returns the node's instance key, creating a random one on first run. The key narrows
// takeover: taking a node's stream needs a file in the user's 0700 state directory. It is not auth.
func loadKey(dir string) (string, error) {
	path := filepath.Join(dir, keyName)

	data, err := os.ReadFile(path)
	if err == nil {
		return checkKey(path, data)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading the instance key: %w", err)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating the instance key: %w", err)
	}

	key := hex.EncodeToString(raw[:])

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		data, err = os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading the instance key: %w", err)
		}

		return checkKey(path, data)
	}

	if err != nil {
		return "", fmt.Errorf("creating the instance key: %w", err)
	}

	if _, err := f.WriteString(key + "\n"); err != nil {
		_ = f.Close()

		return "", fmt.Errorf("writing the instance key: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()

		return "", fmt.Errorf("syncing the instance key: %w", err)
	}

	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing the instance key: %w", err)
	}

	return key, nil
}

func checkKey(path string, data []byte) (string, error) {
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%s is empty. Delete it and the node makes a new key", path)
	}

	info, err := os.Stat(path)
	if err == nil && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by others (mode %04o). Fix it with chmod 600", path, info.Mode().Perm())
	}

	return key, nil
}

// fingerprint identifies the file an executable path leads to, so the node can tell that an upgrade
// replaced it.
type fingerprint struct {
	dev, ino uint64
	size     int64
	mtime    time.Time
}

// statExec reads the fingerprint. A missing file returns fs.ErrNotExist.
func statExec(path string) (fingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fingerprint{}, fmt.Errorf("checking the executable %s: %w", path, err)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fingerprint{}, fmt.Errorf("checking %s: no file identity on this platform", path)
	}

	return fingerprint{dev: uint64(st.Dev), ino: st.Ino, size: info.Size(), mtime: info.ModTime()}, nil
}

// ExistingFingerprint returns the fingerprint of the node's key when the key exists. It creates
// nothing, so a command that promises to write nothing can use it.
func ExistingFingerprint(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, keyName))
	if err != nil {
		return "", false
	}

	key, err := checkKey(filepath.Join(dir, keyName), data)
	if err != nil {
		return "", false
	}

	return wire.Fingerprint(wire.KeyHash(key)), true
}

// Fingerprint returns the fingerprint of the node's instance key, creating the key when the node never
// ran. The user passes it to the broker's approve command. It reveals nothing a hash of the key does
// not.
func Fingerprint(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the state directory %s: %w", dir, err)
	}

	key, err := loadKey(dir)
	if err != nil {
		return "", err
	}

	return wire.Fingerprint(wire.KeyHash(key)), nil
}
