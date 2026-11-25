package media

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// the bounds of one fetch (M8-D4)
const (
	maxArtBytes = 5 << 20
	artTimeout  = 5 * time.Second
)

var artTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

var (
	errArtURL      = errors.New("artwork: url refused")
	errArtStatus   = errors.New("artwork: status refused")
	errArtType     = errors.New("artwork: type refused")
	errArtTooLarge = errors.New("artwork: body over the cap")
)

// Artwork fetches the cover art a media session names. The url comes from a
// device or a cast sender, so it is hostile: https on port 443 only, its name
// resolved once and only the checked addresses dialed (through the
// environment's proxy, asked for the address, never the name), no redirect, a deadline and a size cap. Images, and the
// failures of the last 30 s, stay in memory (D11).
type Artwork struct {
	client  *http.Client
	tls     *tls.Config
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
	// proxy names the http proxy for a host, nil for none; proxyDial reaches it
	proxy     func(host string) *url.URL
	proxyDial func(ctx context.Context, network, address string) (net.Conn, error)
	timeout   time.Duration
	cache     *artCache
	uploads   *artCache
	failed    *failMemo
	flight    singleflight.Group
	now       func() time.Time
	// Own answers a url that names the server's own image (its image proxy's, which a screen's session carries)
	// from memory, handled false for any other; nil fetches every url
	Own func(ctx context.Context, rawURL string) (contentType string, data []byte, handled bool, err error)
}

// NewArtwork verifies against the system roots (SSL_CERT_FILE).
func NewArtwork() *Artwork {
	a := newArtwork(nil, lookup, guardedDialer().DialContext, artTimeout)
	a.proxy = envProxy
	return a
}

func newArtwork(
	roots *x509.CertPool,
	resolve func(context.Context, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
	timeout time.Duration,
) *Artwork {
	a := &Artwork{
		tls:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		resolve: resolve, dial: dial, timeout: timeout, cache: newArtCache(),
		proxy: func(string) *url.URL { return nil }, proxyDial: (&net.Dialer{}).DialContext,
		uploads: newArtCacheOf(uploadEntries, uploadBytes), failed: newFailMemo(), now: time.Now,
	}
	a.client = &http.Client{
		Transport: &http.Transport{
			// the connect and the handshake are one step, on one deadline
			DialTLSContext:         a.dialTLS,
			DisableCompression:     true,
			MaxResponseHeaderBytes: 64 << 10,
			IdleConnTimeout:        30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return httpx.ErrRedirect },
	}
	return a
}

// artworkHash is the hex SHA-256 of an artwork url, "" for none: the key
// clients and the cache know an image by.
func artworkHash(u string) string {
	if u == "" {
		return ""
	}
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}

// get answers the image rawURL names, or its failure of the last 30 s, from
// memory, else fetches it once for every caller asking at the same time.
func (a *Artwork) get(rawURL string) (artImage, error) {
	if a.Own != nil {
		if ct, data, ok, err := a.Own(context.Background(), rawURL); ok {
			if err != nil || len(data) == 0 {
				return artImage{}, errArtStatus
			}
			return artImage{contentType: ct, data: data}, nil
		}
	}
	k := sha256.Sum256([]byte(rawURL))
	if img, ok := a.cache.get(k); ok {
		return img, nil
	}
	if err := a.failed.get(k, a.now()); err != nil {
		return artImage{}, err
	}
	v, err, _ := a.flight.Do(string(k[:]), func() (any, error) {
		if img, ok := a.cache.get(k); ok {
			return img, nil
		}
		img, err := a.fetch(rawURL)
		if err != nil {
			a.failed.put(k, err, a.now())
			return img, err
		}
		a.cache.put(k, img)
		return img, nil
	})
	if err != nil {
		return artImage{}, err
	}
	return v.(artImage), nil
}

// fetch runs on its own deadline, not a caller's: other callers may wait on it.
func (a *Artwork) fetch(rawURL string) (artImage, error) {
	u, err := url.Parse(rawURL)
	// port 443 only (ruling M8-R3): another port is a service, not a cover host
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		(u.Port() != "" && u.Port() != "443") {
		return artImage{}, errArtURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	req, err := http.NewRequestWithContext(context.WithValue(ctx, fetchDeadline{}, deadline), http.MethodGet, u.String(), nil)
	if err != nil {
		return artImage{}, errArtURL
	}
	req.Header.Set("Accept", strings.Join(artTypes, ", "))
	resp, err := a.client.Do(req)
	if err != nil {
		return artImage{}, fetchFailure(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return artImage{}, errArtStatus
	}
	typ, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	// nothing is decoded, so an encoded body would be served as the image
	enc := resp.Header.Get("Content-Encoding")
	if err != nil || !slices.Contains(artTypes, typ) || (enc != "" && enc != "identity") {
		return artImage{}, errArtType
	}
	if resp.ContentLength > maxArtBytes {
		return artImage{}, errArtTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtBytes+1))
	if err != nil {
		return artImage{}, fetchFailure(ctx, err)
	}
	if len(data) > maxArtBytes {
		return artImage{}, errArtTooLarge
	}
	return artImage{contentType: typ, data: data}, nil
}

// fetchFailure names a failed request by its deadline first: a dial or read
// cut by it fails with whatever error it was in.
func fetchFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// artReason is the log's reason token for a failed fetch; never the error's
// text, which can carry the url.
func artReason(err error) string {
	var verify *tls.CertificateVerificationError
	var header tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.Is(err, errArtURL):
		return "url"
	case errors.Is(err, errArtAddress):
		return "address"
	case errors.Is(err, errArtDNS):
		return "dns"
	case errors.Is(err, errArtConnect):
		return "connect"
	case errors.Is(err, httpx.ErrRedirect):
		return "redirect"
	case errors.Is(err, errArtStatus):
		return "status"
	case errors.Is(err, errArtType):
		return "type"
	case errors.Is(err, errArtTooLarge):
		return "too_large"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &verify), errors.As(err, &header), errors.As(err, &alert):
		return "tls"
	}
	return "failed"
}

// Image is the image a hostile https url names, fetched under the same
// guard, cap and memory as a session's cover (the catalog's image proxy
// fetches posters through it); the error is never the url's.
func (a *Artwork) Image(rawURL string) (contentType string, data []byte, err error) {
	img, err := a.get(rawURL)
	if err != nil {
		return "", nil, errors.New("artwork: " + artReason(err))
	}
	return img.contentType, img.data, nil
}
