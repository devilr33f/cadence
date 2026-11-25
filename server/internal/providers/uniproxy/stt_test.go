package uniproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func nextEvent(t *testing.T, st providers.STTStream) (providers.STTEvent, bool) {
	t.Helper()
	select {
	case e, ok := <-st.Events():
		return e, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no stt event")
		return providers.STTEvent{}, false
	}
}

// a partial is speech, the end of utterance is the final, and the upgrade carries the speaker's identity
func TestSTTRecognizes(t *testing.T) {
	media := make(chan uint32, 64)
	f := newFake(t, func(c *fakeConn) {
		h, n, _, ok := c.nextEvent()
		if !ok || n != 3 || h.streamID != 1 {
			t.Errorf("first utterance: field %d stream %d", n, h.streamID)
			return
		}
		for range 3 {
			if _, stream, b, ok := c.next(); ok && b != nil {
				media <- stream
			}
		}
		c.event(eventHeader{refMessageID: h.messageID}, 13, asr(0, false, "включи"))
		c.event(eventHeader{refMessageID: h.messageID}, 13, asr(0, true, "включи свет"))
		c.next()
	})
	st, err := (&stt{cfg: f.config(t, "")}).Start(context.Background(), providers.STTOptions{SampleRate: 16000, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for range 2 {
		if err := st.Write([]byte{0xFC, 0xFF, 0xFE}); err != nil {
			t.Fatal(err)
		}
	}
	if e, _ := nextEvent(t, st); e.Kind != providers.STTSpeechStarted {
		t.Fatalf("first event %+v", e)
	}
	if e, _ := nextEvent(t, st); e.Kind != providers.STTPartial || e.Text != "включи" {
		t.Fatalf("partial %+v", e)
	}
	if e, _ := nextEvent(t, st); e.Kind != providers.STTFinal || e.Text != "включи свет" {
		t.Fatalf("final %+v", e)
	}
	if s := <-media; s != 1 {
		t.Fatalf("media on stream %d", s)
	}
	h := <-f.header
	for name, want := range map[string]string{
		"X-Uprx-Oauth-Token": testOAuth, "X-Uprx-Auth-Token": testAuth, "X-Uprx-Device-Id": testDeviceID,
		"X-Uprx-Firmware": "1.2.3", "X-Uprx-Device-Model": defaultDeviceModel, "X-Uprx-App-Id": factoryAppID,
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// an utterance that ends empty leaves the next frame to open another, on the next odd stream
func TestSTTEmptyFinalOpensAnotherUtterance(t *testing.T) {
	f := newFake(t, func(c *fakeConn) {
		h, _, _, _ := c.nextEvent()
		c.event(eventHeader{refMessageID: h.messageID}, 13, asr(0, true, ""))
		h2, n, _, ok := c.nextEvent()
		if !ok || n != 3 || h2.streamID != 3 {
			t.Errorf("second utterance: field %d stream %d", n, h2.streamID)
			return
		}
		c.event(eventHeader{refMessageID: h2.messageID}, 13, asr(0, true, "стоп"))
		c.next()
	})
	st, err := (&stt{cfg: f.config(t, "")}).Start(context.Background(), providers.STTOptions{Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_ = st.Write([]byte{1})
	deadline := time.Now().Add(5 * time.Second)
	for {
		// keep feeding: the empty final releases the first utterance, and a frame then opens the second
		_ = st.Write([]byte{2})
		select {
		case e := <-st.Events():
			if e.Kind != providers.STTFinal || e.Text != "стоп" {
				t.Fatalf("event %+v", e)
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("no final from the second utterance")
		}
	}
}

// rejected credentials are unavailable, a failed result is failed; neither carries upstream text, and events close
func TestSTTFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(c *fakeConn, h eventHeader)
		code   string
	}{
		{"invalid auth", func(c *fakeConn, _ eventHeader) { c.event(eventHeader{}, 17, str(nil, 1, "secret upstream text")) }, providers.CodeSTTUnavailable},
		{"error result", func(c *fakeConn, h eventHeader) {
			c.event(eventHeader{refMessageID: h.messageID}, 13, asr(1, false, ""))
		}, providers.CodeSTTFailed},
		{"stream closed with an error", func(c *fakeConn, _ eventHeader) { c.streamClosed(1, 1) }, providers.CodeSTTFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(c *fakeConn) {
				h, _, _, ok := c.nextEvent()
				if !ok {
					return
				}
				tc.answer(c, h)
				c.next()
			})
			st, err := (&stt{cfg: f.config(t, "")}).Start(context.Background(), providers.STTOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			_ = st.Write([]byte{1})
			e, ok := nextEvent(t, st)
			if !ok || e.Kind != providers.STTFailed || e.Code != tc.code || strings.Contains(e.Text, "secret") {
				t.Fatalf("event %+v open %v", e, ok)
			}
			if _, ok := nextEvent(t, st); ok {
				t.Fatal("events stayed open after the failure")
			}
			if st.Write([]byte{1}) == nil {
				t.Fatal("write after the failure accepted")
			}
		})
	}
}

// a refused upgrade is unavailable, and the error names no credential
func TestSTTRefusedUpgrade(t *testing.T) {
	f := newFake(t, nil)
	f.status = 401
	_, err := (&stt{cfg: f.config(t, "")}).Start(context.Background(), providers.STTOptions{})
	if err == nil || err.Error() != providers.CodeSTTUnavailable {
		t.Fatalf("err %v", err)
	}
}

func TestProbe(t *testing.T) {
	answered := newFake(t, func(c *fakeConn) { c.event(eventHeader{}, 24, nil); c.next() })
	if err := probe(context.Background(), &stt{cfg: answered.config(t, "")}); err != nil {
		t.Fatalf("answered: %v", err)
	}
	refused := newFake(t, func(c *fakeConn) { c.event(eventHeader{}, 17, nil); c.next() })
	if err := probe(context.Background(), &tts{cfg: refused.config(t, "")}); !errors.Is(err, providers.ProbeAuth) {
		t.Fatalf("invalid auth: %v", err)
	}
	upgrade := newFake(t, nil)
	upgrade.status = 403
	if err := probe(context.Background(), &stt{cfg: upgrade.config(t, "")}); !errors.Is(err, providers.ProbeAuth) {
		t.Fatalf("403: %v", err)
	}
}

func TestModules(t *testing.T) {
	sec := map[string]string{"oauthToken": "o", "authToken": "a", "deviceId": "d-1"}
	for _, m := range []providers.Module{STTModule, TTSModule} {
		if _, err := m.New([]byte(`{"firmware":"1"}`), sec); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
		if _, err := m.New([]byte(`{"firmware":"1"}`), map[string]string{"oauthToken": "o", "authToken": "a", "deviceId": "bad id!"}); err == nil {
			t.Errorf("%s took a malformed device id", m.ID)
		}
		if _, err := m.New([]byte(`{"firmware":"1"}`), map[string]string{"deviceId": "d"}); err == nil {
			t.Errorf("%s took no tokens", m.ID)
		}
	}
	if strings.Contains(STTModule.ConfigSchema, "voice") || !strings.Contains(TTSModule.ConfigSchema, `"voice"`) {
		t.Fatal("voice belongs to tts alone")
	}
	c := &config{}
	for in, want := range map[string]string{"ru": "ru-RU", "en": "en-US", "": "ru-RU", "tr": "tr"} {
		if got := c.withLanguage(in).lang; got != want {
			t.Errorf("language %q = %q", in, got)
		}
	}
	if got := (&config{language: "uk-UA"}).withLanguage("en").lang; got != "uk-UA" {
		t.Errorf("the instance's language must win: %q", got)
	}
}
