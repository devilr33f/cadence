package media

import (
	"context"
	"crypto/x509"
	"net"
	"net/netip"
	"net/url"
	"time"
)

// ArtworkRig is what a test owns of an Artwork: the roots that verify, the
// resolver, the dial of the pinned address, the deadline and the clock.
type ArtworkRig struct {
	Roots   *x509.CertPool
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, network, address string) (net.Conn, error)
	Timeout time.Duration
	Now     func() time.Time
	// Proxy names an http proxy for a host; ProxyDial reaches it
	Proxy     func(host string) *url.URL
	ProxyDial func(ctx context.Context, network, address string) (net.Conn, error)
}

func NewTestArtwork(r ArtworkRig) *Artwork {
	a := newArtwork(r.Roots, r.Resolve, r.Dial, r.Timeout)
	if r.Now != nil {
		a.now = r.Now
	}
	if r.Proxy != nil {
		a.proxy, a.proxyDial = r.Proxy, r.ProxyDial
	}
	return a
}

// GuardedDial is the server's own dial of a pinned address.
var GuardedDial = guardedDialer().DialContext

var ErrAddressRefused = errArtAddress
