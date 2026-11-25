package voice

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// cutAfter pads head so the chunker cuts at the space after it, at maxChunkRunes
func cutAfter(head string) string {
	return strings.Repeat("я", maxChunkRunes-utf8.RuneCountInString(head)-1) + " " + head
}

func guardedHarness(t *testing.T, run agentFunc) (*harness, *spyTTS) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	h := newHarness(t, func(s *setup) {
		s.values.Language = "ru"
		s.tts, s.agent = spy, run
	})
	return h, spy
}

// M6a-D6: every reply passes the guard on its way to tts, a word the chunker's cut left hanging included
func TestReplyReachesTTSGuarded(t *testing.T) {
	head := cutAfter("Сделай громче")
	for name, tc := range map[string]struct {
		run  agentFunc
		said string
	}{
		"one chunk":                     {says("Сделай громче", " музыку."), "Сделай сильнее музыку."},
		"held at a cut":                 {says(head + " музыку."), strings.TrimSuffix(head, "громче") + "сильнее музыку."},
		"joined stem, then a lone word": {says("Не вышло: стоп-кран.", " Хватит?"), "Не вышло: кран. Достаточно?"},
	} {
		t.Run(name, func(t *testing.T) {
			h, spy := guardedHarness(t, tc.run)
			if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" {
				t.Fatalf("close %v", c)
			}
			if said := strings.Join(spy.said(), ""); said != tc.said {
				t.Fatalf("tts heard %q, want %q", said, tc.said)
			}
			h.logFree(t, tc.said, "громче музыку стоп хватит")
		})
	}
}

// a discard drops the held word with the unfinished sentence (M3-R9)
func TestDiscardDropsTheHeldWord(t *testing.T) {
	h, spy := guardedHarness(t, func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
		out <- agent.Out{Speech: cutAfter("Сделай громче") + " ещё"}
		out <- agent.Out{Discard: true}
		out <- agent.Out{Speech: "Готово."}
		return agent.Result{}
	})
	h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE)
	said := spy.said()
	if len(said) != 2 || said[0] != strings.TrimSuffix(cutAfter("Сделай громче"), "громче") || said[1] != "Готово." {
		t.Fatalf("tts heard %q", said)
	}
}

// an announcement is guarded whole, in the language it is spoken in
func TestAnnouncementReachesTTSGuarded(t *testing.T) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	r := newAnnounceRig(t, func(s *setup) { s.tts = spy })
	id := r.announce(t, `{"text":"Стоп, хватит, сделай погромче.","language":"ru"}`)
	waitFor(t, "the announcement's end", func() bool {
		for _, e := range r.stream(id) {
			if e.GetStreamEnd() != nil {
				return true
			}
		}
		return false
	})
	if said := strings.Join(spy.said(), ""); said != "Всё, достаточно, сделай сильнее." {
		t.Fatalf("tts heard %q", said)
	}
	r.logFree(t, "стоп хватит погромче сделай")
}
