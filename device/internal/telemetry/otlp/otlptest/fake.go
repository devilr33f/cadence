// Package otlptest is a fake OTLP/HTTP server over TLS for tests: it records
// every request, its body gunzipped, and answers as the test scripts it.
package otlptest

import (
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
)

// a gzip bomb in a test fails the test instead of the machine
const maxBody = 64 << 20

// Request is one request the fake received.
type Request struct {
	Method, Path string
	Header       http.Header
	// Compressed is the size on the wire, Body the payload gunzipped
	Compressed int
	Body       []byte
	// BadGzip is set when the body did not gunzip
	BadGzip bool
	// Answer is how the fake answered it
	Answer Response
}

// Response is how the fake answers one request; Drop closes the connection
// without an answer.
type Response struct {
	Status int
	Header http.Header
	Drop   bool
}

type Server struct {
	*httptest.Server
	got chan Request
	// what a "no request" failure reports: the connections and what the server logged
	opened, closed, handshakeErrors, otherErrors atomic.Int64

	mu      sync.Mutex
	respond func(Request) Response
}

// New starts a fake answering 200 until Respond says otherwise.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{got: make(chan Request, 4096), respond: func(Request) Response { return Response{Status: http.StatusOK} }}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	// refused handshakes are what some tests are after: counted, never printed
	s.Config.ErrorLog = log.New(errorCounter{s}, "", 0)
	s.Config.ConnState = func(_ net.Conn, cs http.ConnState) {
		switch cs {
		case http.StateNew:
			s.opened.Add(1)
		case http.StateClosed, http.StateHijacked:
			s.closed.Add(1)
		}
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// Pool trusts the fake's certificate alone.
func (s *Server) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(s.Certificate())
	return p
}

// OtherCA trusts a fresh CA of its own: every httptest server shares one
// certificate, so another fake's pool would trust this one too.
func OtherCA(t testing.TB) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "otlptest other ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AddCert(cert)
	return p
}

func (s *Server) Respond(f func(Request) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond = f
}

// Script answers the next requests with rs in turn, and 200 after them.
func (s *Server) Script(rs ...Response) {
	var mu sync.Mutex
	s.Respond(func(Request) Response {
		mu.Lock()
		defer mu.Unlock()
		if len(rs) == 0 {
			return Response{Status: http.StatusOK}
		}
		r := rs[0]
		rs = rs[1:]
		return r
	})
}

// Next waits for the next request.
func (s *Server) Next(t testing.TB) Request {
	t.Helper()
	select {
	case r := <-s.got:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("no request (%s)", s.Connections())
		return Request{}
	}
}

// TryNext returns the next request that arrives within d.
func (s *Server) TryNext(d time.Duration) (Request, bool) {
	select {
	case r := <-s.got:
		return r, true
	case <-time.After(d):
		return Request{}, false
	}
}

// None fails when a request arrives within d.
func (s *Server) None(t testing.TB, d time.Duration) {
	t.Helper()
	select {
	case r := <-s.got:
		t.Fatalf("unexpected request to %s", r.Path)
	case <-time.After(d):
	}
}

// Logs decodes r as an OTLP logs export request.
func Logs(t testing.TB, r Request) *collogsv1.ExportLogsServiceRequest {
	t.Helper()
	var m collogsv1.ExportLogsServiceRequest
	if err := proto.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("decode logs request: %v", err)
	}
	return &m
}

func (s *Server) serve(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))
	r := Request{Method: req.Method, Path: req.URL.Path, Header: req.Header.Clone(), Compressed: len(raw)}
	if zr, err := gzip.NewReader(bytes.NewReader(raw)); err != nil {
		r.BadGzip = true
	} else if r.Body, err = io.ReadAll(io.LimitReader(zr, maxBody)); err != nil {
		r.BadGzip = true
	}
	s.mu.Lock()
	respond := s.respond
	s.mu.Unlock()
	resp := respond(r)
	r.Answer = resp
	s.got <- r
	if resp.Drop {
		// the server closes the connection unanswered, and logs nothing
		panic(http.ErrAbortHandler)
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.Status)
}

// Connections says what became of the fake's connections, for a failure message.
func (s *Server) Connections() string {
	return fmt.Sprintf("connections opened %d, closed %d; tls handshake errors %d, other server errors %d",
		s.opened.Load(), s.closed.Load(), s.handshakeErrors.Load(), s.otherErrors.Load())
}

// errorCounter counts the server's error log lines instead of printing them.
type errorCounter struct{ s *Server }

func (e errorCounter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("TLS handshake error")) {
		e.s.handshakeErrors.Add(1)
	} else {
		e.s.otherErrors.Add(1)
	}
	return len(p), nil
}

func (s *Server) HandshakeErrors() int64 { return s.handshakeErrors.Load() }
