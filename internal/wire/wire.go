// Package wire defines the messages between a node and the broker, and the server-sent events
// framing the broker streams them in.
//
// Every message carries a version, and every path carries a major prefix. A reader follows the minor
// rule of the record format: a field may be added only when its absence never makes the reader more
// permissive, and a field that fails that test takes a new message version, which an older reader
// refuses.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Paths the broker serves. The prefix carries the major version of the protocol.
const (
	Prefix        = "/v1"
	PathStream    = Prefix + "/stream"
	PathRegister  = Prefix + "/register"
	PathAck       = Prefix + "/ack"
	PathSnapshot  = Prefix + "/snapshot"
	PathHeartbeat = Prefix + "/heartbeat"
	PathPing      = Prefix + "/ping"
)

// Headers a node sends on every request after registration. The broker checks the bearer key
// against the key it registered for the machine and uid named here.
const (
	HeaderMachine = "X-Nitipan-Machine"
	HeaderUID     = "X-Nitipan-Uid"
)

// Version is a message version, written "major.minor".
type Version struct {
	Major int
	Minor int
}

// Current is the message version this binary writes and the highest it reads.
var Current = Version{Major: 1, Minor: 0}

// String formats the version as "major.minor".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// MarshalJSON implements json.Marshaler.
func (v Version) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Version) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("decoding message version: %w", err)
	}

	var major, minor int

	if _, err := fmt.Sscanf(s, "%d.%d", &major, &minor); err != nil || major < 0 || minor < 0 {
		return fmt.Errorf("message version %q: want major.minor", s)
	}

	*v = Version{Major: major, Minor: minor}

	return nil
}

// ErrVersion means a message carries a major version this binary does not read.
var ErrVersion = errors.New("message version is not supported")

// Check refuses a message of another major. A higher minor reads with its unknown fields ignored.
func (v Version) Check() error {
	if v.Major != Current.Major {
		return fmt.Errorf("message version %s, this binary reads %d.x: %w", v, Current.Major, ErrVersion)
	}

	return nil
}

// Error codes the broker returns in an Error body.
const (
	CodeUnauthorized  = "unauthorized"
	CodeKeyConflict   = "key_conflict"
	CodeLabelConflict = "label_collision"
	CodeBadRequest    = "bad_request"
	CodeVersion       = "version"
	CodeUnavailable   = "unavailable"
)

// Error is the body of every failed response.
type Error struct {
	Version Version `json:"version"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("broker refused (%s): %s", e.Code, e.Message)
}

// Register is the first call a node makes. The instance key travels in the Authorization header.
type Register struct {
	Version   Version `json:"version"`
	MachineID string  `json:"machine_id"`
	UID       int     `json:"uid"`

	// User and Host are for display only. They never decide identity.
	User string `json:"user"`
	Host string `json:"host"`

	// NodeVersion is the node binary's build version. HTTPS says whether the node reached the
	// broker over TLS.
	NodeVersion string `json:"node_version"`
	HTTPS       bool   `json:"https"`
}

// Registered answers a registration.
type Registered struct {
	Version Version `json:"version"`

	// Label is the four-word machine part of the node's qualified names. LabelVersion names the
	// derivation that produced it.
	Label        string `json:"label"`
	LabelVersion int    `json:"label_version"`

	BrokerVersion string `json:"broker_version"`

	// TermMillis is the term the broker uses. The node paces its heartbeat from it. KeepaliveMillis
	// is how often the stream carries a comment, so the node can tell a dead stream from a quiet one.
	TermMillis      int64 `json:"term_ms"`
	KeepaliveMillis int64 `json:"keepalive_ms"`
}

// MirroredLease is one lease of the machine's state directory, as the node saw it. A record the node
// can use travels unchanged. One it cannot use travels as an unknown holder with an all footprint.
type MirroredLease struct {
	ID      string          `json:"id"`
	Record  json.RawMessage `json:"record,omitempty"`
	Unknown *UnknownLease   `json:"unknown,omitempty"`
}

// UnknownLease stands for a record the node cannot use. Its footprint blocks everything on its
// machine, as the local claim path treats it.
type UnknownLease struct {
	Reason    string `json:"reason"`
	Version   string `json:"version,omitempty"`
	Footprint string `json:"footprint"`
}

// FootprintAll is the footprint of an unknown holder.
const FootprintAll = "all"

// Snapshot is the full set of the machine's leases. It replaces the last one.
type Snapshot struct {
	Version Version         `json:"version"`
	Leases  []MirroredLease `json:"leases"`
}

// Heartbeat tells the broker the node's stream is alive.
type Heartbeat struct {
	Version Version `json:"version"`
}

// Ack tells the broker the node handled an event, so it can drop it from the node's queue.
type Ack struct {
	Version Version `json:"version"`
	EventID string  `json:"event_id"`
}

// Ping asks the broker to echo the node's label. It proves a request reached the broker with the
// node's credential.
type Ping struct {
	Version Version `json:"version"`
}

// Pong answers a ping.
type Pong struct {
	Version Version `json:"version"`
	Label   string  `json:"label"`
}

// Event is one message the broker streams to a node. The ID carries the broker's boot ID, so an ID
// never repeats across a broker restart.
type Event struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// Host classes for the address rules.
const (
	HostLoopback = "loopback"
	HostPrivate  = "private"
	HostPublic   = "public"
)

// ClassifyHost says whether a host, as it appears in a URL or listen address, is loopback, private,
// or public. A wildcard is public, since it listens on every interface. A name other than localhost
// is public too, because answering would need a lookup that could change under the caller.
func ClassifyHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	if strings.EqualFold(host, "localhost") {
		return HostLoopback
	}

	return ClassifyIP(net.ParseIP(host))
}

// ClassifyIP is ClassifyHost for an address already resolved. A nil address is public.
func ClassifyIP(ip net.IP) string {
	switch {
	case ip == nil, ip.IsUnspecified():
		return HostPublic
	case ip.IsLoopback():
		return HostLoopback
	case ip.IsPrivate(), ip.IsLinkLocalUnicast():
		return HostPrivate
	default:
		return HostPublic
	}
}
