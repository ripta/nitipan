package setup

import (
	"fmt"
	"html"
	"path/filepath"
)

// Names of the daemons setup prints a LaunchAgent for. Like Git, they are not agents.
const (
	Node   = "node"
	Broker = "broker"
)

// Daemons lists the daemon names setup accepts.
func Daemons() []string {
	return []string{Node, Broker}
}

// DaemonInput is what a LaunchAgent text depends on. Every path is absolute and resolved.
type DaemonInput struct {
	// Executable is the stable nitipan path on PATH. A versioned install path would not survive an
	// upgrade, and the node exits when its executable changes so launchd restarts it on the new one.
	Executable string

	// Home is the home directory from the user database.
	Home string

	UID int
}

// DaemonLabel is the launchd label of a daemon.
func DaemonLabel(daemon string) string {
	return "dev.nitipan." + daemon
}

// PlistPath is where the user saves the plist.
func PlistPath(in DaemonInput, daemon string) string {
	return filepath.Join(in.Home, "Library", "LaunchAgents", DaemonLabel(daemon)+".plist")
}

// NodeText returns the LaunchAgent that runs `nitipan node`, and the commands that load it.
func NodeText(in DaemonInput) string {
	return daemonText(in, Node, fmt.Sprintf(`The node mirrors this machine's leases to the broker, and forwards the requests that sessions hand it.
Run one node for each OS user on the machine.

Create %[1]s with the broker address:

  {"version": "1.0", "broker": "https://broker.example:8443"}

Add "proxy" for an HTTP proxy, and "ca_file" for a private certificate authority. The node reads only this
file, not the environment, and refuses a file that another user owns or that others can write.
`, filepath.Join(in.Home, ".config", "nitipan", "node.json")))
}

// BrokerText returns the LaunchAgent that runs `nitipan broker`, and the commands that load it.
func BrokerText(in DaemonInput) string {
	return daemonText(in, Broker, fmt.Sprintf(`Run the broker on one host that every machine can reach. Run it on a host of your own, never a hosted relay.

Create %[1]s:

  {"version": "1.0", "listen": "127.0.0.1:8443", "cert_file": "/path/cert.pem", "key_file": "/path/key.pem"}

Without a certificate and key the broker refuses to listen on a public or wildcard address. "store" sets the
store file, which must be on a local volume that no sync service owns. The default is under
~/Library/Application Support/nitipan.

A new machine can send leases, but neither send nor receive mail until you approve it. On this host, run
nitipan broker pending to list the keys that wait, then nitipan broker approve <label> <fingerprint> with the
fingerprint the node prints (nitipan node fingerprint). Run nitipan broker forget <label> after a machine is
wiped or reinstalled.

Those commands reach the broker through a unix socket in the store's directory, and only for the OS user the
broker runs as. No agent session may run as that OS user on this host. A session that did could approve its
own machine, which is the proof the approval exists to require.
`, filepath.Join(in.Home, ".config", "nitipan", "broker.json")))
}

func daemonText(in DaemonInput, daemon, intro string) string {
	plist := PlistPath(in, daemon)
	logPath := filepath.Join(in.Home, "Library", "Logs", "nitipan", daemon+".log")

	return fmt.Sprintf(`%[1]s
Save this as %[2]s:

<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%[3]s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%[4]s</string>
    <string>%[5]s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%[6]s</string>
  <key>StandardErrorPath</key>
  <string>%[6]s</string>
</dict>
</plist>

Then load it:

  mkdir -p %[7]s
  launchctl bootstrap gui/%[8]d %[2]s

To stop it:

  launchctl bootout gui/%[8]d/%[3]s

nitipan prints these steps and writes nothing.
`, intro, plist, DaemonLabel(daemon), html.EscapeString(in.Executable), daemon, html.EscapeString(logPath),
		filepath.Dir(logPath), in.UID)
}
