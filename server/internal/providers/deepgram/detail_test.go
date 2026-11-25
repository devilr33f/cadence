package deepgram_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/deepgram"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// a failure keeps its code and says its cause as a token: a close status, an error frame, a dial's class
func TestFailureDetails(t *testing.T) {
	up := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Close(websocket.StatusInternalError, upstreamLeak)
	})
	st, err := deepgram.NewFlux(deepgram.FluxConfig{BaseURL: up.base}, "k", up.client).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if e := next(t, st.Events()); e.Kind != providers.STTFailed || e.Code != providers.CodeSTTFailed || e.Detail != "closed_1011" {
		t.Fatalf("closed: %+v", e)
	}

	errFrame := newUpstream(t, func(_ *http.Request, c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"Error","description":"`+upstreamLeak+`"}`))
		_, _, _ = c.Read(context.Background())
	})
	s, err := deepgram.NewAura(deepgram.AuraConfig{BaseURL: errFrame.base}, "k", errFrame.client).Open(ctx(t), providers.TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	drain(t, s)
	if s.Err() == nil || s.Err().Error() != providers.CodeTTSFailed || providers.Detail(s.Err()) != "error_message" {
		t.Fatalf("error frame: %#v", s.Err())
	}

	untrusted := newUpstream(t, func(*http.Request, *websocket.Conn) {})
	_, err = deepgram.NewFlux(deepgram.FluxConfig{BaseURL: untrusted.base}, "k", httpx.Client(nil)).Start(ctx(t), providers.STTOptions{SampleRate: 16000})
	if err == nil || err.Error() != providers.CodeSTTUnavailable || providers.Detail(err) != "dial_tls" {
		t.Fatalf("untrusted: %v %q", err, providers.Detail(err))
	}
}
