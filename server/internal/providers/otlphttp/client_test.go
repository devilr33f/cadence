package otlphttp_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
)

const authValue = "Bearer collector-token-0123456789"

type seen struct {
	path, auth, contentType, encoding string
	body                              []byte
}

type collector struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []seen
	answer func(w http.ResponseWriter)
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{answer: func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := seen{path: r.URL.Path, auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"),
			encoding: r.Header.Get("Content-Encoding")}
		if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			s.body, _ = io.ReadAll(zr)
		}
		c.mu.Lock()
		c.got = append(c.got, s)
		answer := c.answer
		c.mu.Unlock()
		answer(w)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) setAnswer(fn func(w http.ResponseWriter)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answer = fn
}

func (c *collector) client(t *testing.T, url, auth string) *otlphttp.Client {
	t.Helper()
	cl, err := otlphttp.NewClient(otlphttp.ClientConfig{
		URL: url, Authorization: auth, HTTP: httpx.Client(c.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func (c *collector) requests() []seen {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]seen(nil), c.got...)
}

func TestPostsEachSignalToItsPathGzipped(t *testing.T) {
	c := newCollector(t)
	cl := c.client(t, c.srv.URL+"/otlp/", authValue)
	if err := cl.Post(context.Background(), "logs", []byte("logs-request")); err != nil {
		t.Fatal(err)
	}
	if err := cl.Post(context.Background(), "metrics", []byte("metrics-request")); err != nil {
		t.Fatal(err)
	}
	if err := cl.Post(context.Background(), "traces", []byte("traces-request")); err != nil {
		t.Fatal(err)
	}
	got := c.requests()
	if len(got) != 3 {
		t.Fatalf("requests %d", len(got))
	}
	for i, want := range []seen{{path: "/otlp/v1/logs", body: []byte("logs-request")}, {path: "/otlp/v1/metrics", body: []byte("metrics-request")},
		{path: "/otlp/v1/traces", body: []byte("traces-request")}} {
		g := got[i]
		if g.path != want.path || string(g.body) != string(want.body) || g.auth != authValue ||
			g.contentType != "application/x-protobuf" || g.encoding != "gzip" {
			t.Fatalf("request %d: %+v", i, g)
		}
	}
	if err := cl.Post(context.Background(), "profiles", nil); err == nil {
		t.Fatal("an unknown signal posted")
	}
	// no secret: no header
	if err := c.client(t, c.srv.URL, "").Post(context.Background(), "logs", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if g := c.requests()[3]; g.auth != "" || g.path != "/v1/logs" {
		t.Fatalf("without a secret: %+v", g)
	}
}

func TestVerifiesTLS(t *testing.T) {
	c := newCollector(t)
	cl, err := otlphttp.NewClient(otlphttp.ClientConfig{URL: c.srv.URL, Authorization: authValue, HTTP: httpx.Client(nil)})
	if err != nil {
		t.Fatal(err)
	}
	err = cl.Post(context.Background(), "logs", []byte("x"))
	var e *otlphttp.Error
	if !errors.As(err, &e) || e.Permanent() || e.Code() != "tls" || len(c.requests()) != 0 {
		t.Fatalf("untrusted certificate: %v", err)
	}
}

// the header goes to the configured endpoint only: a redirect is refused
// before its target is asked anything
func TestRefusesRedirects(t *testing.T) {
	other := newCollector(t)
	c := newCollector(t)
	c.setAnswer(func(w http.ResponseWriter) {
		w.Header().Set("Location", other.srv.URL+"/v1/logs")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	err := c.client(t, c.srv.URL, authValue).Post(context.Background(), "logs", []byte("x"))
	var e *otlphttp.Error
	if !errors.As(err, &e) || e.Permanent() || e.Code() != "redirect" {
		t.Fatalf("redirect: %v", err)
	}
	if len(other.requests()) != 0 {
		t.Fatal("the redirect target was asked")
	}
}

func TestFailures(t *testing.T) {
	c := newCollector(t)
	cl := c.client(t, c.srv.URL, authValue)
	for _, tc := range []struct {
		status     int
		retryAfter string
		permanent  bool
		wait       time.Duration
	}{
		{http.StatusBadRequest, "", true, 0},
		{http.StatusRequestEntityTooLarge, "", true, 0},
		{http.StatusUnauthorized, "", true, 0},
		{http.StatusTooManyRequests, "7", false, 7 * time.Second},
		{http.StatusTooManyRequests, "3600", false, time.Minute},
		{http.StatusServiceUnavailable, "", false, 0},
		{http.StatusServiceUnavailable, "2", false, 2 * time.Second},
		{http.StatusBadGateway, "2", false, 0},
		{http.StatusRequestTimeout, "", false, 0},
		{http.StatusInternalServerError, "", false, 0},
	} {
		c.setAnswer(func(w http.ResponseWriter) {
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("upstream says password=hunter2"))
		})
		err := cl.Post(context.Background(), "logs", []byte("x"))
		var e *otlphttp.Error
		if !errors.As(err, &e) || e.Permanent() != tc.permanent || e.Wait() != tc.wait || e.Status() != tc.status || e.Code() != "status" {
			t.Fatalf("%d %q: %v", tc.status, tc.retryAfter, err)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("error carries the body: %v", err)
		}
	}
	// 2xx of any kind is accepted, its body dropped
	c.setAnswer(func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) })
	if err := cl.Post(context.Background(), "logs", []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestTimeoutBoundsARequest(t *testing.T) {
	c := newCollector(t)
	release := make(chan struct{})
	c.setAnswer(func(http.ResponseWriter) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	})
	t.Cleanup(func() { close(release) })
	began := time.Now()
	err := c.client(t, c.srv.URL, "").Post(context.Background(), "logs", []byte("x"))
	var e *otlphttp.Error
	if !errors.As(err, &e) || e.Permanent() || e.Code() != "timeout" || time.Since(began) > 3*time.Second {
		t.Fatalf("after %v: %v", time.Since(began), err)
	}
	// the caller's own cancellation comes back as its error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.client(t, c.srv.URL, "").Post(ctx, "logs", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
