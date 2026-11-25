package voice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// noMatch marks a row that must not match the table
const noMatch directKind = 200

// the table takes whole utterances as recognition writes them back, forgives politeness, and takes the home's
// включи/выключи by prefix; everything else stays the model's
func TestMatchDirect(t *testing.T) {
	for _, tc := range []struct {
		lang, text string
		want       directKind
		target     string
	}{
		{"ru", "Громче", volumeUp, ""},
		{"ru", "ПОГРОМЧЕ!", volumeUp, ""},
		{"ru", "громкость больше", volumeUp, ""},
		{"ru", "Сделай погромче", volumeUp, ""},
		{"ru", "прибавь звук", volumeUp, ""},
		{"ru", "громкость ниже", volumeDown, ""},
		{"ru", "потише, пожалуйста", volumeDown, ""},
		{"ru", "тише пожалуйста", volumeDown, ""},
		{"ru", "Сделай тише", volumeDown, ""},
		{"ru", "убавь громкость", volumeDown, ""},
		{"en", "Louder.", volumeUp, ""},
		{"en", "turn it up", volumeUp, ""},
		{"en", "volume down please", volumeDown, ""},
		{"en-US", "quieter", volumeDown, ""}, // an unknown variant uses en, as the stop words do
		{"ru", "продолжи", mediaResume, ""},
		{"ru", "Продолжай музыку", mediaResume, ""},
		{"ru", "дальше", mediaNext, ""},
		{"ru", "следующий трек", mediaNext, ""},
		{"ru", "предыдущая песня", mediaPrevious, ""},
		{"en", "skip", mediaNext, ""},
		{"en", "last song", mediaPrevious, ""},
		{"ru", "включи свет", homeOn, "свет"},
		{"ru", "Выключи свет на кухне", homeOff, "свет на кухне"},
		{"en", "turn on the light", homeOn, "the light"},
		{"en", "switch off the lamp", homeOff, "the lamp"},
		{"ru", "включи музыку", homeOn, "музыку"}, // the home resolves nothing, so the model answers
		{"ru", "пожалуйста", noMatch, ""},
		{"ru", "включи", noMatch, ""},
		{"ru", "громче музыки", noMatch, ""}, // a relative volume with its own words is the model's
		{"ru", "включи пожалуйста", noMatch, ""},
		{"en", "stop the music", noMatch, ""},
		{"ru", "", noMatch, ""},
	} {
		got := matchDirect(tc.text, tc.lang)
		if tc.want == 200 {
			if got != nil {
				t.Errorf("%s %q: matched %+v", tc.lang, tc.text, got)
			}
			continue
		}
		if got == nil || got.kind != tc.want || got.target != tc.target {
			t.Errorf("%s %q: %+v, want %v %q", tc.lang, tc.text, got, tc.want, tc.target)
		}
	}
}

// the volume words run on the device's own audio.output and end the turn silently, stored like any turn; a raise
// needs the woken session rule 7 gives the model, and a command the device refused is the model's to answer
func TestDirectVolumeCommands(t *testing.T) {
	ok := func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
		return json.RawMessage(`{"level":40}`), nil
	}
	t.Run("quieter in a wake", func(t *testing.T) {
		a := &countingAgent{reply: "The model must not answer."}
		h := newHarness(t, func(s *setup) {
			s.values.Language = "ru"
			s.stt, s.agent, s.command = heardSaying("Потише"), a.run, ok
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || offered(c) {
			t.Fatalf("close %v", c)
		}
		if a.runs.Load() != 0 {
			t.Fatalf("the model answered")
		}
		if got := h.sentCommands(); len(got) != 1 || got[0].capability != "audio.output" || got[0].name != "change_level" ||
			string(got[0].args) != `{"delta":-10,"sessionId":"s1"}` {
			t.Fatalf("commands %+v", got)
		}
		if turns := h.storedTurns(); len(turns) != 1 || turns[0].text != "Потише" || turns[0].reply != "" || turns[0].trigger != "wake" {
			t.Fatalf("stored %+v", turns)
		}
		wantFrames(t, h.conn.of("s1"), "SPEECH_STARTED", "close:")
		if line := h.logHas("direct_command"); line == nil || line["kind"] != "volume_down" || line["spoken"] != false {
			t.Fatalf("log %v", line)
		}
		if heard := h.heard(); len(heard) != 1 || !heard[0] {
			t.Fatalf("session end heard %v", heard)
		}
	})
	t.Run("louder in a wake", func(t *testing.T) {
		a := &countingAgent{reply: "The model must not answer."}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent, s.command = heardSaying("louder"), a.run, ok
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_BUTTON); c.GetCode() != "" {
			t.Fatalf("close %v", c)
		}
		if got := h.sentCommands(); len(got) != 1 || string(got[0].args) != `{"delta":10,"sessionId":"s1"}` {
			t.Fatalf("commands %+v", got)
		}
	})
	t.Run("louder in a follow-up is the model's", func(t *testing.T) {
		a := &countingAgent{reply: "Sure."}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent, s.command = heardSaying("louder"), a.run, ok
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_FOLLOW_UP); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
		if got := h.sentCommands(); len(got) != 0 {
			t.Fatalf("commands %+v", got)
		}
	})
	t.Run("a refused change is the model's", func(t *testing.T) {
		a := &countingAgent{reply: "Sure."}
		refused := func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
			return nil, &commands.DeviceError{Code: "wake_required"}
		}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent, s.command = heardSaying("quieter"), a.run, refused
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
	})
}

// the player's skips and resumes run on media.player; with nothing playing the turn says so and says nothing else
func TestDirectMediaCommands(t *testing.T) {
	t.Run("next, silent", func(t *testing.T) {
		a := &countingAgent{reply: "The model must not answer."}
		h := newHarness(t, func(s *setup) {
			s.values.Language = "ru"
			s.stt, s.agent = heardSaying("Дальше"), a.run
			s.command = func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			}
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" {
			t.Fatalf("close %v", c)
		}
		if a.runs.Load() != 0 {
			t.Fatalf("the model answered")
		}
		if got := h.sentCommands(); len(got) != 1 || got[0].capability != "media.player" || got[0].name != "next" {
			t.Fatalf("commands %+v", got)
		}
		wantFrames(t, h.conn.of("s1"), "SPEECH_STARTED", "close:")
	})
	t.Run("next with nothing playing", func(t *testing.T) {
		a := &countingAgent{reply: "The model must not answer."}
		spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
		h := newHarness(t, func(s *setup) {
			s.values.Language = "ru"
			s.stt, s.agent, s.tts = heardSaying("дальше"), a.run, spy
			s.command = func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
				return nil, &commands.DeviceError{Code: "no_session"}
			}
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || offered(c) {
			t.Fatalf("close %v", c)
		}
		if a.runs.Load() != 0 {
			t.Fatalf("the model answered")
		}
		if said := strings.Join(spy.said(), ""); said != "Ничего не играет." {
			t.Fatalf("tts %q", said)
		}
		if turns := h.storedTurns(); len(turns) != 1 || turns[0].reply != "Ничего не играет." {
			t.Fatalf("stored %+v", turns)
		}
		h.logFree(t, "дальше")
	})
	t.Run("a resume with nothing playing is the model's", func(t *testing.T) {
		// media_control restarts a queue that ended; the direct path cannot
		a := &countingAgent{reply: "Sure."}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent = heardSaying("resume"), a.run
			s.command = func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
				return nil, &commands.DeviceError{Code: "no_session"}
			}
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
	})
	t.Run("a failed skip is the model's", func(t *testing.T) {
		a := &countingAgent{reply: "Sure."}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent = heardSaying("next"), a.run
			s.command = func(context.Context, string, string, string, []byte) (json.RawMessage, error) {
				return nil, commands.ErrTimeout
			}
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
	})
}

// включи/выключи by name turns the device through the home the model's own tool uses; a name it cannot settle alone
// is the model's to ask about. the command is the whole answer: the session ends without a follow-up offer,
// follow-ups on or not, so the microphone stops with the turn
func TestDirectHomeCommands(t *testing.T) {
	t.Run("on, one match", func(t *testing.T) {
		a := &countingAgent{reply: "The model must not answer."}
		spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
		h := newHarness(t, func(s *setup) {
			s.values.Language, s.values.FollowUp = "ru", followUpOn
			s.stt, s.agent, s.tts = heardSaying("включи свет"), a.run, spy
			s.homeControl = func(_ context.Context, call HomeCall) (string, bool) {
				if call.DeviceID != dev || call.SessionID != "s1" || call.Target != "свет" ||
					call.Trigger != cadencev2.Trigger_TRIGGER_WAKE || !call.On {
					t.Errorf("call %+v", call)
				}
				return "Свет в кухне", true
			}
		})
		c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE)
		if c.GetCode() != "" || offered(c) {
			t.Fatalf("close %v", c)
		}
		if a.runs.Load() != 0 {
			t.Fatalf("the model answered")
		}
		if got := h.homeCalls(); len(got) != 1 || got[0].Target != "свет" {
			t.Fatalf("homes %+v", got)
		}
		if said := strings.Join(spy.said(), ""); said != "Включаю Свет в кухне." {
			t.Fatalf("tts %q", said)
		}
		if turns := h.storedTurns(); len(turns) != 1 || turns[0].reply != "Включаю Свет в кухне." {
			t.Fatalf("stored %+v", turns)
		}
		h.logFree(t, "включи свет")
	})
	t.Run("off, en", func(t *testing.T) {
		a := &countingAgent{reply: "no"}
		spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
		h := newHarness(t, func(s *setup) {
			s.stt, s.agent, s.tts = heardSaying("turn off the lamp"), a.run, spy
			s.homeControl = func(context.Context, HomeCall) (string, bool) { return "Lamp", true }
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" {
			t.Fatalf("close %v", c)
		}
		if said := strings.Join(spy.said(), ""); said != "Turning off Lamp." {
			t.Fatalf("tts %q", said)
		}
	})
	t.Run("unsettled is the model's", func(t *testing.T) {
		a := &countingAgent{reply: "Which light?"}
		h := newHarness(t, func(s *setup) {
			s.values.Language = "ru"
			s.stt, s.agent = heardSaying("включи свет"), a.run
			s.homeControl = func(context.Context, HomeCall) (string, bool) { return "", false }
		})
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
		if turns := h.storedTurns(); len(turns) != 0 {
			t.Fatalf("stored %+v", turns)
		}
	})
}

// a home phrase the spotter heard on an idle speaker runs as the direct table runs it: the speaker's room's
// lights, silently, stored like any turn — and nothing else runs at all, no model and no reply
func TestWordHeard(t *testing.T) {
	a := &countingAgent{reply: "no"}
	h := newHarness(t, func(s *setup) {
		s.values.Language = "ru"
		s.agent = a.run
		s.homeControl = func(_ context.Context, call HomeCall) (string, bool) {
			if call.DeviceID != dev || call.Target != "свет" || call.Trigger != cadencev2.Trigger_TRIGGER_WORD {
				t.Errorf("call %+v", call)
			}
			if call.On {
				return "", false // включи свет below: a name the home cannot settle
			}
			return "свет", true
		}
	})
	h.c.Handle(h.ctx, h.conn, &cadencev2.Envelope{Body: &cadencev2.Envelope_WordHeard{
		WordHeard: &cadencev2.WordHeard{Word: "выключи свет"},
	}})
	if a.runs.Load() != 0 {
		t.Fatalf("the model answered")
	}
	if got := h.homeCalls(); len(got) != 1 || got[0].Target != "свет" || got[0].TurnID == "" {
		t.Fatalf("homes %+v", got)
	}
	turns := h.storedTurns()
	if len(turns) != 1 || turns[0].text != "выключи свет" || turns[0].reply != "" || turns[0].trigger != "word" {
		t.Fatalf("stored %+v", turns)
	}
	if line := h.logHas("direct_command"); line == nil || line["kind"] != "home_off" || line["heard"] != true {
		t.Fatalf("log %v", line)
	}
	h.logFree(t, "выключи свет")

	// a phrase the direct table does not settle is dropped without a word
	h.c.Handle(h.ctx, h.conn, &cadencev2.Envelope{Body: &cadencev2.Envelope_WordHeard{
		WordHeard: &cadencev2.WordHeard{Word: "дальше"},
	}})
	if len(h.homeCalls()) != 1 || len(h.storedTurns()) != 1 || a.runs.Load() != 0 {
		t.Fatalf("an unsettled word did something: homes %d turns %d runs %d", len(h.homeCalls()), len(h.storedTurns()), a.runs.Load())
	}

	// a name the home cannot settle is refused in silence, the model told nothing
	h.c.Handle(h.ctx, h.conn, &cadencev2.Envelope{Body: &cadencev2.Envelope_WordHeard{
		WordHeard: &cadencev2.WordHeard{Word: "включи свет"},
	}})
	if len(h.homeCalls()) != 2 || len(h.storedTurns()) != 1 {
		t.Fatalf("a refused word stored or ran again")
	}
	if h.logHas("word_heard_refused") == nil {
		t.Fatalf("no refusal logged")
	}
}
