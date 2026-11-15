package oidctest

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Browser is a browser on the broker's sign-in page. It follows no redirect, so each hop of a sign-in is
// the test's to take.
type Browser struct {
	// Header is the header of the broker's last answer.
	Header http.Header

	t      *testing.T
	broker string
	http   *http.Client
	iss    *Issuer
}

// NewBrowser opens the page at brokerURL. It trusts caFile for the broker, or the system roots when
// caFile is empty.
func NewBrowser(t *testing.T, brokerURL, caFile string, iss *Issuer) *Browser {
	t.Helper()

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatal(err)
		}

		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		tlsCfg.RootCAs = pool
	}

	return &Browser{t: t, broker: brokerURL, iss: iss, http: &http.Client{
		Transport:     &http.Transport{TLSClientConfig: tlsCfg},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (p *Browser) read(resp *http.Response) (status int, body string) {
	p.t.Helper()

	defer resp.Body.Close()

	p.Header = resp.Header

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatal(err)
	}

	return resp.StatusCode, string(data)
}

// Get fetches a page path and returns its status and body.
func (p *Browser) Get(path string) (status int, body string) {
	p.t.Helper()

	resp, err := p.http.Get(p.broker + path)
	if err != nil {
		p.t.Fatal(err)
	}

	return p.read(resp)
}

// Post submits a form to a page path and returns its status and body.
func (p *Browser) Post(path string, form url.Values) (status int, body string) {
	p.t.Helper()

	resp, err := p.http.PostForm(p.broker+path, form)
	if err != nil {
		p.t.Fatal(err)
	}

	return p.read(resp)
}

// SignIn starts the page action, signs in at the issuer, and returns the broker's answer to the callback.
func (p *Browser) SignIn(action string) (status int, body string) {
	p.t.Helper()

	resp, err := p.http.PostForm(p.broker+"/"+action, nil)
	if err != nil {
		p.t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		p.t.Fatalf("starting %s: status %d, want a redirect to the issuer", action, resp.StatusCode)
	}

	authorize := resp.Header.Get("Location")
	if !strings.HasPrefix(authorize, p.iss.URL+"/authorize?") {
		p.t.Fatalf("starting %s redirected to %q, not the configured issuer %s", action, authorize, p.iss.URL)
	}

	resp, err = p.iss.Client(p.t).Get(authorize)
	if err != nil {
		p.t.Fatal(err)
	}

	resp.Body.Close()

	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		p.t.Fatal(err)
	}

	return p.Get("/callback?" + back.RawQuery)
}

// Submit signs in for action, then submits the form the callback rendered with the given fields. It
// returns the broker's answer to the submit.
func (p *Browser) Submit(action string, fields url.Values) (status int, body string) {
	p.t.Helper()

	status, page := p.SignIn(action)
	if status != http.StatusOK {
		p.t.Fatalf("signing in for %s: status %d: %s", action, status, page)
	}

	form := url.Values{"token": {FormToken(p.t, page)}}
	for k, v := range fields {
		form[k] = v
	}

	return p.Post("/"+action+"-submit", form)
}

var formTokenPattern = regexp.MustCompile(`name="token" value="([0-9a-f]+)"`)

// FormToken plucks the single-use token from the form a callback rendered.
func FormToken(t *testing.T, page string) string {
	t.Helper()

	m := formTokenPattern.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the callback rendered no action form:\n%s", page)
	}

	return m[1]
}
