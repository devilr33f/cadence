// Package httpx builds the HTTP client every outbound provider call uses:
// verified TLS and no redirects, so a key never follows a redirect.
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

var ErrRedirect = errors.New("httpx: redirect refused")

const dialTimeout = 10 * time.Second

// Client verifies against roots, or the system pool when nil. It sets no
// overall timeout, so streaming bodies are bounded by the request context.
// A request made under a recording span gets a client span and a traceparent
// (tracing.Transport); any other passes as it is.
func Client(roots *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: tracing.Transport(&http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout: dialTimeout,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
}
