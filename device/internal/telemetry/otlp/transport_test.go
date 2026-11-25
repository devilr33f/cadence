package otlp_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp/otlptest"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
)

const token = "cdd_device-token-0123456789"

func newClient(t *testing.T, srv *otlptest.Server) *otlp.Client {
	t.Helper()
	c, err := otlp.NewClient(srv.URL, token, srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func timeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPostGzipsWithBearerAndProtobufType(t *testing.T) {
	srv := otlptest.New(t)
	c := newClient(t, srv)
	msg := &commonv1.InstrumentationScope{Name: "cadence.device"}
	status, wait, err := c.Post(timeout(t), "/v2/otlp/v1/logs", msg)
	if err != nil || status != http.StatusOK || wait != 0 {
		t.Fatalf("post = %d, %v, %v", status, wait, err)
	}
	r := srv.Next(t)
	if r.Method != http.MethodPost || r.Path != "/v2/otlp/v1/logs" {
		t.Fatalf("request = %s %s", r.Method, r.Path)
	}
	for k, want := range map[string]string{
		"Content-Type": "application/x-protobuf", "Content-Encoding": "gzip", "Authorization": "Bearer " + token,
	} {
		if got := r.Header.Get(k); got != want {
			t.Fatalf("%s = %q", k, got)
		}
	}
	var got commonv1.InstrumentationScope
	if r.BadGzip {
		t.Fatal("body is not gzip")
	}
	if err := proto.Unmarshal(r.Body, &got); err != nil || got.GetName() != "cadence.device" {
		t.Fatalf("body = %v, %v", &got, err)
	}
}

// the server url may end in a slash, as an operator writes it
func TestPostJoinsBaseWithoutDoubleSlash(t *testing.T) {
	srv := otlptest.New(t)
	c, err := otlp.NewClient(srv.URL+"/", token, srv.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PostEncoded(timeout(t), "/v2/otlp/v1/logs", nil); err != nil {
		t.Fatal(err)
	}
	if r := srv.Next(t); r.Path != "/v2/otlp/v1/logs" {
		t.Fatalf("path = %s", r.Path)
	}
}

func TestNewClientRefusesOtherSchemes(t *testing.T) {
	for _, u := range []string{"", "ftp://h", "wss://h", "h:443"} {
		if _, err := otlp.NewClient(u, token, nil); err == nil {
			t.Fatalf("%q accepted", u)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status int
		header string
		want   time.Duration
	}{
		{"seconds", 429, "7", 7 * time.Second},
		{"seconds on 503", 503, "12", 12 * time.Second},
		{"absent", 429, "", 5 * time.Second},
		{"date", 503, now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{"capped seconds", 429, "3600", 60 * time.Second},
		{"capped beyond range", 429, "99999999999999999999999", 60 * time.Second},
		{"capped date", 429, now.Add(time.Hour).Format(http.TimeFormat), 60 * time.Second},
		{"past date", 429, now.Add(-time.Hour).Format(http.TimeFormat), time.Second},
		{"zero", 429, "0", time.Second},
		{"invalid", 429, "soon", 5 * time.Second},
		{"not a retry status", 500, "7", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := otlptest.New(t)
			srv.Respond(func(otlptest.Request) otlptest.Response {
				h := http.Header{}
				if tc.header != "" {
					h.Set("Retry-After", tc.header)
				}
				return otlptest.Response{Status: tc.status, Header: h}
			})
			c := newClient(t, srv)
			otlp.SetNow(c, func() time.Time { return now })
			status, wait, err := c.PostEncoded(timeout(t), "/x", nil)
			if err != nil || status != tc.status || wait != tc.want {
				t.Fatalf("post = %d, %v, %v; want %d, %v", status, wait, err, tc.status, tc.want)
			}
		})
	}
}

// a redirect could carry the bearer to another host
func TestRedirectRefused(t *testing.T) {
	other := otlptest.New(t)
	srv := otlptest.New(t)
	srv.Respond(func(otlptest.Request) otlptest.Response {
		return otlptest.Response{Status: http.StatusTemporaryRedirect, Header: http.Header{"Location": {other.URL + "/v2/otlp/v1/logs"}}}
	})
	c := newClient(t, srv)
	if _, _, err := c.PostEncoded(timeout(t), "/v2/otlp/v1/logs", []byte("x")); err == nil {
		t.Fatal("redirect followed")
	}
	srv.Next(t)
	other.None(t, 100*time.Millisecond)
}

func TestBadCertificateRefused(t *testing.T) {
	srv := otlptest.New(t)
	c, err := otlp.NewClient(srv.URL, token, otlptest.OtherCA(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PostEncoded(timeout(t), "/v2/otlp/v1/logs", []byte("x")); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	srv.None(t, 100*time.Millisecond)
	// the fake counts the refused handshake, for its "no request" message; it logs it on its own goroutine
	deadline := time.Now().Add(5 * time.Second)
	for srv.HandshakeErrors() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := srv.HandshakeErrors(); n != 1 {
		t.Fatalf("handshake errors = %d", n)
	}
}

// random bytes do not compress: over 1 MiB of them is no request at all
func TestTooLargeNeverSent(t *testing.T) {
	srv := otlptest.New(t)
	c := newClient(t, srv)
	body := make([]byte, otlp.MaxRequestBytes+1)
	_, _ = rand.Read(body)
	if _, _, err := c.PostEncoded(timeout(t), "/x", body); !errors.Is(err, otlp.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	srv.None(t, 100*time.Millisecond)
	// just under the cap once compressed goes out
	if _, _, err := c.PostEncoded(timeout(t), "/x", body[:otlp.MaxRequestBytes-64<<10]); err != nil {
		t.Fatalf("err = %v", err)
	}
	if r := srv.Next(t); r.Compressed > otlp.MaxRequestBytes {
		t.Fatalf("sent %d bytes", r.Compressed)
	}
}

func TestTimeoutIsAnError(t *testing.T) {
	srv := otlptest.New(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv.Respond(func(otlptest.Request) otlptest.Response {
		<-release
		return otlptest.Response{Status: http.StatusOK}
	})
	c := newClient(t, srv)
	otlp.SetTimeout(c, 50*time.Millisecond)
	start := time.Now()
	if _, _, err := c.PostEncoded(timeout(t), "/x", nil); err == nil {
		t.Fatal("no timeout")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// the error may reach a log: it never quotes the url, the token or the cause
func TestErrorsQuoteNothing(t *testing.T) {
	srv := otlptest.New(t)
	c, err := otlp.NewClient(srv.URL, token, otlptest.OtherCA(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.PostEncoded(timeout(t), "/v2/otlp/v1/logs", nil)
	if err == nil {
		t.Fatal("no error")
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	if msg := err.Error(); strings.Contains(msg, host) || strings.Contains(msg, token) || strings.Contains(msg, "x509") {
		t.Fatalf("error quotes too much: %s", msg)
	}
}

// GetWith sends its header's fields beside the bearer, which a field of that name never replaces
func TestGetWithSendsHeader(t *testing.T) {
	srv := otlptest.New(t)
	c := newClient(t, srv)
	body, err := c.GetWith(timeout(t), "/v2/session-artwork", http.Header{"X-Cadence-Item": {"abc"}, "Authorization": {"Bearer other"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	r := srv.Next(t)
	if r.Method != http.MethodGet || r.Path != "/v2/session-artwork" || r.Header.Get("X-Cadence-Item") != "abc" ||
		r.Header.Get("Authorization") != "Bearer "+token {
		t.Fatalf("request %s %s %v", r.Method, r.Path, r.Header)
	}
}
