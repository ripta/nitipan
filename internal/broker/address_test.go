package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/tlstest"
	"github.com/space-armada/nitipan/internal/wire"
)

func TestValidateAddressRefusesALabelTheBrokerNeverSaw(t *testing.T) {
	h := newHarness(t, nil)

	reg := h.register(alice)

	got, err := h.b.ValidateAddress("worker-1@" + reg.Label)
	if err != nil || got.Handle != "worker-1" || got.Machine.MachineID != "machine-a" || got.Machine.UID != 501 {
		t.Fatalf("seen label: %+v, %v", got, err)
	}

	_, err = h.b.ValidateAddress("worker-1@never-seen-label-here")
	if !errors.Is(err, ErrUnknownLabel) {
		t.Errorf("unseen label = %v, want ErrUnknownLabel", err)
	}
}

type addressCase struct {
	Addr string
}

var badAddresses = []addressCase{{""}, {"nolabel"}, {"@label"}, {"handle@"}, {"a@b@c"}}

func TestValidateAddressRefusesMalformedText(t *testing.T) {
	h := newHarness(t, nil)

	for _, tc := range badAddresses {
		t.Run(tc.Addr, func(t *testing.T) {
			if _, err := h.b.ValidateAddress(tc.Addr); !errors.Is(err, ErrBadAddress) {
				t.Errorf("ValidateAddress(%q) = %v, want ErrBadAddress", tc.Addr, err)
			}
		})
	}
}

func TestTwoMachinesThatCollideOnALabelAreRefusedAndBothAreNamed(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Derive = func(string, int) string { return "same-label-for-both-nodes" }
	})

	if _, e := h.tryRegister(alice); e != nil {
		t.Fatalf("first node: %v", e)
	}

	_, e := h.tryRegister(bob)
	if e == nil || e.Code != wire.CodeLabelConflict {
		t.Fatalf("second node = %v, want a label collision", e)
	}

	for _, want := range []string{"machine-a", "machine-b", "host-machine-a", "host-machine-b", "same-label-for-both-nodes"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("collision message %q does not name %q", e.Message, want)
		}
	}

	if _, seen := h.store.Data().Nodes[bob.key().String()]; seen {
		t.Error("the colliding node was registered")
	}
}

func TestTheSameNodeRegisteringAgainIsNotACollision(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Derive = func(string, int) string { return "fixed-label-for-test-x" }
	})

	h.register(alice)

	if _, e := h.tryRegister(alice); e != nil {
		t.Errorf("re-register: %v", e)
	}
}

type listenCase struct {
	Name string
	Addr string
	TLS  bool
	Err  bool
}

var listenCases = []listenCase{
	{"loopback without TLS", "127.0.0.1:8443", false, false},
	{"IPv6 loopback without TLS", "[::1]:8443", false, false},
	{"localhost without TLS", "localhost:8443", false, false},
	{"private without TLS", "10.0.0.5:8443", false, false},
	{"public without TLS", "203.0.113.9:8443", false, true},
	{"IPv4 wildcard without TLS", "0.0.0.0:8443", false, true},
	{"bare port is a wildcard", ":8443", false, true},
	{"IPv6 wildcard without TLS", "[::]:8443", false, true},
	{"wildcard with TLS", "0.0.0.0:8443", true, false},
	{"public with TLS", "203.0.113.9:8443", true, false},
	{"no port", "127.0.0.1", false, true},
}

func TestCheckListenRefusesPublicAndWildcardAddressesWithoutACertificate(t *testing.T) {
	for _, tc := range listenCases {
		t.Run(tc.Name, func(t *testing.T) {
			if err := CheckListen(tc.Addr, tc.TLS); (err != nil) != tc.Err {
				t.Errorf("CheckListen(%q, %v) = %v, want error %v", tc.Addr, tc.TLS, err, tc.Err)
			}
		})
	}
}

func TestServeRefusesAWildcardWithoutACertificateBeforeListening(t *testing.T) {
	h := newHarness(t, nil)

	err := h.b.Serve(t.Context(), "0.0.0.0:0", "", "")
	if err == nil || !strings.Contains(err.Error(), "no certificate") {
		t.Errorf("Serve = %v", err)
	}

	if err := h.b.Serve(t.Context(), "127.0.0.1:0", "cert.pem", ""); err == nil {
		t.Error("a certificate without a key was accepted")
	}
}

func TestBrokerServesTLSWhenGivenACertificateAndAClientVerifiesIt(t *testing.T) {
	h := newHarness(t, nil)
	files := tlstest.SelfSigned(t, t.TempDir())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- h.b.serveOn(ctx, ln, files.Cert, files.Key) }()

	pem, err := os.ReadFile(files.Cert)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)

	trusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	url := "https://" + ln.Addr().String() + wire.PathPing

	resp, err := trusting.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("a client that trusts the certificate: %v", err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without a key", resp.StatusCode)
	}

	if _, err := http.Post(url, "application/json", strings.NewReader(`{}`)); err == nil {
		t.Error("a client with the system roots accepted a self-signed certificate")
	}

	cancel()

	if err := <-done; err != nil {
		t.Errorf("serve after cancel = %v", err)
	}
}
