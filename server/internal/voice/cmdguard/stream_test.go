package cmdguard_test

import (
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

// a long run the chunker cuts at its last whitespace, and one without whitespace it cuts inside the run
var (
	long = strings.Repeat("слово ", 39)
	run  = strings.Repeat("а", 200)
)

func at(text string) cmdguard.Chunk      { return cmdguard.Chunk{Text: text} }
func inRun(text string) cmdguard.Chunk   { return cmdguard.Chunk{Text: text, InRun: true} }
func trimmed(text string) cmdguard.Chunk { return at(strings.TrimSpace(text)) }

// spoken is what the stream gives tts for the chunks, piece by piece
func spoken(lang string, chunks ...cmdguard.Chunk) []string {
	s := cmdguard.NewStream(lang)
	var out []string
	for _, c := range chunks {
		out = append(out, s.Push(c)...)
	}
	return append(out, s.Flush()...)
}

// the chunker trims the whitespace where it cuts, and cuts a run without whitespace inside it; a held word gets one
// space back, but never inside a run
func TestStreamKeepsTheTextTheChunkerCut(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []cmdguard.Chunk
		text   string
	}{
		{"a long run cut at whitespace", []cmdguard.Chunk{trimmed(long + "ну как"), at("дела у тебя?")}, strings.TrimSpace(long) + " ну как дела у тебя?"},
		{"a line break", []cmdguard.Chunk{at("Привет"), at("пока")}, "Привет пока"},
		{"a chunk that starts with whitespace", []cmdguard.Chunk{at("Ну, как"), at(" дела?")}, "Ну, как дела?"},
		{"a stem other than a raise across the cut", []cmdguard.Chunk{at("Просто"), at("попробуй.")}, "Просто попробуй."},
		{"the start of a raise before the cut", []cmdguard.Chunk{at("Это пирог"), at("ром и чай.")}, "Это пирог ром и чай."},
		{"a raise that ends at the cut", []cmdguard.Chunk{at("Сделай громче"), at("музыку.")}, "Сделай сильнее музыку."},
		{"a run cut without whitespace", []cmdguard.Chunk{inRun(run), at("а дальше.")}, run + "а дальше."},
		{"a run cut before an emoji", []cmdguard.Chunk{inRun(run[4:]), at("😀 дальше.")}, run[4:] + "😀 дальше."},
		{"a raise cut inside its run", []cmdguard.Chunk{inRun("Сделай гро"), at("мче, пожалуйста.")}, "Сделай сильнее, пожалуйста."},
		{"a raise read across a trimmed space", []cmdguard.Chunk{at("Сделай гро"), at("мче.")}, "Сделай сильнее."},
		{"a raise that may run on into a later chunk", []cmdguard.Chunk{at("Это гро"), at("м"), at("че.")}, "Это сильнее."},
		{"sentences", []cmdguard.Chunk{at("Привет."), at("Как дела?")}, "Привет. Как дела?"},
	} {
		if got := strings.Join(spoken("ru", c.chunks...), ""); got != c.text {
			t.Errorf("%s: got %q, want %q", c.name, got, c.text)
		}
	}
}

// in en only a word with a cyrillic letter, or one whose end may begin a raise, waits; the join takes over the space
// the chunker trimmed, so other english reaches tts piece by piece as before
func TestStreamInEnglish(t *testing.T) {
	if got := spoken("en", at("Hello there."), at("How are you?"), at("Fine")); !slices.Equal(got, []string{"Hello there.", " How are you?", " Fine"}) {
		t.Fatalf("plain: %q", got)
	}
	for _, c := range []struct {
		name   string
		chunks []cmdguard.Chunk
		text   string
	}{
		{"a cyrillic word cut inside its run", []cmdguard.Chunk{inRun("Say гром"), at("че now.")}, "Say сильнее now."},
		{"a cyrillic stop cut inside its run", []cmdguard.Chunk{inRun("Say сто"), at("п now.")}, "Say всё now."},
		{"a latin raise cut inside its run", []cmdguard.Chunk{inRun("Say gro"), at("mche now.")}, "Say сильнее now."},
		{"a latin raise read across a trimmed space", []cmdguard.Chunk{at("Say g"), at("romche now.")}, "Say сильнее now."},
		{"english stop across a cut", []cmdguard.Chunk{inRun("Please st"), at("op it.")}, "Please stop it."},
	} {
		if got := strings.Join(spoken("en", c.chunks...), ""); got != c.text {
			t.Errorf("%s: got %q, want %q", c.name, got, c.text)
		}
	}
	// what does not wait goes out at once
	s := cmdguard.NewStream("en")
	if got := s.Push(inRun("Please st")); !slices.Equal(got, []string{"Please st"}) {
		t.Fatalf("held english: %q", got)
	}
	if got := s.Push(inRun("Say gro")); !slices.Equal(got, []string{"Say "}) {
		t.Fatalf("a raise's start went out: %q", got)
	}
}

// the accepted gap: in en a latin look-alike fragment that ends a chunk cut inside its run, with the cyrillic rest in
// the next, is never held; it can only form стоп, хватит or тише, never a raise
func TestStreamAcceptedGapInEnglish(t *testing.T) {
	if got := strings.Join(spoken("en", inRun("Say C"), at("ТОП now.")), ""); got != "Say CТОП now." {
		t.Fatalf("got %q", got)
	}
}

// a held word waits for the next chunk; a reset (the speaker's discard) drops it with the unfinished sentence, and the
// next chunk starts afresh, without a joining space
func TestStreamResetDropsTheHeldWord(t *testing.T) {
	s := cmdguard.NewStream("ru")
	if got := s.Push(inRun("Сделай гро")); !slices.Equal(got, []string{"Сделай "}) {
		t.Fatalf("before the held word: %q", got)
	}
	s.Reset()
	got := append(s.Push(at("мче музыку.")), s.Flush()...)
	if !slices.Equal(got, []string{"мче музыку."}) {
		t.Fatalf("after the reset: %q", got)
	}
}

// whitespace never goes to tts on its own, and the held tail is rewritten at the end
func TestStreamFlushesTheHeldTail(t *testing.T) {
	s := cmdguard.NewStream("ru")
	if got := s.Push(at("Готово.")); !slices.Equal(got, []string{"Готово."}) {
		t.Fatalf("first: %q", got)
	}
	if got := s.Push(at("Стоп")); len(got) != 0 {
		t.Fatalf("a held word went out with its space: %q", got)
	}
	if got := s.Flush(); !slices.Equal(got, []string{" Всё"}) {
		t.Fatalf("flush: %q", got)
	}
	if got := s.Flush(); len(got) != 0 {
		t.Fatalf("second flush: %q", got)
	}
	if got := s.Push(at("Привет.")); !slices.Equal(got, []string{"Привет."}) {
		t.Fatalf("after a flush the stream starts afresh: %q", got)
	}
}
