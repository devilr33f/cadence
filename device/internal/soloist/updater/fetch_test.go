package updater_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

const path = "/soloist_release_arm64.tar.gz"

type cdn struct {
	srv      *httptest.Server
	requests atomic.Int32
	caFile   string
}

// newCDN serves handler over TLS with a certificate of its own, which caFile
// alone trusts
func newCDN(t *testing.T, handler http.HandlerFunc) *cdn {
	t.Helper()
	c := &cdn{}
	c.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		handler(w, r)
	}))
	c.srv.StartTLS()
	t.Cleanup(c.srv.Close)
	c.caFile = caFile(t, c.srv)
	return c
}

func caFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (c *cdn) fetcher() *updater.CDN {
	return &updater.CDN{URL: c.srv.URL + path, CAFile: c.caFile, Timeout: 2 * time.Second, Total: 10 * time.Second}
}

// raw answers with exactly what write puts on the connection
func raw(write func(w *bufio.Writer)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		write(buf.Writer)
		_ = buf.Flush()
	}
}

func head(lines ...string) func(w *bufio.Writer) {
	return func(w *bufio.Writer) {
		for _, l := range lines {
			_, _ = w.WriteString(l + "\r\n")
		}
		_, _ = w.WriteString("\r\n")
	}
}

func get(t *testing.T, f updater.Fetcher) ([]byte, error) {
	t.Helper()
	body, err := f.Get(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// one plain GET, verified against the bundle, and the body ends at its length
func TestFetchGetsTheTarball(t *testing.T) {
	body := bytes.Repeat([]byte("tarball"), 1000)
	c := newCDN(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path || r.Header.Get("Accept-Encoding") != "identity" || r.Host == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	got, err := get(t, c.fetcher())
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
}

// review focus 4: no redirect is followed and only a 200 is a body
func TestFetchRefusesAnythingButA200(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusMovedPermanently, http.StatusNotFound, http.StatusPartialContent, http.StatusNoContent} {
		c := newCDN(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://"+r.Host+"/elsewhere")
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("0123456789"))
		})
		if _, err := get(t, c.fetcher()); !errors.Is(err, updater.FailHTTP) {
			t.Fatalf("%d: %v", status, err)
		}
		if n := c.requests.Load(); n != 1 {
			t.Fatalf("%d: %d requests", status, n)
		}
	}
}

// a body whose end the head does not fix is refused: chunked, any transfer
// encoding, no length, two lengths, a length that is no number
func TestFetchNeedsOneContentLength(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(w *bufio.Writer)
	}{
		{"chunked", head("HTTP/1.1 200 OK", "Transfer-Encoding: chunked")},
		{"chunked with a length", head("HTTP/1.1 200 OK", "Content-Length: 10", "Transfer-Encoding: chunked")},
		{"identity encoding", head("HTTP/1.1 200 OK", "Transfer-Encoding: identity", "Content-Length: 10")},
		{"no length", head("HTTP/1.1 200 OK", "Content-Type: application/gzip")},
		{"two equal lengths", head("HTTP/1.1 200 OK", "Content-Length: 10", "Content-Length: 10")},
		{"a list", head("HTTP/1.1 200 OK", "Content-Length: 10, 10")},
		{"empty", head("HTTP/1.1 200 OK", "Content-Length:")},
		{"negative", head("HTTP/1.1 200 OK", "Content-Length: -1")},
		{"signed", head("HTTP/1.1 200 OK", "Content-Length: +10")},
		{"exponent", head("HTTP/1.1 200 OK", "Content-Length: 1e3")},
		{"huge", head("HTTP/1.1 200 OK", "Content-Length: 99999999999999999999999")},
		{"no colon", head("HTTP/1.1 200 OK", "Content-Length 10")},
		{"http/2", head("HTTP/2 200", "Content-Length: 10")},
		{"status 2000", head("HTTP/1.1 2000 OK", "Content-Length: 10")},
		{"no head", func(w *bufio.Writer) { _, _ = w.WriteString("SSH-2.0-OpenSSH\r\n") }},
		{"head over 16 KiB", func(w *bufio.Writer) {
			head("HTTP/1.1 200 OK", "Content-Length: 10", "X-Pad: "+string(bytes.Repeat([]byte("p"), 16*1024)))(w)
		}},
	} {
		cdn := newCDN(t, raw(c.write))
		if _, err := get(t, cdn.fetcher()); !errors.Is(err, updater.FailHTTP) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// any case and spacing of the name and the value
	cdn := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.0 200", "content-LENGTH:   5  ")(w)
		_, _ = w.WriteString("hello")
	}))
	if got, err := get(t, cdn.fetcher()); err != nil || string(got) != "hello" {
		t.Fatalf("lenient head: %q, %v", got, err)
	}
}

func TestFetchSizeCap(t *testing.T) {
	over := newCDN(t, raw(head("HTTP/1.1 200 OK", fmt.Sprintf("Content-Length: %d", 64*mib+1))))
	if _, err := over.fetcher().Get(context.Background()); !errors.Is(err, updater.FailSize) {
		t.Fatalf("over 64 MiB: %v", err)
	}
	at := newCDN(t, raw(head("HTTP/1.1 200 OK", fmt.Sprintf("Content-Length: %d", 64*mib))))
	body, err := at.fetcher().Get(context.Background())
	if err != nil {
		t.Fatalf("64 MiB: %v", err)
	}
	_ = body.Close()
	// the status is judged first
	missing := newCDN(t, raw(head("HTTP/1.1 404 Not Found", fmt.Sprintf("Content-Length: %d", 65*mib))))
	if _, err := get(t, missing.fetcher()); !errors.Is(err, updater.FailHTTP) {
		t.Fatalf("404 over 64 MiB: %v", err)
	}
}

// a body cut short of its length is an error, never a short file
func TestFetchShortBodyFails(t *testing.T) {
	c := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.1 200 OK", "Content-Length: 100")(w)
		_, _ = w.WriteString("only ten..")
	}))
	if got, err := get(t, c.fetcher()); err == nil {
		t.Fatalf("short body read whole: %d bytes", len(got))
	}
}

// a server that stops sending fails the read after the read timeout, and one
// that drips fails at the total deadline
func TestFetchTimeouts(t *testing.T) {
	stall := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.1 200 OK", "Content-Length: 100")(w)
		_, _ = w.WriteString("ten bytes.")
		_ = w.Flush()
		time.Sleep(3 * time.Second)
	}))
	f := stall.fetcher()
	f.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := get(t, f); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("stalled body: %v after %v", err, time.Since(start))
	}
	silent := newCDN(t, raw(func(*bufio.Writer) { time.Sleep(3 * time.Second) }))
	f = silent.fetcher()
	f.Timeout = 200 * time.Millisecond
	start = time.Now()
	if _, err := get(t, f); !errors.Is(err, updater.FailHTTP) || time.Since(start) > 2*time.Second {
		t.Fatalf("silent server: %v after %v", err, time.Since(start))
	}
	drip := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.1 200 OK", "Content-Length: 1000")(w)
		for range 1000 {
			if _, err := w.WriteString("x"); err != nil || w.Flush() != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	f = drip.fetcher()
	f.Timeout, f.Total = 300*time.Millisecond, 600*time.Millisecond
	start = time.Now()
	if got, err := get(t, f); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("dripping body: %d bytes, %v after %v", len(got), err, time.Since(start))
	}
}

func TestFetchFailureReasons(t *testing.T) {
	good := newCDN(t, raw(head("HTTP/1.1 200 OK", "Content-Length: 0")))

	// a certificate the bundle does not vouch for, and a bundle with none
	f := good.fetcher()
	f.CAFile = foreignCA(t)
	if _, err := get(t, f); !errors.Is(err, updater.FailTLS) {
		t.Fatalf("foreign certificate: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("no certificates"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ca := range []string{empty, filepath.Join(t.TempDir(), "missing.pem")} {
		f.CAFile = ca
		if _, err := get(t, f); !errors.Is(err, updater.FailTLS) {
			t.Fatalf("bundle %s: %v", filepath.Base(ca), err)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	f = &updater.CDN{URL: "https://" + closed + path, CAFile: good.caFile, Timeout: 2 * time.Second}
	if _, err := get(t, f); !errors.Is(err, updater.FailConnect) {
		t.Fatalf("closed port: %v", err)
	}

	f = &updater.CDN{URL: "https://soloist-builds.invalid" + path, CAFile: good.caFile}
	updater.SetLookup(f, func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") })
	if _, err := get(t, f); !errors.Is(err, updater.FailDNS) {
		t.Fatalf("lookup failed: %v", err)
	}
	updater.SetLookup(f, func(context.Context, string) ([]netip.Addr, error) { return nil, nil })
	if _, err := get(t, f); !errors.Is(err, updater.FailDNS) {
		t.Fatalf("no address: %v", err)
	}
	// the name is looked up, and the certificate checked for it
	port := good.srv.Listener.Addr().(*net.TCPAddr).Port
	f = &updater.CDN{URL: fmt.Sprintf("https://example.com:%d%s", port, path), CAFile: good.caFile, Timeout: 2 * time.Second}
	updater.SetLookup(f, func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "example.com" {
			return nil, errors.New("wrong host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, err := get(t, f); err != nil {
		t.Fatalf("by name: %v", err)
	}
	for _, u := range []string{"http://127.0.0.1" + path, "ftp://x/y", "https://", "https://user:pass@example.com/x", "::"} {
		f = &updater.CDN{URL: u, CAFile: good.caFile}
		if _, err := get(t, f); !errors.Is(err, updater.FailHTTP) {
			t.Fatalf("url %q: %v", u, err)
		}
	}
}

// foreignCA is a bundle with a certificate of another key than httptest's
func foreignCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// the certificate is checked for the url's own name, whatever it resolves to
func TestFetchRefusesACertificateForAnotherName(t *testing.T) {
	c := newCDN(t, raw(head("HTTP/1.1 200 OK", "Content-Length: 0")))
	port := c.srv.Listener.Addr().(*net.TCPAddr).Port
	f := &updater.CDN{URL: fmt.Sprintf("https://other.test:%d%s", port, path), CAFile: c.caFile, Timeout: 2 * time.Second}
	updater.SetLookup(f, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, err := get(t, f); !errors.Is(err, updater.FailTLS) {
		t.Fatalf("another name: %v", err)
	}
	if n := c.requests.Load(); n != 0 {
		t.Fatalf("%d requests reached the server", n)
	}
}
