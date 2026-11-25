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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

const (
	videoBody = "VIDEO-0123456789-VIDEO-0123456789"
	audioBody = "audio-abcdefghij"
	master    = "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"https://manifest.googlevideo.com/a.m3u8?sig=AUD-SECRET\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2600000,RESOLUTION=1280x720\nv/720.m3u8?sig=VAR-SECRET\n"
	variant = "#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:5.0,\n" +
		"https://rr2---sn-y.googlevideo.com/seg/1.ts?sig=SEG-SECRET\r\n#EXTINF:5.0,\n/seg/2.ts\n"
	evilVariant = "#EXTM3U\n#EXTINF:5.0,\nhttps://evil.example/seg.ts\n"
)

// hosts is a fake set of media hosts on one server: each path serves its own file, ranges included
func hosts(t *testing.T, block chan struct{}, hit chan struct{}) (*http.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Host+r.URL.Path+" range="+r.Header.Get("Range"))
		mu.Unlock()
		switch r.URL.Path {
		case "/video":
			w.Header().Set("Content-Type", "video/webm")
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(videoBody))
		case "/audio":
			w.Header().Set("Content-Type", "audio/mp4")
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(audioBody))
		case "/master.m3u8":
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = io.WriteString(w, master)
		case "/v/720.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, variant)
		case "/evil.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, evilVariant)
		case "/seg/1.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader("SEGMENT-ONE"))
		case "/big":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(strings.Repeat("b", 96<<10)))
		case "/slow":
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			hit <- struct{}{}
			<-block
		default:
			http.NotFound(w, r)
		}
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

func TestRelayServesASetOfTwoUpstreamsWithRanges(t *testing.T) {
	client, seen := hosts(t, nil, nil)
	var log bytes.Buffer
	r := streams.New(streams.Config{Client: client, Log: slog.New(slog.NewTextHandler(&log, nil))})
	token, err := r.AddSet("tv", []streams.Upstream{
		{URL: "https://rr1---sn-x.googlevideo.com/audio?sig=AUDIO-SECRET"},
		{URL: "https://rr1---sn-x.googlevideo.com/video?sig=VIDEO-SECRET"},
	}, streams.Options{TTL: 12 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	a := app(r)
	if resp, body := get(t, a, "/v2/stream/"+token+"/1", "bytes=6-15"); resp.StatusCode != http.StatusPartialContent ||
		body != videoBody[6:16] || resp.Header.Get("Content-Type") != "video/webm" {
		t.Fatalf("video: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(t, a, "/v2/stream/"+token+"/0", "bytes=0-4"); resp.StatusCode != http.StatusPartialContent || body != "audio" {
		t.Fatalf("audio: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(t, a, "/v2/stream/"+token, ""); resp.StatusCode != http.StatusOK || body != audioBody {
		t.Fatalf("first: %d %q", resp.StatusCode, body)
	}
	for _, bad := range []string{"/2", "/-1", "/01", "/x"} {
		if resp, _ := get(t, a, "/v2/stream/"+token+bad, ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", bad, resp.StatusCode)
		}
	}
	if len(*seen) != 3 || (*seen)[0] != "rr1---sn-x.googlevideo.com/video range=bytes=6-15" {
		t.Fatalf("upstream saw %v", *seen)
	}
	if strings.Contains(log.String(), "SECRET") || strings.Contains(log.String(), "googlevideo") {
		t.Fatalf("log: %s", log.String())
	}
	exp, ok := r.Expires(token)
	if !ok || time.Until(exp) < 11*time.Hour {
		t.Fatalf("a film's ttl: %v", exp)
	}
}

func TestRelayRefusesASetWithOneHostOffTheList(t *testing.T) {
	r := streams.New(streams.Config{})
	_, err := r.AddSet("tv", []streams.Upstream{{URL: "https://rr1.googlevideo.com/a"}, {URL: "https://evil.example/b"}}, streams.Options{})
	if !errors.Is(err, streams.ErrUpstream) {
		t.Fatal(err)
	}
	if _, err := r.AddSet("tv", nil, streams.Options{}); !errors.Is(err, streams.ErrUpstream) {
		t.Fatal(err)
	}
	extra := streams.New(streams.Config{Hosts: []string{"cdn.example"}})
	if _, err := extra.Add("tv", "https://cdn.example/x.m3u8"); err != nil {
		t.Fatalf("an allowed whole host: %v", err)
	}
	if _, err := extra.Add("tv", "https://sub.cdn.example/x.m3u8"); !errors.Is(err, streams.ErrUpstream) {
		t.Fatalf("a whole host's subdomain: %v", err)
	}
}

func TestRelayRewritesHLSPlaylistsThroughItself(t *testing.T) {
	client, seen := hosts(t, nil, nil)
	r := streams.New(streams.Config{Client: client})
	token, err := r.AddSet("tv", []streams.Upstream{{URL: "https://manifest.googlevideo.com/master.m3u8?sig=M", HLS: true}}, streams.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := app(r)
	resp, body := get(t, a, "/v2/stream/"+token, "bytes=0-3")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "#EXTM3U\n") || resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("master: %d %q", resp.StatusCode, body)
	}
	if strings.Contains(body, "googlevideo") || strings.Contains(body, "SECRET") {
		t.Fatalf("master leaks an upstream: %s", body)
	}
	children := relayURIs(t, body, token)
	if len(children) != 2 {
		t.Fatalf("children %v in %s", children, body)
	}
	// the variant, a relative uri, resolves against the master's url
	resp, body = get(t, a, children[1], "")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "googlevideo") || strings.Contains(body, "\r\n") == false {
		t.Fatalf("variant: %d %q", resp.StatusCode, body)
	}
	segs := relayURIs(t, body, token)
	if len(segs) != 3 {
		t.Fatalf("segments %v", segs)
	}
	resp, body = get(t, a, segs[1], "bytes=0-6")
	if resp.StatusCode != http.StatusPartialContent || body != "SEGMENT" {
		t.Fatalf("segment: %d %q", resp.StatusCode, body)
	}
	want := []string{
		"manifest.googlevideo.com/master.m3u8 range=", "manifest.googlevideo.com/v/720.m3u8 range=",
		"rr2---sn-y.googlevideo.com/seg/1.ts range=bytes=0-6",
	}
	if strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Fatalf("upstream saw %v", *seen)
	}
	// a sealed child is bound to its token
	other, _ := r.AddSet("tv", []streams.Upstream{{URL: "https://manifest.googlevideo.com/x", HLS: true}}, streams.Options{})
	if resp, _ := get(t, a, strings.Replace(segs[1], token, other, 1), ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("child under another token: %d", resp.StatusCode)
	}
	if resp, _ := get(t, a, "/v2/stream/"+token+"/h/AAAA", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("forged child: %d", resp.StatusCode)
	}
}

func TestRelayRefusesAPlaylistNamingAnotherHost(t *testing.T) {
	client, _ := hosts(t, nil, nil)
	r := streams.New(streams.Config{Client: client})
	token, _ := r.AddSet("tv", []streams.Upstream{{URL: "https://manifest.googlevideo.com/evil.m3u8", HLS: true}}, streams.Options{})
	resp, body := get(t, app(r), "/v2/stream/"+token, "")
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "evil") {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

func TestRelayCapsADevicesFetchesAtOnce(t *testing.T) {
	block, hit := make(chan struct{}), make(chan struct{}, 1)
	client, _ := hosts(t, block, hit)
	r := streams.New(streams.Config{Client: client, PerDevice: 1})
	slow, _ := r.Add("tv", "https://rr1.googlevideo.com/slow")
	fast, _ := r.Add("tv", "https://rr1.googlevideo.com/video")
	other, _ := r.Add("speaker", "https://rr1.googlevideo.com/video")
	a := app(r)
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/v2/stream/"+slow, nil)
		if resp, err := a.Test(req, fiberTimeout(5*time.Second)); err == nil {
			_, _ = io.ReadAll(resp.Body)
		}
	}()
	<-hit
	if resp, _ := get(t, a, "/v2/stream/"+fast, ""); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second fetch: %d", resp.StatusCode)
	}
	if resp, _ := get(t, a, "/v2/stream/"+other, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("another device: %d", resp.StatusCode)
	}
	close(block)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, _ := get(t, a, "/v2/stream/"+fast, "")
		if resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never freed: %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRelayThrottlesADevicesBytes(t *testing.T) {
	client, _ := hosts(t, nil, nil)
	// 32 KiB a second, a burst of one read: the 33-byte video is under it, so this checks the reader's accounting
	r := streams.New(streams.Config{Client: client, BytesPerSecond: 32 << 10})
	token, _ := r.Add("tv", "https://rr1.googlevideo.com/video")
	start := time.Now()
	if resp, body := get(t, app(r), "/v2/stream/"+token, ""); resp.StatusCode != http.StatusOK || body != videoBody {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a body under the burst waited")
	}
	// 96 KiB at 128 KiB a second with a 32 KiB burst takes about half a second
	r = streams.New(streams.Config{Client: client, BytesPerSecond: 128 << 10})
	token, _ = r.Add("tv", "https://rr1.googlevideo.com/big")
	start = time.Now()
	if resp, body := get(t, app(r), "/v2/stream/"+token, ""); resp.StatusCode != http.StatusOK || len(body) != 96<<10 {
		t.Fatalf("%d %d", resp.StatusCode, len(body))
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("not throttled: %v", d)
	}
}

// relayURIs are the relay paths a rewritten playlist names, in order
func relayURIs(t *testing.T, playlist, token string) []string {
	t.Helper()
	var out []string
	for _, f := range strings.FieldsFunc(playlist, func(r rune) bool { return r == '\n' || r == '\r' || r == '"' }) {
		if strings.HasPrefix(f, "/v2/stream/"+token+"/h/") {
			out = append(out, f)
		}
	}
	return out
}

func fiberTimeout(d time.Duration) fiber.TestConfig { return fiber.TestConfig{Timeout: d} }
