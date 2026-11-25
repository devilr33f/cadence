package media_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// connectProxy is an http proxy that tunnels every CONNECT to the art host, whatever it names, recording the targets
type connectProxy struct {
	ln      net.Listener
	mu      sync.Mutex
	targets []string
}

func newConnectProxy(t *testing.T, upstream string) *connectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				p.mu.Lock()
				p.targets = append(p.targets, req.Host)
				p.mu.Unlock()
				up, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return p
}

func (p *connectProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// with a proxy the cover still comes from the checked address: the proxy is asked to CONNECT to it, never to the
// name, the server dials nothing itself, and an address the guard refuses never reaches the proxy
func TestArtworkThroughAProxyByAddress(t *testing.T) {
	e, u := artEnv(t, image("image/jpeg"))
	p := newConnectProxy(t, u.srv.Listener.Addr().String())
	proxyURL := &url.URL{Scheme: "http", Host: p.ln.Addr().String()}
	fresh := func() *media.Artwork {
		return media.NewTestArtwork(media.ArtworkRig{
			Roots: u.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, Resolve: u.resolve,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				t.Error("dialed directly with a proxy set")
				return nil, net.ErrClosed
			},
			Timeout: 2 * time.Second, Now: u.now,
			Proxy:     func(string) *url.URL { return proxyURL },
			ProxyDial: (&net.Dialer{}).DialContext,
		})
	}
	e.art = fresh()
	e.play(t, devID, artURL)
	if code, _, body := e.do(t, "x", artPath); code != 200 || string(body) != string(cover) {
		t.Fatalf("%d %q", code, body)
	}
	if got := p.seen(); len(got) != 1 || got[0] != "93.184.215.14:443" {
		t.Fatalf("proxy asked for %v", got)
	}

	// a new fetcher: the first one keeps its open tunnel, whatever the name answers now
	e.art = fresh()
	u.answer([]netip.Addr{netip.MustParseAddr("10.0.0.5")})
	e.play(t, devID, "https://"+artHost+"/covers/other.jpg")
	e.unavailable(t, "address")
	if got := p.seen(); len(got) != 1 {
		t.Fatalf("a refused address reached the proxy: %v", got)
	}
}
