package node

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestARedirectFromHTTPSToPlainHTTPNeverCarriesTheCredential(t *testing.T) {
	var leaked atomic.Bool

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	base, err := url.Parse(secure.URL)
	if err != nil {
		t.Fatal(err)
	}

	c := &client{base: base, http: newHTTPClient(secure.Client().Transport), key: "secret", machineID: "m1", uid: 501}

	err = c.call(context.Background(), "/v1/ping", struct{}{}, nil)
	if !errors.Is(err, ErrRedirect) {
		t.Errorf("call = %v, want ErrRedirect", err)
	}

	if leaked.Load() {
		t.Error("the credential was re-sent over plain HTTP after a redirect")
	}
}
