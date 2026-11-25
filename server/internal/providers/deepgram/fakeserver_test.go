package deepgram_test

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

type upstream struct {
	srv    *httptest.Server
	base   string
	client *http.Client
}

// newUpstream upgrades every request; its cert is trusted only by the returned client.
func newUpstream(t *testing.T, h func(r *http.Request, c *websocket.Conn)) upstream {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		h(r, c)
	}))
	t.Cleanup(srv.Close)
	return upstream{srv: srv, base: "wss" + strings.TrimPrefix(srv.URL, "https"), client: httpx.Client(roots(srv))}
}

func roots(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

// redirector answers every request with a 301 to a second server that counts hits.
func redirector(t *testing.T) (base string, client *http.Client, hits *int) {
	t.Helper()
	hits = new(int)
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *hits++ }))
	t.Cleanup(other.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)
	return "wss" + strings.TrimPrefix(srv.URL, "https"), httpx.Client(roots(srv)), hits
}
