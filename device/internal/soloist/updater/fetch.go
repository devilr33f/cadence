package updater

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// SpotifyARM64 is where Spotify serves the arm64 build.
const SpotifyARM64 = "https://soloist-builds.spotifycdn.com/soloist_release_arm64.tar.gz"

const (
	defaultTimeout = 30 * time.Second
	// the whole download, from the first lookup to the last byte of the body
	defaultTotal = 600 * time.Second
	maxTarball   = 64 << 20
	headLimit    = 16 << 10
)

// Fetcher gets the tarball: its body ends at the head's Content-Length, and a
// read fails with the network.
type Fetcher interface {
	Get(ctx context.Context) (io.ReadCloser, error)
}

// CDN fetches over HTTP/1.1 and TLS, verified against CAFile's certificates
// alone. It follows no redirect: only a 200 with exactly one numeric
// Content-Length of at most 64 MiB and no Transfer-Encoding is a body. Each
// connect, read and write gets Timeout, and the whole download Total.
type CDN struct {
	URL    string
	CAFile string
	// zero is 30 s and 600 s
	Timeout time.Duration
	Total   time.Duration

	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (c *CDN) Get(ctx context.Context) (io.ReadCloser, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, FailHTTP
	}
	roots, err := c.roots()
	if err != nil {
		return nil, FailTLS
	}
	timeout, total := c.Timeout, c.Total
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if total <= 0 {
		total = defaultTotal
	}
	deadline := time.Now().Add(total)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	conn, err := c.dial(ctx, u, timeout, deadline)
	if err != nil {
		cancel()
		return nil, err
	}
	// a read blocked when the deadline passes or the caller gives up ends at once
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	release := func() {
		stop()
		cancel()
		_ = conn.Close()
	}
	// under the tls, so the handshake counts too
	tc := tls.Client(&deadlineConn{Conn: conn, timeout: timeout, at: deadline},
		&tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		release()
		return nil, FailTLS
	}
	body, err := respond(tc, u)
	if err != nil {
		release()
		return nil, err
	}
	return &closer{Reader: body, close: release}, nil
}

func (c *CDN) roots() (*x509.CertPool, error) {
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("no certificates")
	}
	return pool, nil
}

func (c *CDN) dial(ctx context.Context, u *url.URL, timeout time.Duration, deadline time.Time) (net.Conn, error) {
	port := uint64(443)
	if p := u.Port(); p != "" {
		var err error
		if port, err = strconv.ParseUint(p, 10, 16); err != nil || port == 0 {
			return nil, FailHTTP
		}
	}
	addrs, err := c.addrs(ctx, u.Hostname())
	if err != nil || len(addrs) == 0 {
		return nil, FailDNS
	}
	for _, a := range addrs {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		d := net.Dialer{Timeout: min(timeout, left)}
		conn, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		if err == nil {
			return conn, nil
		}
	}
	return nil, FailConnect
}

func (c *CDN) addrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if c.lookup != nil {
		return c.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// respond sends the request and reads the head; the body that follows ends at
// its Content-Length
func respond(conn io.ReadWriter, u *url.URL) (io.Reader, error) {
	target := u.EscapedPath()
	if target == "" {
		target = "/"
	}
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	req := "GET " + target + " HTTP/1.1\r\nHost: " + u.Host +
		"\r\nUser-Agent: cadenced\r\nAccept: */*\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return nil, FailHTTP
	}
	length, rest, err := readHead(conn)
	if err != nil {
		return nil, err
	}
	return &exact{r: io.MultiReader(bytes.NewReader(rest), conn), left: length}, nil
}

// readHead reads up to the blank line, at most 16 KiB, and returns what the
// last read took of the body beyond it
func readHead(r io.Reader) (int64, []byte, error) {
	buf := make([]byte, 0, 4096)
	piece := make([]byte, 4096)
	for {
		if end := bytes.Index(buf, []byte("\r\n\r\n")); end >= 0 {
			if end+4 > headLimit {
				return 0, nil, FailHTTP
			}
			length, err := parseHead(buf[:end])
			return length, buf[end+4:], err
		}
		if len(buf) >= headLimit {
			return 0, nil, FailHTTP
		}
		n, err := r.Read(piece)
		buf = append(buf, piece[:n]...)
		if err != nil && !bytes.Contains(buf, []byte("\r\n\r\n")) {
			return 0, nil, FailHTTP
		}
	}
}

// parseHead is the Content-Length of a 200 over HTTP/1.x: a body of unknown
// length (chunked, or none) cannot be bounded, so it is refused
func parseHead(head []byte) (int64, error) {
	lines := strings.Split(string(head), "\n")
	status := strings.SplitN(strings.TrimSuffix(lines[0], "\r"), " ", 3)
	if len(status) < 2 || !strings.HasPrefix(status[0], "HTTP/1.") || status[1] != "200" {
		return 0, FailHTTP
	}
	length := int64(-1)
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return 0, FailHTTP
		}
		value = strings.Trim(value, " \t")
		switch {
		case strings.EqualFold(name, "transfer-encoding"):
			return 0, FailHTTP
		case strings.EqualFold(name, "content-length"):
			if length >= 0 || value == "" || strings.Trim(value, "0123456789") != "" {
				return 0, FailHTTP
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return 0, FailHTTP
			}
			length = n
		}
	}
	switch {
	case length < 0:
		return 0, FailHTTP
	case length > maxTarball:
		return 0, FailSize
	}
	return length, nil
}

// exact ends at the head's length: a stream that stops short of it is an
// error, never a short file
type exact struct {
	r    io.Reader
	left int64
}

func (e *exact) Read(p []byte) (int, error) {
	if e.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.left {
		p = p[:e.left]
	}
	n, err := e.r.Read(p)
	e.left -= int64(n)
	if errors.Is(err, io.EOF) && e.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

type closer struct {
	io.Reader
	close func()
}

func (c *closer) Close() error {
	c.close()
	return nil
}

// deadlineConn fails a read or write once at has passed, whatever the peer
// does, and gives each one timeout: a server that drips a byte at a time
// cannot hold the update forever
type deadlineConn struct {
	net.Conn
	timeout time.Duration
	at      time.Time
}

func (c *deadlineConn) Read(p []byte) (int, error) {
	if err := c.arm(c.SetReadDeadline); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *deadlineConn) Write(p []byte) (int, error) {
	if err := c.arm(c.SetWriteDeadline); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *deadlineConn) arm(set func(time.Time) error) error {
	now := time.Now()
	if !now.Before(c.at) {
		return os.ErrDeadlineExceeded
	}
	next := now.Add(c.timeout)
	if next.After(c.at) {
		next = c.at
	}
	return set(next)
}
