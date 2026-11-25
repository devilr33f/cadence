package media

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// the proxy the environment sets for an https fetch of host (HTTPS_PROXY, NO_PROXY), nil for none
func envProxy(host string) *url.URL {
	u, err := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "https", Host: host}})
	if err != nil {
		return nil
	}
	return u
}

// connect reaches the checked address, through the proxy the environment names for host when there is one: the
// proxy is asked to CONNECT to that address, never to the name, so it resolves nothing and reaches only what passed
// forbidden. a proxy other than http:// is refused
func (a *Artwork) connect(ctx context.Context, network, host, address string) (net.Conn, error) {
	proxy := a.proxy(host)
	if proxy == nil {
		return a.dial(ctx, network, address)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || forbidden(ap.Addr()) {
		return nil, errArtAddress
	}
	if proxy.Scheme != "http" {
		return nil, errArtConnect
	}
	proxyAddr := proxy.Host
	if proxy.Port() == "" {
		proxyAddr = net.JoinHostPort(proxy.Hostname(), "80")
	}
	conn, err := a.proxyDial(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, errArtConnect
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	req := "CONNECT " + address + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)) + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
		return nil, errArtConnect
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, errArtConnect
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errArtConnect
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn reads first what the proxy's answer left in its reader
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
