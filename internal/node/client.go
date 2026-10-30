package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/wire"
)

// Errors for the broker address.
var (
	// ErrPlainPublic means the config names a plain HTTP broker at a public address.
	ErrPlainPublic = errors.New("plain HTTP to a public address is refused")

	// ErrBadBroker means the config's broker URL cannot be used.
	ErrBadBroker = errors.New("broker address cannot be used")
)

// brokerURL parses the broker address and applies the address rules. It reports whether the node
// will use HTTPS.
//
// Plain HTTP to a literal public IP is refused here. A name is allowed, since it may be a private
// name. The transport checks the address a name resolves to when it dials.
func brokerURL(c config.Node) (u *url.URL, https bool, err error) {
	u, err = url.Parse(c.Broker)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false, fmt.Errorf("%q: want http://host:port or https://host:port: %w", c.Broker, ErrBadBroker)
	}

	if u.Scheme == "https" {
		return u, true, nil
	}

	if ip := net.ParseIP(u.Hostname()); ip != nil && wire.ClassifyIP(ip) == wire.HostPublic {
		return nil, false, fmt.Errorf("%s is a public address. Use https, or an address on a loopback or private network: %w",
			c.Broker, ErrPlainPublic)
	}

	return u, false, nil
}

// proxyFunc builds the node's proxy lookup from its config. Go's default lookup reads the
// environment, which a LaunchAgent does not inherit and a session could set, so the node never uses
// it.
func proxyFunc(c config.Node) (func(*http.Request) (*url.URL, error), error) {
	if c.Proxy == "" {
		return func(*http.Request) (*url.URL, error) { return nil, nil }, nil
	}

	p, err := url.Parse(c.Proxy)
	if err != nil || p.Host == "" {
		return nil, fmt.Errorf("proxy %q: want a URL such as http://proxy:3128", c.Proxy)
	}

	return http.ProxyURL(p), nil
}

// newTransport builds the transport. The broker's certificate is verified against the system roots,
// or against the CA file when the config names one.
//
// With plain HTTP, the dialer refuses a connection to a public address after the name resolves, so a
// private-looking name that resolves to a public address, or that changes its answer later, is
// refused too. The check covers whatever the transport dials, which is the proxy when one is set.
func newTransport(c config.Node, plain bool) (*http.Transport, error) {
	proxy, err := proxyFunc(c)
	if err != nil {
		return nil, err
	}

	t := &http.Transport{
		Proxy:               proxy,
		TLSHandshakeTimeout: 10 * time.Second,

		// The stream's headers arrive at once, so this bounds the wait to open it without cutting an
		// open stream short.
		ResponseHeaderTimeout: callTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}

	if plain {
		t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refusePublicDial}).DialContext
	}

	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading the CA file: %w", err)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", c.CAFile)
		}

		t.TLSClientConfig.RootCAs = pool
	}

	return t, nil
}

// callTimeout bounds every call except the stream.
var callTimeout = 30 * time.Second

// refusePublicDial is a dialer control function. It sees the resolved address just before the
// connection is made.
func refusePublicDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("dialing %q: %w", address, err)
	}

	if wire.ClassifyIP(net.ParseIP(host)) == wire.HostPublic {
		return fmt.Errorf("refusing plain HTTP to the public address %s. Use https: %w", host, ErrPlainPublic)
	}

	return nil
}

// client talks to the broker with the node's credential on every request.
type client struct {
	base *url.URL
	http *http.Client
	key  string

	machineID string
	uid       int
}

func (c *client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/") + path

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("building the %s request: %w", path, err)
	}

	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set(wire.HeaderMachine, c.machineID)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(c.uid))
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// call posts a message and decodes the answer into out when it is not nil. The whole call has a
// deadline, so a broker that accepts the connection and never answers cannot stall the session.
func (c *client) call(ctx context.Context, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encoding the %s request: %w", path, err)
	}

	req, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s: %w", path, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return decodeError(path, resp)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the %s answer: %w", path, err)
	}

	return nil
}

func decodeError(path string, resp *http.Response) error {
	var e wire.Error

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return fmt.Errorf("calling %s: %w", path, &e)
	}

	return fmt.Errorf("calling %s: broker answered %s", path, resp.Status)
}
