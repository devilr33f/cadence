package deepgram_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/deepgram"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func next(t *testing.T, ch <-chan providers.STTEvent) providers.STTEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("events closed")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return providers.STTEvent{}
}

func TestFluxRequest(t *testing.T) {
	got := make(chan *http.Request, 1)
	up := newUpstream(t, func(r *http.Request, c *websocket.Conn) {
		got <- r
		_, _, _ = c.Read(context.Background())
	})
	st, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: up.base}, "key-123", up.client).
		Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := <-got
	q, _ := url.ParseQuery(r.URL.RawQuery)
	if r.URL.Path != "/v2/listen" || q.Get("model") != "flux-general-en" || q.Get("encoding") != "opus" ||
		q.Get("sample_rate") != "16000" || q.Get("eot_threshold") != "0.8" {
		t.Fatalf("request %s", r.URL)
	}
	if r.Header.Get("Authorization") != "Token key-123" {
		t.Fatal("authorization header")
	}
}

func TestFluxEvents(t *testing.T) {
	audio := make(chan []byte, 1)
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		bg := context.Background()
		typ, b, err := c.Read(bg)
		if err == nil && typ == websocket.MessageBinary {
			audio <- b
		}
		for _, m := range []string{
			`{"type":"TurnInfo","event":"StartOfTurn","turn_index":0,"transcript":""}`,
			`{"type":"TurnInfo","event":"Update","turn_index":0,"transcript":"what"}`,
			`{"type":"Connected"}`,
			`{"type":"TurnInfo","event":"Update","turn_index":0,"transcript":"  "}`,
			`{"type":"TurnInfo","event":"EagerEndOfTurn","turn_index":0,"transcript":"what time"}`,
			`{"type":"TurnInfo","event":"TurnResumed","turn_index":0,"transcript":"what time is"}`,
			`{"type":"TurnInfo","event":"EndOfTurn","turn_index":0,"transcript":" what time is it "}`,
			`{"type":"TurnInfo","event":"EndOfTurn","turn_index":1,"transcript":"  "}`,
		} {
			_ = c.Write(bg, websocket.MessageText, []byte(m))
		}
		_, _, _ = c.Read(bg)
	})
	st, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: up.base}, "k", up.client).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if e := next(t, st.Events()); e.Kind != providers.STTSpeechStarted {
		t.Fatalf("first %+v", e)
	}
	// each interim transcript with text is a partial, in order; an empty one is none
	for _, want := range []string{"what", "what time", "what time is"} {
		if e := next(t, st.Events()); e.Kind != providers.STTPartial || e.Text != want {
			t.Fatalf("partial %+v, want %q", e, want)
		}
	}
	if e := next(t, st.Events()); e.Kind != providers.STTFinal || e.Text != "what time is it" {
		t.Fatalf("final %+v", e)
	}
	if b := <-audio; !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("audio %v", b)
	}
}

func TestFluxUpstreamErrorIsFixedCode(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"Error","description":"`+upstreamLeak+`"}`))
		_, _, _ = c.Read(context.Background())
	})
	st, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: up.base}, "k", up.client).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := next(t, st.Events())
	if e.Kind != providers.STTFailed || e.Code != providers.CodeSTTFailed || e.Text != "" {
		t.Fatalf("event %+v", e)
	}
	if bytes.Contains(logs.Bytes(), []byte(upstreamLeak)) {
		t.Fatal("log carries upstream text")
	}
}

func TestFluxRejectsUntrustedTLS(t *testing.T) {
	up := newUpstream(t, func(*http.Request, *websocket.Conn) {})
	_, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: up.base}, "k", httpx.Client(nil)).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestFluxRefusesRedirect(t *testing.T) {
	base, client, hits := redirector(t)
	_, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: base}, "k", client).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err == nil {
		t.Fatal("redirect followed")
	}
	if *hits != 0 {
		t.Fatal("redirect target was hit")
	}
}

func TestFluxReadLoopPanicEndsStream(t *testing.T) {
	var logs bytes.Buffer
	deepgram.SetDecodeHook(t, func([]byte) { panic("secret-xyz") })
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"TurnInfo"}`))
		_, _, _ = c.Read(context.Background())
	})
	cfg := deepgram.FluxConfig{BaseURL: up.base, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	st, err := deepgram.NewFlux(cfg, "k", up.client).Start(ctx(t), providers.STTOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if e := next(t, st.Events()); e.Kind != providers.STTFailed || e.Code != providers.CodeSTTFailed {
		t.Fatalf("event %+v", e)
	}
	if _, ok := <-st.Events(); ok {
		t.Fatal("events not closed")
	}
	if out := logs.String(); strings.Contains(out, "secret-xyz") || !strings.Contains(out, "provider_panic") {
		t.Fatalf("log %q", out)
	}
}
