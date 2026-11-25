package streams_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

const track = "0123456789abcdefghijklmnopqrstuvwxyz"

// upstream is a fake media host: every name dials it (TLS unchecked: the fake's
// certificate names no media host), and it serves track with ranges
func upstream(t *testing.T, status int) (*http.Client, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Host+" "+r.URL.RawQuery+" range="+r.Header.Get("Range"))
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "LEAK")
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(track))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the fake's own certificate
	}}
	return client, &seen
}

func app(r *streams.Relay) *fiber.App {
	a := fiber.New()
	r.Mount(a.Group("/v2"))
	return a
}

func get(t *testing.T, a *fiber.App, path, rng string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := a.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestRelayServesTheStreamWithRanges(t *testing.T) {
	client, seen := upstream(t, 0)
	var log bytes.Buffer
	r := streams.New(streams.Config{Client: client, Log: slog.New(slog.NewTextHandler(&log, nil))})
	first := 0
	token, err := r.AddSet("d1", []streams.Upstream{{URL: "https://rr1---sn-x.googlevideo.com/videoplayback?sig=SIGNED"}},
		streams.Options{OnFirstByte: func() { first++ }})
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("token %q", token)
	}
	a := app(r)
	resp, body := get(t, a, "/v2/stream/"+token, "")
	if resp.StatusCode != http.StatusOK || body != track || resp.Header.Get("Content-Type") != "audio/mp4" ||
		resp.Header.Get("Content-Length") != strconv.Itoa(len(track)) {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = get(t, a, "/v2/stream/"+token, "bytes=10-19")
	if resp.StatusCode != http.StatusPartialContent || body != track[10:20] || resp.Header.Get("Content-Range") != "bytes 10-19/36" {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	if len(*seen) != 2 || (*seen)[0] != "rr1---sn-x.googlevideo.com sig=SIGNED range=" || !strings.HasSuffix((*seen)[1], "range=bytes=10-19") {
		t.Fatalf("upstream saw %v", *seen)
	}
	if first != 1 {
		t.Fatalf("first-byte callback ran %d times", first)
	}
	if strings.Contains(log.String(), "SIGNED") || strings.Contains(log.String(), "googlevideo") || strings.Contains(log.String(), token) {
		t.Fatalf("log: %s", log.String())
	}
}

func TestRelayRefusesOtherHostsAndUnknownTokens(t *testing.T) {
	client, seen := upstream(t, 0)
	r := streams.New(streams.Config{Client: client})
	for _, u := range []string{
		"http://rr1.googlevideo.com/x", "https://evil.example/x", "https://googlevideo.com.evil.example/x",
		"https://169.254.169.254/latest", "not a url",
	} {
		if _, err := r.Add("d1", u); !errors.Is(err, streams.ErrUpstream) {
			t.Fatalf("%q: %v", u, err)
		}
	}
	if resp, _ := get(t, app(r), "/v2/stream/nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token: %d", resp.StatusCode)
	}
	if len(*seen) != 0 {
		t.Fatalf("upstream fetched: %v", *seen)
	}
}

func TestRelayForgetsAtTheUpstreamsExpiry(t *testing.T) {
	client, _ := upstream(t, 0)
	now := time.Unix(1_800_000_000, 0)
	r := streams.New(streams.Config{Client: client, Now: func() time.Time { return now }})
	token, err := r.Add("d1", "https://rr1.googlevideo.com/videoplayback?expire=1800000060")
	if err != nil {
		t.Fatal(err)
	}
	a := app(r)
	if resp, _ := get(t, a, "/v2/stream/"+token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
	now = now.Add(61 * time.Second)
	if resp, _ := get(t, a, "/v2/stream/"+token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired stream: %d", resp.StatusCode)
	}
}

func TestRelayAnswersAnUpstreamRefusalAsBadGateway(t *testing.T) {
	client, _ := upstream(t, http.StatusForbidden)
	r := streams.New(streams.Config{Client: client})
	token, _ := r.Add("d1", "https://rr1.googlevideo.com/videoplayback")
	resp, body := get(t, app(r), "/v2/stream/"+token, "")
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "LEAK") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

func TestRelayKeepsTheNewestStreams(t *testing.T) {
	client, _ := upstream(t, 0)
	r := streams.New(streams.Config{Client: client})
	first, _ := r.Add("d1", "https://rr1.googlevideo.com/a")
	var last string
	for range 64 {
		last, _ = r.Add("d1", "https://rr1.googlevideo.com/b")
	}
	a := app(r)
	if resp, _ := get(t, a, "/v2/stream/"+first, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oldest kept: %d", resp.StatusCode)
	}
	if resp, _ := get(t, a, "/v2/stream/"+last, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("newest: %d", resp.StatusCode)
	}
}
