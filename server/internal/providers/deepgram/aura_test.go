package deepgram_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/deepgram"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

func TestAuraSpeakFlush(t *testing.T) {
	reqs := make(chan *http.Request, 1)
	texts := make(chan []string, 1)
	up := newUpstream(t, func(r *http.Request, c *websocket.Conn) {
		bg := context.Background()
		reqs <- r
		var seen []string
		for range 3 {
			_, b, err := c.Read(bg)
			if err != nil {
				return
			}
			seen = append(seen, string(b))
		}
		texts <- seen
		_ = c.Write(bg, websocket.MessageBinary, []byte{1, 0, 2, 0})
		_ = c.Write(bg, websocket.MessageBinary, []byte{0xff, 0xff})
		_ = c.Write(bg, websocket.MessageText, []byte(`{"type":"Flushed","sequence_id":0}`))
		_, _, _ = c.Read(bg)
	})
	s, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up.base}, "key-9", up.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, txt := range []string{"Hello.", "World."} {
		if err := s.Speak(txt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var got []int16
	timeout := time.After(3 * time.Second)
	for open := true; open; {
		select {
		case p, ok := <-s.Audio():
			got, open = append(got, p...), ok
		case <-timeout:
			t.Fatal("audio never closed")
		}
	}
	if !slices.Equal(got, []int16{1, 2, -1}) {
		t.Fatalf("samples %v", got)
	}
	want := []string{`{"type":"Speak","text":"Hello."}`, `{"type":"Speak","text":"World."}`, `{"type":"Flush"}`}
	if seen := <-texts; !slices.Equal(seen, want) {
		t.Fatalf("frames %v", seen)
	}
	r := <-reqs
	q, _ := url.ParseQuery(r.URL.RawQuery)
	if r.URL.Path != "/v1/speak" || q.Get("model") != "aura-2-thalia-en" || q.Get("encoding") != "linear16" ||
		q.Get("sample_rate") != "24000" || r.Header.Get("Authorization") != "Token key-9" {
		t.Fatalf("request %s", r.URL)
	}
}

func TestAuraRejectsUntrustedTLS(t *testing.T) {
	up := newUpstream(t, func(*http.Request, *websocket.Conn) {})
	_, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up.base}, "k", httpx.Client(nil)).Open(ctx(t), providers.TTSOptions{})
	if err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestAuraRefusesRedirect(t *testing.T) {
	base, client, hits := redirector(t)
	_, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: base}, "k", client).Open(ctx(t), providers.TTSOptions{})
	if err == nil {
		t.Fatal("redirect followed")
	}
	if *hits != 0 {
		t.Fatal("redirect target was hit")
	}
}

func drain(t *testing.T, s providers.TTSStream) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-s.Audio():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("audio never closed")
		}
	}
}

func TestAuraErrorFrameSetsErr(t *testing.T) {
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"Error","description":"`+upstreamLeak+`"}`))
		_, _, _ = c.Read(context.Background())
	})
	s, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up.base}, "k", up.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	drain(t, s)
	if s.Err() == nil || s.Err().Error() != providers.CodeTTSFailed {
		t.Fatalf("err %v", s.Err())
	}
}

func TestAuraDroppedSocketSetsErr(t *testing.T) {
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) { _ = c.CloseNow() })
	s, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up.base}, "k", up.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	drain(t, s)
	if s.Err() == nil {
		t.Fatal("dropped socket reported clean")
	}
}

func TestAuraCleanFlushAndCloseHaveNilErr(t *testing.T) {
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		bg := context.Background()
		_, _, _ = c.Read(bg)
		_ = c.Write(bg, websocket.MessageText, []byte(`{"type":"Flushed","sequence_id":0}`))
		_, _, _ = c.Read(bg)
	})
	s, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up.base}, "k", up.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	if s.Err() != nil {
		t.Fatalf("err %v", s.Err())
	}

	up2 := newUpstream(t, func(_ *http.Request, c *websocket.Conn) { _, _, _ = c.Read(context.Background()) })
	s2, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: up2.base}, "k", up2.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	drain(t, s2)
	if s2.Err() != nil {
		t.Fatalf("err after close %v", s2.Err())
	}
}

func TestAuraReadLoopPanicEndsStream(t *testing.T) {
	var logs bytes.Buffer
	deepgram.SetDecodeHook(t, func([]byte) { panic("secret-xyz") })
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageBinary, []byte{1, 0})
		_, _, _ = c.Read(context.Background())
	})
	cfg := deepgram.AuraConfig{BaseURL: up.base, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	s, err := deepgram.NewAura(cfg, "k", up.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	select {
	case _, ok := <-s.Audio():
		if ok {
			t.Fatal("audio before close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("audio never closed")
	}
	if err := s.Err(); err == nil || err.Error() != providers.CodeTTSFailed {
		t.Fatalf("err %v", err)
	}
	if out := logs.String(); strings.Contains(out, "secret-xyz") || !strings.Contains(out, "provider_panic") {
		t.Fatalf("log %q", out)
	}
}
