package media_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

const (
	artHost = "art.example.com"
	artURL  = "https://" + artHost + "/covers/c.jpg"
	artPath = "/api/v1/devices/" + devID + "/artwork"
	artCap  = 5 << 20
)

var (
	public4 = netip.MustParseAddr("93.184.215.14")
	public6 = netip.MustParseAddr("2606:4700:4700::1111")
	cover   = []byte("\xff\xd8\xff\xe0 a cover")
)

// upstream is the art host: a TLS server (its certificate names *.example.com)
// whose name the rig's resolver answers and whose pinned address the rig's
// dial connects to, unless the test makes that address fail or hang.
type upstream struct {
	srv *httptest.Server
	// bytes the fetches read from the art host's connections
	read atomic.Int64

	mu sync.Mutex
	// the resolver's answers in turn; the last one repeats
	answers [][]netip.Addr
	lookups int
	dialed  []string
	hits    map[string]int
	// pinned addresses whose dial fails at once, or hangs until its context ends
	refuse, hang map[string]bool
	// closed when a hanging dial returns
	hangEnded chan struct{}
	clock     time.Time
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	return newUpstreamTLS(t, h, nil)
}

// newUpstreamTLS is newUpstream serving with cfg (nil: httptest's own
// certificate)
func newUpstreamTLS(t *testing.T, h http.HandlerFunc, cfg *tls.Config) *upstream {
	t.Helper()
	u := &upstream{
		answers: [][]netip.Addr{{public4}}, hits: map[string]int{}, refuse: map[string]bool{}, hang: map[string]bool{},
		clock: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
	}
	u.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits[r.URL.Path]++
		u.mu.Unlock()
		h(w, r)
	}))
	// the refused handshakes some tests make are no news
	u.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	u.srv.TLS = cfg
	u.srv.StartTLS()
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if host != artHost {
		return nil, errors.New("no such host")
	}
	a := u.answers[min(u.lookups, len(u.answers)-1)]
	u.lookups++
	return a, nil
}

func (u *upstream) dial(ctx context.Context, network, address string) (net.Conn, error) {
	u.mu.Lock()
	u.dialed = append(u.dialed, address)
	refuse, hang, ended := u.refuse[address], u.hang[address], u.hangEnded
	u.mu.Unlock()
	switch {
	case refuse:
		return nil, errors.New("connection refused")
	case hang:
		<-ctx.Done()
		if ended != nil {
			close(ended)
		}
		return nil, ctx.Err()
	}
	c, err := (&net.Dialer{}).DialContext(ctx, network, u.srv.Listener.Addr().String())
	if err != nil {
		return nil, err
	}
	return counted{Conn: c, n: &u.read}, nil
}

type counted struct {
	net.Conn
	n *atomic.Int64
}

func (c counted) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n.Add(int64(n))
	return n, err
}

func (u *upstream) answer(addrs ...[]netip.Addr) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.answers = addrs
}

func (u *upstream) now() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.clock
}

func (u *upstream) advance(d time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clock = u.clock.Add(d)
}

// seen is what the upstream saw: lookups, dials, requests to path.
func (u *upstream) seen(path string) (lookups int, dialed []string, hits int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lookups, slices.Clone(u.dialed), u.hits[path]
}

func (u *upstream) artwork(roots *x509.CertPool, timeout time.Duration) *media.Artwork {
	if roots == nil {
		roots = u.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	}
	return media.NewTestArtwork(media.ArtworkRig{Roots: roots, Resolve: u.resolve, Dial: u.dial, Timeout: timeout, Now: u.now})
}

// artEnv is the media rig with h serving the art; whatever a test does, the
// log never carries the url, its host, path or the address it pinned (D11).
func artEnv(t *testing.T, h http.HandlerFunc) (*env, *upstream) {
	t.Helper()
	e := newEnv(t)
	u := newUpstream(t, h)
	e.art = u.artwork(nil, 2*time.Second)
	t.Cleanup(func() { private(t, e.logs.String()) })
	return e, u
}

func private(t *testing.T, logs string) {
	t.Helper()
	for _, s := range []string{"https:", "http:", "example.com", "covers", "c.jpg", "user", public4.String(), "2606:4700"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log carries %q:\n%s", s, logs)
		}
	}
}

func image(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", typ)
		_, _ = w.Write(cover)
	}
}

// play makes the device's session show url as its art.
func (e *env) play(t *testing.T, dev, url string) {
	t.Helper()
	m := playing()
	m.Metadata.ArtworkUrl = url
	handle(t, e.store, dev, m)
}

// unavailable asks for the art and wants 502 artwork_unavailable, logged with
// one of reasons and nothing else.
func (e *env) unavailable(t *testing.T, reasons ...string) {
	t.Helper()
	code, _, body := e.do(t, "x", artPath)
	if code != 502 || string(body) != `{"error":"artwork_unavailable"}` {
		t.Fatalf("%d %s, want 502 artwork_unavailable (%v)", code, body, reasons)
	}
	lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	last := lines[len(lines)-1]
	if !slices.ContainsFunc(reasons, func(r string) bool {
		return strings.HasSuffix(last, "msg=artwork_unavailable device_id=dev-1 reason="+r)
	}) {
		t.Fatalf("log %q, want reason %v", last, reasons)
	}
}

// distinct is an art url of its own for the n-th ask, so no failure an earlier
// ask left behind answers it.
func distinct(n int) string { return "https://" + artHost + "/covers/" + strconv.Itoa(n) + ".jpg" }

// the art comes through the server with its type, kept out of client caches;
// a second ask, from any device whose session names the same url, is served
// from memory
func TestArtworkServedAndCached(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	e.play(t, devID, artURL)
	for _, path := range []string{artPath, artPath, "/api/v1/devices/DEV-1/artwork"} {
		code, h, body := e.do(t, "x", path)
		if code != 200 || string(body) != string(cover) {
			t.Fatalf("%s: %d %q", path, code, body)
		}
		if h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("headers %v", h)
		}
	}
	e.play(t, "dev-2", artURL)
	if code, _, body := e.do(t, "x", "/api/v1/devices/dev-2/artwork"); code != 200 || string(body) != string(cover) {
		t.Fatalf("dev-2: %d %q", code, body)
	}
	if lookups, dialed, hits := u.seen("/covers/c.jpg"); lookups != 1 || !slices.Equal(dialed, []string{"93.184.215.14:443"}) || hits != 1 {
		t.Fatalf("lookups %d dialed %v hits %d, want one fetch", lookups, dialed, hits)
	}
}

// the name is resolved once and the checked address is dialed, never the name
// again: an answer that rebinds it inside never reaches a dial, and at a later
// dial (a new connection) it is refused
func TestArtworkPinsTheResolvedAddress(t *testing.T) {
	e, u := artEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
		image("image/png")(w, nil)
	})
	u.answer([]netip.Addr{public6}, []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	e.play(t, devID, artURL)
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("%d", code)
	}
	if lookups, dialed, _ := u.seen(""); lookups != 1 || !slices.Equal(dialed, []string{"[2606:4700:4700::1111]:443"}) {
		t.Fatalf("lookups %d dialed %v", lookups, dialed)
	}
	e.play(t, devID, "https://"+artHost+"/covers/d.jpg")
	e.unavailable(t, "address")
	if lookups, dialed, hits := u.seen("/covers/d.jpg"); lookups != 2 || len(dialed) != 1 || hits != 0 {
		t.Fatalf("lookups %d dialed %v hits %d", lookups, dialed, hits)
	}
}

// literal reads a as a url host.
func literal(a string) string {
	if strings.Contains(a, ":") {
		return "[" + a + "]"
	}
	return a
}

// every internal address class, v4 and v6 (and v4 inside v6), is refused by
// name and as a literal, before anything is dialed (M8-D4)
func TestArtworkRefusesInternalAddresses(t *testing.T) {
	refused := map[string][]string{
		"loopback":    {"127.0.0.1", "127.8.9.10", "::1"},
		"private":     {"10.1.2.3", "172.16.0.1", "172.31.255.254", "192.168.1.1", "fc00::1", "fd12:3456::1"},
		"link-local":  {"169.254.169.254", "169.254.0.1", "fe80::1"},
		"cgnat":       {"100.64.0.1", "100.127.255.254"},
		"multicast":   {"224.0.0.251", "239.255.255.250", "ff02::fb", "ff0e::1"},
		"unspecified": {"0.0.0.0", "::"},
		"v4 in v6":    {"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:100.64.0.1"},
		"special": {
			"0.1.2.3", "255.255.255.255", "240.0.0.1", "192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.1",
			"203.0.113.1", "::7f00:1", "64:ff9b::7f00:1", "64:ff9b:1::a00:1", "100::1", "2001::1", "2001:db8::1",
			"2002:7f00:1::1", "3fff::1", "5f00::1", "fec0::1", "192.88.99.1",
		},
	}
	e, u := artEnv(t, image("image/png"))
	asks := 0
	for class, addrs := range refused {
		for _, a := range addrs {
			u.answer([]netip.Addr{netip.MustParseAddr(a)})
			asks++
			e.play(t, devID, distinct(asks))
			e.unavailable(t, "address")
			e.play(t, devID, "https://"+literal(a)+"/c.jpg")
			e.unavailable(t, "address")
			if lookups, dialed, _ := u.seen(""); lookups != asks || len(dialed) != 0 {
				t.Fatalf("%s %s: %d lookups, dialed %v", class, a, lookups, dialed)
			}
		}
	}
}

// a name whose answer holds an internal address among public ones is refused
// whole: no address of it is dialed
func TestArtworkRefusesANameResolvingInside(t *testing.T) {
	e, u := artEnv(t, image("image/png"))
	for _, answer := range [][]netip.Addr{
		{netip.MustParseAddr("192.168.0.10")},
		{public4, netip.MustParseAddr("10.0.0.5")},
		{public6, netip.MustParseAddr("fd00::5")},
		{},
	} {
		u.answer(answer)
		e.play(t, devID, distinct(len(answer)))
		reason := "address"
		if len(answer) == 0 {
			reason = "dns"
		}
		e.unavailable(t, reason)
	}
	if _, dialed, hits := u.seen("/covers/c.jpg"); len(dialed) != 0 || hits != 0 {
		t.Fatalf("dialed %v hits %d", dialed, hits)
	}
}

func TestArtworkRefusesRedirects(t *testing.T) {
	e, u := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/covers/c.jpg" {
			http.Redirect(w, r, "/elsewhere.jpg", http.StatusFound)
			return
		}
		image("image/jpeg")(w, r)
	})
	e.play(t, devID, artURL)
	e.unavailable(t, "redirect")
	if _, _, hits := u.seen("/elsewhere.jpg"); hits != 0 {
		t.Fatal("the redirect was followed")
	}
}

// only an https url with a host, no userinfo and no port but 443 is fetched
// (ruling M8-R3); nothing is resolved or dialed for any other
func TestArtworkFetchesHTTPSOnly(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	for _, raw := range []string{
		"http://" + artHost + "/covers/c.jpg", "ftp://" + artHost + "/c.jpg", "//" + artHost + "/c.jpg",
		"https://user:pw@" + artHost + "/c.jpg", "https:///c.jpg", "https://:443/c.jpg", "https:" + artHost,
		"art.example.com/c.jpg", "https://%zz/c.jpg", "https://" + artHost + ":8443/c.jpg",
		"https://" + artHost + ":80/c.jpg", "https://" + artHost + ":0443/c.jpg", "https://" + artHost + ":444/c.jpg",
		"https://[2606:4700:4700::1111]:8443/c.jpg",
	} {
		e.play(t, devID, raw)
		e.unavailable(t, "url")
	}
	if lookups, dialed, _ := u.seen(""); lookups != 0 || len(dialed) != 0 {
		t.Fatalf("lookups %d dialed %v", lookups, dialed)
	}
	e.play(t, devID, "https://"+artHost+":443/covers/c.jpg")
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("port 443 named: %d", code)
	}
	if _, dialed, _ := u.seen(""); !slices.Equal(dialed, []string{"93.184.215.14:443"}) {
		t.Fatalf("dialed %v", dialed)
	}
}

// a name nobody answers, and addresses none of which connects
func TestArtworkUnreachable(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	e.play(t, devID, "https://other.example.com/c.jpg")
	e.unavailable(t, "dns")
	e.art = media.NewTestArtwork(media.ArtworkRig{
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{public4, public6}, nil },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
		Timeout: 2 * time.Second,
	})
	e.play(t, devID, artURL)
	e.unavailable(t, "connect")
	if _, _, hits := u.seen("/covers/c.jpg"); hits != 0 {
		t.Fatalf("%d hits", hits)
	}
}

// the checked addresses are tried in order, at most four of them, and one that
// hangs leaves the next its share of the deadline
func TestArtworkTriesTheNextAddress(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	u.answer([]netip.Addr{public4, public6})
	u.refuse["93.184.215.14:443"] = true
	e.play(t, devID, distinct(1))
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("first refused: %d", code)
	}
	if _, dialed, _ := u.seen(""); !slices.Equal(dialed, []string{"93.184.215.14:443", "[2606:4700:4700::1111]:443"}) {
		t.Fatalf("dialed %v", dialed)
	}

	e, u = artEnv(t, image("image/jpeg"))
	e.art = u.artwork(nil, 3*time.Second)
	u.answer([]netip.Addr{public6, public4})
	u.hang["[2606:4700:4700::1111]:443"] = true
	e.play(t, devID, distinct(2))
	start := time.Now()
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("first hangs: %d after %v", code, time.Since(start))
	}

	e, u = artEnv(t, image("image/jpeg"))
	var answer []netip.Addr
	for i := range 6 {
		a := netip.AddrFrom4([4]byte{93, 184, 215, byte(20 + i)})
		answer = append(answer, a)
		u.refuse[a.String()+":443"] = true
	}
	u.answer(answer)
	e.play(t, devID, distinct(3))
	e.unavailable(t, "connect")
	if _, dialed, _ := u.seen(""); len(dialed) != 4 || dialed[0] != "93.184.215.20:443" || dialed[3] != "93.184.215.23:443" {
		t.Fatalf("dialed %v", dialed)
	}
}

// the transport dials apart from the request (a dial may outlive it), so a
// connect that hangs and a TLS handshake that stalls end on the fetch's own
// deadline, not whenever the far end gives up
func TestArtworkDialEndsWithTheFetch(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	e.art = u.artwork(nil, 300*time.Millisecond)
	ended := make(chan struct{})
	u.mu.Lock()
	u.hang["93.184.215.14:443"] = true
	u.hangEnded = ended
	u.mu.Unlock()
	e.play(t, devID, distinct(1))
	e.unavailable(t, "timeout")
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the hanging dial outlived the fetch")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	closed := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(io.Discard, c)
		close(closed)
	}()
	e.art = media.NewTestArtwork(media.ArtworkRig{
		Resolve: u.resolve,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
		},
		Timeout: 300 * time.Millisecond,
	})
	e.play(t, devID, distinct(2))
	e.unavailable(t, "timeout")
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the stalled handshake outlived the fetch")
	}
}

// one deadline, the fetch's own, holds the connect and the TLS handshake
// together: a slow connect leaves the handshake only what is left of it, so
// the transport's orphaned dial ends with the fetch, not a handshake timeout
// after it
func TestArtworkConnectAndHandshakeShareTheFetchDeadline(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	closed := make(chan time.Time, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(io.Discard, c)
		closed <- time.Now()
	}()
	e.art = media.NewTestArtwork(media.ArtworkRig{
		Resolve: u.resolve,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			select {
			case <-time.After(700 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
		},
		Timeout: time.Second,
	})
	e.play(t, devID, distinct(1))
	start := time.Now()
	e.unavailable(t, "timeout")
	select {
	case at := <-closed:
		// a handshake timed on its own would end 1.7 s in
		if d := at.Sub(start); d > 1350*time.Millisecond {
			t.Fatalf("the handshake ended %v into a fetch of 1 s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stalled handshake never ended")
	}
}

// a v6 literal is fetched on the default port and on :443 alike, its address
// dialed as written and verified against the certificate
func TestArtworkFetchesAV6Literal(t *testing.T) {
	e := newEnv(t)
	cert, roots := v6Cert(t)
	u := newUpstreamTLS(t, func(w http.ResponseWriter, _ *http.Request) {
		// each fetch dials anew
		w.Header().Set("Connection", "close")
		image("image/png")(w, nil)
	}, &tls.Config{Certificates: []tls.Certificate{cert}})
	e.art = u.artwork(roots, 2*time.Second)
	t.Cleanup(func() { private(t, e.logs.String()) })
	for i, raw := range []string{"https://[2606:4700:4700::1111]/covers/v6.png", "https://[2606:4700:4700::1111]:443/covers/v6.png"} {
		e.play(t, devID, raw)
		if code, _, body := e.do(t, "x", artPath); code != 200 || string(body) != string(cover) {
			t.Fatalf("%s: %d %q", raw, code, body)
		}
		if lookups, dialed, hits := u.seen("/covers/v6.png"); lookups != 0 || len(dialed) != i+1 || dialed[i] != "[2606:4700:4700::1111]:443" || hits != i+1 {
			t.Fatalf("%s: lookups %d dialed %v hits %d", raw, lookups, dialed, hits)
		}
	}
}

// v6Cert is a certificate naming public6 alone, and the roots that trust it
func v6Cert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "v6 art"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{public6.AsSlice()}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

// a failed fetch is remembered for 30 s by its url: asks within it get the same
// answer with nothing fetched, a later one fetches again
func TestArtworkRemembersAFailure(t *testing.T) {
	var up atomic.Bool
	e, u := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		image("image/png")(w, r)
	})
	e.play(t, devID, artURL)
	e.unavailable(t, "status")
	up.Store(true)
	u.advance(29 * time.Second)
	e.unavailable(t, "status")
	if lookups, _, hits := u.seen("/covers/c.jpg"); lookups != 1 || hits != 1 {
		t.Fatalf("remembered: %d lookups %d hits", lookups, hits)
	}
	u.advance(2 * time.Second)
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("after 31 s: %d", code)
	}
	if _, _, hits := u.seen("/covers/c.jpg"); hits != 2 {
		t.Fatalf("%d hits", hits)
	}
}

func TestArtworkVerifiesTLS(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	e.art = u.artwork(x509.NewCertPool(), 2*time.Second)
	e.play(t, devID, artURL)
	e.unavailable(t, "tls")
	if _, _, hits := u.seen("/covers/c.jpg"); hits != 0 {
		t.Fatal("a request went over an unverified connection")
	}
}

// a body is cut at 5 MiB: one that says it is longer is refused unread, one
// that streams on is cut where the cap is passed
func TestArtworkSizeCap(t *testing.T) {
	cut := make(chan struct{})
	e, u := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/exact.png":
			_, _ = w.Write(make([]byte, artCap))
		case "/declared.png":
			w.Header().Set("Content-Length", "5242881")
			_, _ = w.Write(make([]byte, artCap+1))
		case "/stream.png":
			defer close(cut)
			chunk := make([]byte, 64<<10)
			for r.Context().Err() == nil {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	})
	e.play(t, devID, "https://"+artHost+"/exact.png")
	if code, _, body := e.do(t, "x", artPath); code != 200 || len(body) != artCap {
		t.Fatalf("%d, %d bytes", code, len(body))
	}
	e.play(t, devID, "https://"+artHost+"/declared.png")
	u.read.Store(0)
	e.unavailable(t, "too_large")
	if n := u.read.Load(); n > 1<<20 {
		t.Fatalf("a body declared too long was read: %d bytes", n)
	}
	e.play(t, devID, "https://"+artHost+"/stream.png")
	e.unavailable(t, "too_large")
	select {
	case <-cut:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was not cut")
	}
}

// only jpeg, png, webp and gif are served, under their own type, and only as
// sent: an encoded body would be served undecoded
func TestArtworkImagesOnly(t *testing.T) {
	e, _ := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if typ := r.URL.Query().Get("type"); typ == "none" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", typ)
		}
		if enc := r.URL.Query().Get("enc"); enc != "" {
			w.Header().Set("Content-Encoding", enc)
		}
		_, _ = w.Write(cover)
	})
	at := func(typ string) string { return "https://" + artHost + "/t?type=" + url.QueryEscape(typ) }
	for _, typ := range []string{
		"text/html", "image/svg+xml", "application/octet-stream", "image/jpegx", "image/", "image/png, text/html", "none",
	} {
		e.play(t, devID, at(typ))
		e.unavailable(t, "type")
	}
	for _, enc := range []string{"gzip", "br", "identity, gzip"} {
		e.play(t, devID, at("image/jpeg")+"&enc="+url.QueryEscape(enc))
		e.unavailable(t, "type")
	}
	e.play(t, devID, at("image/jpeg")+"&enc=identity")
	if code, _, _ := e.do(t, "x", artPath); code != 200 {
		t.Fatalf("identity: %d", code)
	}
	for typ, want := range map[string]string{
		"image/jpeg": "image/jpeg", "image/png": "image/png", "image/webp": "image/webp", "image/gif": "image/gif",
		"IMAGE/PNG; charset=binary": "image/png",
	} {
		e.play(t, devID, at(typ))
		if code, h, body := e.do(t, "x", artPath); code != 200 || h.Get("Content-Type") != want || string(body) != string(cover) {
			t.Fatalf("%s: %d %s %q", typ, code, h.Get("Content-Type"), body)
		}
	}
}

func TestArtworkRefusesOtherStatuses(t *testing.T) {
	e, _ := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/gone.png":
			w.WriteHeader(http.StatusNotFound)
		case "/empty.png":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write(cover)
	})
	for _, p := range []string{"/gone.png", "/empty.png", "/failed.png"} {
		e.play(t, devID, "https://"+artHost+p)
		e.unavailable(t, "status")
	}
}

// the deadline covers the whole fetch: an answer that never starts, and a body
// that stalls
func TestArtworkTimeout(t *testing.T) {
	e, u := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stall.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(cover)
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
			// aborted, never ended: the client's close at its deadline sends close_notify before it shuts the socket,
			// and a response ended in between could still reach it whole
			panic(http.ErrAbortHandler)
		case <-time.After(5 * time.Second):
		}
	})
	e.art = u.artwork(nil, 300*time.Millisecond)
	start := time.Now()
	for _, p := range []string{"/silent.jpg", "/stall.jpg"} {
		e.play(t, devID, "https://"+artHost+p)
		e.unavailable(t, "timeout")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}

// concurrent asks for one url fetch it once
func TestArtworkFetchedOnceForConcurrentAsks(t *testing.T) {
	release, first := make(chan struct{}), make(chan struct{}, 1)
	e, u := artEnv(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case first <- struct{}{}:
		default:
		}
		<-release
		image("image/gif")(w, r)
	})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	e.play(t, devID, artURL)
	app := e.app()
	codes := make([]int, 4)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, artPath, nil)
			req.Header.Set("Authorization", "Bearer x")
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			codes[i] = resp.StatusCode
		})
	}
	<-first
	time.Sleep(200 * time.Millisecond)
	free()
	wg.Wait()
	if !slices.Equal(codes, []int{200, 200, 200, 200}) {
		t.Fatalf("codes %v", codes)
	}
	if _, _, hits := u.seen("/covers/c.jpg"); hits != 1 {
		t.Fatalf("%d fetches", hits)
	}
}

// no session, a session without art, or art past the url bound: 404
// no_artwork, with nothing fetched
func TestArtworkNeedsSessionArt(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	for _, step := range []func(){
		func() {},
		func() { e.play(t, devID, "") },
		func() { e.play(t, devID, "https://"+artHost+"/"+strings.Repeat("x", 2048)) },
		func() { e.store.Closed(devID) },
	} {
		step()
		if code, _, body := e.do(t, "x", artPath); code != 404 || string(body) != `{"error":"no_artwork"}` {
			t.Fatalf("%d %s", code, body)
		}
	}
	if code, _, body := e.do(t, "x", "/api/v1/devices/nope/artwork"); code != 404 || string(body) != `{"error":"not_found"}` {
		t.Fatalf("%d %s", code, body)
	}
	if lookups, _, _ := u.seen(""); lookups != 0 {
		t.Fatalf("%d lookups", lookups)
	}
}

// an ha token reads the art (M8-D4), a metrics one is refused
func TestArtworkScopes(t *testing.T) {
	e, _ := artEnv(t, image("image/webp"))
	e.verify = func(ctx context.Context, bearer string) (auth.Grants, error) {
		if bearer == "metrics-token" {
			return auth.Grants{Scopes: auth.Scopes{auth.ScopeMetrics}}, nil
		}
		return apitest.Verify(ctx, bearer)
	}
	e.play(t, devID, artURL)
	if code, _, body := e.do(t, apitest.HABearer, artPath); code != 200 || string(body) != string(cover) {
		t.Fatalf("ha: %d %q", code, body)
	}
	if code, _, body := e.do(t, "metrics-token", artPath); code != 403 || string(body) != `{"error":"forbidden"}` {
		t.Fatalf("metrics: %d %s", code, body)
	}
}

func hashOf(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}

// the caller may name the art it expects (ruling M8-R3): its hash fetches it,
// another one answers 409 artwork_changed with nothing fetched, a malformed one
// is refused, and without a session there is no art whatever the hash
func TestArtworkHashMustMatch(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	e.play(t, devID, artURL)
	if code, _, body := e.do(t, "x", artPath+"?h="+hashOf(distinct(1))); code != 409 || string(body) != `{"error":"artwork_changed"}` {
		t.Fatalf("another hash: %d %s", code, body)
	}
	if lookups, dialed, _ := u.seen(""); lookups != 0 || len(dialed) != 0 {
		t.Fatalf("fetched for another hash: %d lookups, dialed %v", lookups, dialed)
	}
	if code, _, body := e.do(t, "x", artPath+"?h="+hashOf(artURL)); code != 200 || string(body) != string(cover) {
		t.Fatalf("its hash: %d %q", code, body)
	}
	for _, bad := range []string{"abc", strings.ToUpper(hashOf(artURL)), hashOf(artURL) + "0", strings.Repeat("g", 64)} {
		if code, _, body := e.do(t, "x", artPath+"?h="+bad); code != 422 || !strings.Contains(string(body), `"error":"invalid"`) {
			t.Fatalf("h=%s: %d %s", bad, code, body)
		}
	}
	e.store.Closed(devID)
	if code, _, body := e.do(t, "x", artPath+"?h="+hashOf(artURL)); code != 404 || string(body) != `{"error":"no_artwork"}` {
		t.Fatalf("no session: %d %s", code, body)
	}
	if _, _, hits := u.seen("/covers/c.jpg"); hits != 1 {
		t.Fatalf("%d hits", hits)
	}
}

// the server's own artwork client resolves with the system and dials through
// a dialer that refuses an internal address at the socket too; the odd spellings
// of loopback are refused or not resolved, depending on the resolver, never
// dialed
func TestArtworkServerClientRefusesLoopback(t *testing.T) {
	e, _ := artEnv(t, image("image/jpeg"))
	e.art = media.NewArtwork()
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]", "[::ffff:127.0.0.1]"} {
		e.play(t, devID, "https://"+host+"/covers/c.jpg")
		e.unavailable(t, "address")
	}
	for _, host := range []string{"localhost.", "2130706433", "0x7f.1", "127.1", "0177.0.0.1"} {
		e.play(t, devID, "https://"+host+"/covers/c.jpg")
		e.unavailable(t, "address", "dns")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if c, err := media.GuardedDial(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, media.ErrAddressRefused) {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("guarded dial: %v", err)
	}
}
