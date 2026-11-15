package cli

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/oidc/oidctest"
)

type brokerConfigTest struct {
	Name string

	Listen string
	TLS    bool

	// Refusal is what turning cross-user features on says. Empty means it turns them on.
	Refusal string
}

var brokerConfigTests = []brokerConfigTest{
	{Name: "loopback listener in plain HTTP", Listen: "127.0.0.1:8443"},
	{Name: "private listener serving TLS", Listen: "10.0.0.5:8443", TLS: true},
	{Name: "private listener in plain HTTP", Listen: "10.0.0.5:8443", Refusal: "requires TLS or a loopback address"},
}

// TestTheBrokerConfigFileReachesTheBroker proves the broker run takes its identity provider, listen
// address, and TLS setting from its config file. The sign-in page answers, a sign-in exchanges its code
// with the configured client secret over the configured CA file, and turning cross-user features on
// judges the configured listener. It catches a config block the broker parses and never passes on, which
// left the page unmounted and the switch refusing for want of an identity provider.
func TestTheBrokerConfigFileReachesTheBroker(t *testing.T) {
	for _, test := range brokerConfigTests {
		t.Run(test.Name, func(t *testing.T) {
			iss := oidctest.New(t, "coworker-sub", "coworker@example.com")
			iss.ClientSecret = "s3cret"

			cfg := config.Broker{
				Version: "1.0",
				Listen:  test.Listen,
				OIDC: &config.OIDCConfig{
					Issuer:       iss.URL,
					ClientID:     iss.ClientID,
					ClientSecret: iss.ClientSecret,
					RedirectURL:  "https://broker.test/callback",
					CAFile:       iss.CAFile,
				},
			}

			if test.TLS {
				cfg.CertFile, cfg.KeyFile = "/etc/nitipan/cert.pem", "/etc/nitipan/key.pem"
			}

			path := filepath.Join(t.TempDir(), "broker.json")

			store, err := broker.OpenStore(path, broker.StoreOptions{Volume: okVolume{}})
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = store.Close() })

			b := broker.New(brokerConfig(cfg, store, path, slog.New(slog.DiscardHandler)))

			srv := httptest.NewServer(b.Handler())
			t.Cleanup(srv.Close)

			page := oidctest.NewBrowser(t, srv.URL, "", iss)

			if status, body := page.Get("/signin"); status != http.StatusOK {
				t.Fatalf("the sign-in page answered %d: %s", status, body)
			}

			status, body := page.SignIn("bind")
			if status != http.StatusOK || !strings.Contains(body, "Pending Admission") {
				t.Fatalf("the sign-in callback answered %d, want the pending admission page: %s", status, body)
			}

			_, err = b.SetCrossUserEnabled(true)

			switch {
			case test.Refusal == "" && err != nil:
				t.Fatalf("turning cross-user features on: %v", err)
			case test.Refusal != "" && (err == nil || !strings.Contains(err.Error(), test.Refusal)):
				t.Fatalf("turning cross-user features on: got %v, want a refusal saying %q", err, test.Refusal)
			case err != nil && strings.Contains(err.Error(), "no identity provider"):
				t.Fatalf("the broker saw no identity provider: %v", err)
			}
		})
	}
}
