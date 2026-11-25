package voice

import (
	"context"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestResetRequestMatchesWholeUtterance(t *testing.T) {
	for _, tc := range []struct {
		lang, text string
		want       bool
	}{
		{"ru", "Сброс", true},
		{"ru", "  СБРОСЬ РАЗГОВОР! ", true},
		{"ru", "Начнём заново…", true},
		{"ru", "начнем заново", true},
		{"en", "Reset.", true},
		{"en", "START OVER!", true},
		{"en-US", "start over", true}, // an unknown variant uses en, as stop words do
		{"ru", "сброс настроек роутера", false},
		{"ru", "пожалуйста, сбрось разговор", false},
		{"ru", "забудь этот разговор", false}, // the model's tool handles other wordings
		{"en", "reset the router settings", false},
		{"en", "please start over", false},
		{"en", "", false},
	} {
		if got := isResetRequest(tc.text, tc.lang); got != tc.want {
			t.Errorf("%s %q: %v, want %v", tc.lang, tc.text, got, tc.want)
		}
	}
}

func TestResetRequestSkipsTheModelAndSpeaksConfirmation(t *testing.T) {
	for _, tc := range []struct{ lang, text, reply string }{
		{"ru", "Сбрось разговор!", "Начинаем заново."},
		{"en", "Start over.", "Starting over."},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			a := &countingAgent{reply: "The model must not answer."}
			spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
			called := 0
			h := newHarness(t, func(s *setup) {
				s.values.Language, s.values.FollowUp = tc.lang, followUpOn
				s.stt, s.agent, s.tts = heardSaying(tc.text), a.run, spy
				s.reset = func(_ context.Context, deviceID, sessionID, text, reply string) error {
					called++
					if deviceID != dev || sessionID != "s1" || text != tc.text || reply != tc.reply {
						t.Errorf("reset %q %q %q %q", deviceID, sessionID, text, reply)
					}
					return nil
				}
			})
			c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE)
			if c.GetCode() != codeConversationReset || !refused(c) {
				t.Fatalf("close %v", c)
			}
			if a.runs.Load() != 0 || called != 1 {
				t.Fatalf("agent runs %d, resets %d", a.runs.Load(), called)
			}
			if said := strings.Join(spy.said(), ""); said != tc.reply {
				t.Fatalf("tts %q, want %q", said, tc.reply)
			}
			wantFrames(t, h.conn.of("s1"), "SPEECH_STARTED", "stream_start", "audio×15", "stream_end", "close:"+codeConversationReset)
			if heard := h.heard(); len(heard) != 1 || !heard[0] {
				t.Fatalf("session end heard %v", heard)
			}
			h.logFree(t, tc.text, tc.reply)
		})
	}
}
