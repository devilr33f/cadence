package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

func TestClientVerifiesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	if _, err := httpx.Client(nil).Get(srv.URL); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if _, err := httpx.Client(srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs).Get(srv.URL); err != nil {
		t.Fatalf("trusted certificate refused: %v", err)
	}
}

func TestClientRefusesRedirect(t *testing.T) {
	var hits int
	other := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits++ }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusMovedPermanently)
	}))
	defer srv.Close()

	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	_, err := httpx.Client(pool).Get(srv.URL)
	if !errors.Is(err, httpx.ErrRedirect) {
		t.Fatalf("err = %v", err)
	}
	if hits != 0 {
		t.Fatal("redirect target was hit")
	}
}
