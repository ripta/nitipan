// Package config reads the node and broker config files under ~/.config/nitipan.
//
// The files sit outside the state directory, which an agent's sandbox can write. What the node
// connects to, and through what proxy and CA, must not come from input a session controls.
//
// The directory comes from the user database, like the state directory, because a LaunchAgent gets
// no shell environment. The owner and mode checks guard against other OS users and loose
// permissions. They do not guard against a sandboxed session, which runs as the same user. The
// sandbox's own write list does that.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Names of the config files.
const (
	NodeFile   = "node.json"
	BrokerFile = "broker.json"
)

// Major is the config format major this binary reads.
const Major = 1

// ErrUnsafe means a config file or its directory is owned by someone else or writable by others.
var ErrUnsafe = errors.New("config path is not safe to read")

// Dir returns the config directory under a home directory from the user database.
func Dir(home string) string {
	return filepath.Join(home, ".config", "nitipan")
}

// Node is the node's config.
type Node struct {
	Version string `json:"version"`

	// Broker is the broker's base URL, such as https://broker.example:8443.
	Broker string `json:"broker"`

	// Proxy is the URL of an HTTP proxy. Empty means a direct connection. The environment is never
	// consulted.
	Proxy string `json:"proxy,omitempty"`

	// CAFile is a PEM file of certificate authorities that replaces the system roots.
	CAFile string `json:"ca_file,omitempty"`
}

// Broker is the broker's config.
type Broker struct {
	Version string `json:"version"`

	// Listen is the address to listen on, such as 127.0.0.1:8443.
	Listen string `json:"listen"`

	// CertFile and KeyFile turn on TLS when both are set.
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`

	// Store is the path of the store file. Empty means the default under Application Support.
	Store string `json:"store,omitempty"`

	// OIDC configures the OIDC identity provider for user sign-ins. When absent, only the broker
	// owner can use the broker.
	OIDC *OIDCConfig `json:"oidc,omitempty"`
}

// OIDCConfig configures an OIDC identity provider.
type OIDCConfig struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	RedirectURL  string `json:"redirect_url"`
	CAFile       string `json:"ca_file,omitempty"`
}

// LoadNode reads the node config under home.
func LoadNode(home string, uid int) (Node, error) {
	var c Node

	path := filepath.Join(Dir(home), NodeFile)

	if err := load(path, uid, &c, c.version); err != nil {
		return Node{}, missingHint(err, path, "node")
	}

	if c.Broker == "" {
		return Node{}, fmt.Errorf("%s names no broker", filepath.Join(Dir(home), NodeFile))
	}

	return c, nil
}

// LoadBroker reads the broker config under home.
func LoadBroker(home string, uid int) (Broker, error) {
	var c Broker

	path := filepath.Join(Dir(home), BrokerFile)

	if err := load(path, uid, &c, c.version); err != nil {
		return Broker{}, missingHint(err, path, "broker")
	}

	if c.Listen == "" {
		return Broker{}, fmt.Errorf("%s names no listen address", filepath.Join(Dir(home), BrokerFile))
	}

	return c, nil
}

// missingHint tells the user where the file goes when it does not exist yet.
func missingHint(err error, path, daemon string) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return fmt.Errorf("%w. Run `nitipan setup %s` to see what %s holds", err, daemon, path)
}

func (c *Node) version() string   { return c.Version }
func (c *Broker) version() string { return c.Version }

func load[T any](path string, uid int, into *T, version func() string) error {
	if err := checkSafe(filepath.Dir(path), uid); err != nil {
		return err
	}

	if err := checkSafe(path, uid); err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}

	var major, minor int

	if _, err := fmt.Sscanf(version(), "%d.%d", &major, &minor); err != nil || major != Major {
		return fmt.Errorf("%s: version %q is not %d.x", path, version(), Major)
	}

	return nil
}

func checkSafe(path string, uid int) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("checking %s: %w", path, err)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("checking the owner of %s: no ownership information on this platform", path)
	}

	if int(st.Uid) != uid {
		return fmt.Errorf("%s is owned by user %d, not %d: %w", path, st.Uid, uid, ErrUnsafe)
	}

	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by its group or others (mode %04o): %w", path, info.Mode().Perm(), ErrUnsafe)
	}

	return nil
}
