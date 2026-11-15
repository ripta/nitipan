package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/oidc/oidctest"
)

var (
	pairingCodePattern = regexp.MustCompile(`Pairing code: (\S+)`)
	pairedLabelPattern = regexp.MustCompile(`Label: (\S+)`)
)

// TestNodePairBindsAnUnboundMachineThroughThePage proves nitipan node pair reaches a real broker over the
// node's own TLS setup while cross-user features are on and the label is still unbound, prints the label
// the broker names, and that the page binds the label with the code it printed. It catches a pair command
// that sends no machine headers, skips the node's CA file, or is refused for want of the very binding it
// exists to make.
func TestNodePairBindsAnUnboundMachineThroughThePage(t *testing.T) {
	iss := oidctest.New(t, "coworker-sub", "coworker@example.com")
	oidcCfg := iss.Config("https://broker.test/callback")

	f := newWideFixtureConfig(t, true, nil, func(c *broker.Config) { c.OIDCConfig = &oidcCfg })

	coworker, err := f.b.AdmitUser(iss.URL, "coworker-sub", "coworker@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	m := f.machine("coworker-mac")
	m.start()

	label := m.labelOf(t)

	if _, err := f.b.SetCrossUserEnabled(true); err != nil {
		t.Fatalf("turning cross-user features on: %v", err)
	}

	res := m.w.run(m.w.terminal(70), "node", "pair")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "Label: "+label)

	match := pairingCodePattern.FindStringSubmatch(res.Stdout)
	if match == nil {
		t.Fatalf("node pair printed no pairing code:\n%s", res.Stdout)
	}

	page := oidctest.NewBrowser(t, f.srv.URL, f.caFile, iss)

	status, body := page.Submit("bind", url.Values{"label": {label}, "code": {match[1]}})
	if status != http.StatusOK {
		t.Fatalf("binding on the page with the printed code: %d %s", status, body)
	}

	if got := boundUser(t, f.b, label); got != coworker.ID {
		t.Fatalf("%s is bound to %q, want the coworker %s", label, got, coworker.ID)
	}
}

// boundUser returns the user the bindings list names for label, through the admin socket's handler.
func boundUser(t *testing.T, b *broker.Broker, label string) string {
	t.Helper()

	w := httptest.NewRecorder()
	b.AdminHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, broker.AdminPathBindings, nil))

	var ans struct {
		Bindings []struct {
			Label  string `json:"label"`
			UserID string `json:"user_id"`
		} `json:"bindings"`
	}

	if err := json.NewDecoder(w.Body).Decode(&ans); err != nil {
		t.Fatal(err)
	}

	for _, bnd := range ans.Bindings {
		if bnd.Label == label {
			return bnd.UserID
		}
	}

	return ""
}

// serveAdmin serves the broker's admin socket at a path short enough for a unix socket, and returns it.
func serveAdmin(t *testing.T, b *broker.Broker) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "admin.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- b.ServeAdmin(ctx, sock) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Log(err)
		}
	})

	waitFor(t, "the admin socket", func() bool {
		_, err := os.Stat(sock)

		return err == nil
	})

	return sock
}

// TestBrokerCrossUserPrintsTheUnboundLabelsBeforeTurningOn proves nitipan broker cross-user true lists
// every label nobody bound before it says cross-user features are on, since each is refused from then on.
// It catches a command whose list reaches only the broker's log.
func TestBrokerCrossUserPrintsTheUnboundLabelsBeforeTurningOn(t *testing.T) {
	f := newWideFixtureConfig(t, true, nil, func(c *broker.Config) {
		c.OIDCConfig = &oidc.Config{Issuer: "https://issuer.test", ClientID: "c", RedirectURL: "https://broker.test/callback"}
	})

	m := f.machine("lease-only")
	m.start()

	label := m.labelOf(t)
	sock := serveAdmin(t, f.b)

	var stdout, stderr bytes.Buffer

	d := m.w.deps(m.w.terminal(70), &stdout, &stderr)
	d.AdminSocket = sock

	res := result{Status: Execute(d, []string{"broker", "cross-user", "true"}), Stdout: stdout.String(), Stderr: stderr.String()}
	wantStatus(t, res, exitcode.OK)

	listed := strings.Index(res.Stdout, label)
	on := strings.Index(res.Stdout, "cross-user features are on")

	if listed < 0 || on < 0 || listed > on {
		t.Fatalf("want %s listed before the result line:\n%s", label, res.Stdout)
	}
}
