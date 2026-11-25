// Package probetest drives a provider module's Probe against fake HTTPS
// upstreams, for tests: every way an upstream can answer maps to its probe
// code, and no code carries the upstream's text or the key.
package probetest

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// Leak is in every body a failing fake answers, so a test can assert it never
// surfaces; Key is the credential a Build should configure.
const (
	Leak = "LEAKY-UPSTREAM-TEXT"
	Key  = "probe-key-do-not-print"
)

// Short bounds a probe in the timeout case.
const Short = 300 * time.Millisecond

type Upstream struct {
	Probe func(context.Context, any) error
	// Build makes the module's instance against base (the fake's https url,
	// no path) with Key, over client, which trusts the fake
	Build func(base string, client *http.Client) any
	// OK answers the probe's request as the upstream does; it checks the
	// request and fails t on a wrong one
	OK func(t *testing.T) http.HandlerFunc
	// AnyBody says the probe takes any 2xx without reading its body
	AnyBody bool
}

// HTTP runs every case against u.
func HTTP(t *testing.T, u Upstream) {
	t.Helper()
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":"`+Leak+`"}`)
		}
	}
	fixed := func(h http.HandlerFunc) func(*testing.T) http.HandlerFunc {
		return func(*testing.T) http.HandlerFunc { return h }
	}
	type upstreamCase struct {
		name string
		h    func(t *testing.T) http.HandlerFunc
		want string
	}
	cases := []upstreamCase{
		{"ok", u.OK, ""},
		{"unauthorized", fixed(status(http.StatusUnauthorized)), "auth"},
		{"forbidden", fixed(status(http.StatusForbidden)), "auth"},
		{"rate limited", fixed(status(http.StatusTooManyRequests)), "rate_limited"},
		{"server error", fixed(status(http.StatusBadGateway)), "bad_response"},
		{"redirect", fixed(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
		}), "bad_response"},
		{"slow", func(t *testing.T) http.HandlerFunc {
			return func(_ http.ResponseWriter, r *http.Request) {
				// the server notices the client leave only once the body is read
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-t.Context().Done():
				}
			}
		}, "timeout"},
	}
	if !u.AnyBody {
		cases = append(cases, upstreamCase{"garbage", fixed(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html>"+Leak)
		}), "bad_response"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(c.h(t))
			t.Cleanup(srv.Close)
			check(t, u, srv.URL, Client(srv), c.want)
		})
	}
	t.Run("refused", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.NotFoundHandler())
		client := Client(srv)
		srv.Close()
		check(t, u, srv.URL, client, "unreachable")
	})
}

func check(t *testing.T, u Upstream, base string, client *http.Client, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if want == "timeout" {
		ctx, cancel = context.WithTimeout(context.Background(), Short)
	}
	defer cancel()
	err := u.Probe(ctx, u.Build(base, client))
	Want(t, err, want)
}

// Want fails t unless err maps to code ("" for none) and carries neither
// Leak nor Key.
func Want(t *testing.T, err error, code string) {
	t.Helper()
	if got := providers.ProbeCode(err); got != code {
		t.Fatalf("code %q, want %q", got, code)
	}
	if err != nil && (strings.Contains(err.Error(), Leak) || strings.Contains(err.Error(), Key)) {
		t.Fatal("probe error carries upstream text or the key")
	}
}

// Client is an httpx client (verified TLS, no redirects) that trusts srv.
func Client(srv *httptest.Server) *http.Client {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return httpx.Client(p)
}
