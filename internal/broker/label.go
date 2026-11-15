package broker

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/wire"
)

// LabelDerivation versions the label derivation. The seen-label table stores it beside each label,
// so a later derivation can keep old labels valid.
const LabelDerivation = 1

// labelDomain keeps a label from equalling a handle derived from the same inputs.
const labelDomain = "nitipan-machine-label-v1:"

// DeriveLabel returns the four-word label of a machine ID and numeric uid. Only the broker derives
// labels, so nodes on different versions cannot disagree.
func DeriveLabel(machineID string, uid int) string {
	return handle.Derive(labelDomain+machineID, strconv.Itoa(uid))
}

// NodeKey identifies a node: a machine and the OS user the node runs as.
type NodeKey struct {
	MachineID string
	UID       int
}

// String formats the key for the store.
func (k NodeKey) String() string {
	return k.MachineID + "/" + strconv.Itoa(k.UID)
}

// Errors for addresses and labels.
var (
	// ErrUnknownLabel means no node with that label ever registered, so an address with it names
	// nothing.
	ErrUnknownLabel = errors.New("the broker has never seen this machine label")

	// ErrBadAddress means the text is not <handle>@<label>.
	ErrBadAddress = errors.New("not a <handle>@<label> address")

	// ErrLabelCollision means two machines derive the same label.
	ErrLabelCollision = errors.New("two machines derive the same label")
)

// Address is a validated <handle>@<label> address.
type Address struct {
	Handle string
	Label  string

	// Machine is the table row of the label.
	Machine LabelRecord
}

// ValidateAddress checks an address against the seen-label table. A label that was never seen is
// refused, so mail is never queued for a machine that may not exist.
func (b *Broker) ValidateAddress(addr string) (Address, error) {
	local, label, found := strings.Cut(addr, "@")
	if !found || local == "" || label == "" || strings.Contains(label, "@") {
		return Address{}, fmt.Errorf("%q: %w", addr, ErrBadAddress)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	row, ok := b.store.Data().Labels[label]
	if !ok {
		return Address{}, fmt.Errorf("label %q: %w", label, ErrUnknownLabel)
	}

	return Address{Handle: local, Label: label, Machine: row}, nil
}

// FormatAddress writes the <handle>@<label> qualified name. A holder with no handle reads as "-", which
// ValidateAddress refuses as an address, since nothing could be sent to it.
func FormatAddress(handle, label string) string {
	if handle == "" {
		handle = "-"
	}

	return handle + "@" + label
}

func describeMachine(r LabelRecord) string {
	return fmt.Sprintf("%s (machine %s, uid %d, user %s)", r.Host, r.MachineID, r.UID, r.User)
}

// CheckListen applies the listen address rules. Without TLS the broker listens only on a loopback or
// private address. A wildcard bind is public, since it listens on every interface.
func CheckListen(addr string, tls bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}

	if tls || wire.ClassifyHost(host) != wire.HostPublic {
		return nil
	}

	return fmt.Errorf("listen address %q is public or a wildcard, and no certificate is configured. "+
		"Set cert_file and key_file, or listen on a loopback or private address", addr)
}
