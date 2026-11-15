package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, name, body string, dirMode, fileMode os.FileMode) string {
	t.Helper()

	home := t.TempDir()
	dir := Dir(home)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, dirMode); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), fileMode); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(filepath.Join(dir, name), fileMode); err != nil {
		t.Fatal(err)
	}

	return home
}

func TestLoadNodeReadsTheFileUnderTheGivenHome(t *testing.T) {
	home := writeConfig(t, NodeFile,
		`{"version":"1.2","broker":"https://b:1","proxy":"http://p:3128","ca_file":"/ca.pem"}`, 0o700, 0o600)

	got, err := LoadNode(home, os.Getuid())
	if err != nil || got.Broker != "https://b:1" || got.Proxy != "http://p:3128" || got.CAFile != "/ca.pem" {
		t.Errorf("LoadNode = %+v, %v", got, err)
	}
}

type loadFailure struct {
	Name     string
	Body     string
	DirMode  os.FileMode
	FileMode os.FileMode
	UID      int
	Want     string
	Unsafe   bool
}

var loadFailures = []loadFailure{
	{Name: "group-writable file", Body: `{"version":"1.0","broker":"http://b"}`, DirMode: 0o700, FileMode: 0o620, Unsafe: true, Want: "writable"},
	{Name: "world-writable directory", Body: `{"version":"1.0","broker":"http://b"}`, DirMode: 0o707, FileMode: 0o600, Unsafe: true, Want: "writable"},
	{Name: "owned by someone else", Body: `{"version":"1.0","broker":"http://b"}`, DirMode: 0o700, FileMode: 0o600, UID: -2, Unsafe: true, Want: "owned by"},
	{Name: "higher major", Body: `{"version":"2.0","broker":"http://b"}`, DirMode: 0o700, FileMode: 0o600, Want: "version"},
	{Name: "no broker", Body: `{"version":"1.0"}`, DirMode: 0o700, FileMode: 0o600, Want: "names no broker"},
	{Name: "not json", Body: `nope`, DirMode: 0o700, FileMode: 0o600, Want: "decoding"},
}

func TestLoadNodeRefusesUnsafeOrUnusableFiles(t *testing.T) {
	for _, tc := range loadFailures {
		t.Run(tc.Name, func(t *testing.T) {
			home := writeConfig(t, NodeFile, tc.Body, tc.DirMode, tc.FileMode)

			uid := os.Getuid()
			if tc.UID != 0 {
				uid += 1000
			}

			_, err := LoadNode(home, uid)
			if err == nil || !strings.Contains(err.Error(), tc.Want) {
				t.Fatalf("LoadNode error = %v, want %q", err, tc.Want)
			}

			if errors.Is(err, ErrUnsafe) != tc.Unsafe {
				t.Errorf("errors.Is(ErrUnsafe) = %v, want %v", !tc.Unsafe, tc.Unsafe)
			}
		})
	}
}

func TestLoadBrokerReadsTheListenAddress(t *testing.T) {
	home := writeConfig(t, BrokerFile, `{"version":"1.0","listen":"127.0.0.1:9","store":"/s"}`, 0o700, 0o600)

	got, err := LoadBroker(home, os.Getuid())
	if err != nil || got.Listen != "127.0.0.1:9" || got.Store != "/s" {
		t.Errorf("LoadBroker = %+v, %v", got, err)
	}
}

func TestEnvironmentNeverMovesTheDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/elsewhere")
	t.Setenv("HOME", "/elsewhere")

	if got := Dir("/home/u"); got != "/home/u/.config/nitipan" {
		t.Errorf("Dir = %s", got)
	}
}
