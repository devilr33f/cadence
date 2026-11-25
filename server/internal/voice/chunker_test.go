package voice

import (
	"strings"
	"testing"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

func sentences(parts ...string) []string {
	var out []string
	for _, c := range chunks(parts...) {
		out = append(out, c.Text)
	}
	return out
}

func chunks(parts ...string) []cmdguard.Chunk {
	var c chunker
	var out []cmdguard.Chunk
	for _, p := range parts {
		out = append(out, c.push(p)...)
	}
	return append(out, c.rest()...)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSplitsSentences(t *testing.T) {
	got := sentences("Hi there. How are", " you? Fine")
	if want := []string{"Hi there.", "How are you?", "Fine"}; !equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	// a terminator at a chunk boundary waits for what follows it
	got = sentences("It is 3.", "5 degrees! Wait… ", "ok")
	if want := []string{"It is 3.5 degrees!", "Wait…", "ok"}; !equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHardSplitAt200(t *testing.T) {
	words := strings.Repeat("слово ", 60) // 360 runes, no terminator
	cut := len(strings.Repeat("слово ", 9) + "сл")
	got := sentences(words[:cut], words[cut:])
	if len(got) != 2 {
		t.Fatalf("%d chunks, want 2", len(got))
	}
	for _, s := range got {
		if n := utf8.RuneCountInString(s); n > 200 {
			t.Fatalf("chunk of %d runes", n)
		}
	}
	// a cut inside a word would put a space into it here
	if strings.Join(got, " ") != strings.TrimSpace(words) {
		t.Fatal("text lost, changed or split inside a word")
	}
}

func TestDropsPunctuationOnly(t *testing.T) {
	got := sentences("... ", "! ? ", "Ok. ", " — . ", "2.")
	if want := []string{"Ok.", "2."}; !equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

// the guard joins the chunks itself, so it needs to know where the chunker cut a run without whitespace
func TestTellsACutInsideARun(t *testing.T) {
	run := strings.Repeat("а", 250)
	got := chunks("Hi there. "+strings.Repeat("слово ", 40), run+" end")
	want := []bool{false, false, false, true, false}
	if len(got) != len(want) {
		t.Fatalf("%d chunks: %+v", len(got), got)
	}
	for i, c := range got {
		if c.InRun != want[i] {
			t.Fatalf("chunk %d %q: in run %v", i, c.Text, c.InRun)
		}
	}
	if got[3].Text+got[4].Text != run+" end" {
		t.Fatalf("run split %q | %q", got[3].Text, got[4].Text)
	}
}

// citations are shown on screens and never spoken
func TestStripsCitations(t *testing.T) {
	got := sentences("Frieren airs on Fridays [1]. It has 28 episodes [2, 3]", ". [4]", " Done [", "5].")
	want := []string{"Frieren airs on Fridays.", "It has 28 episodes.", "Done."}
	if !equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := sentences("Rated [12+] by year 2023 [x]."); !equal(got, []string{"Rated [12+] by year 2023 [x]."}) {
		t.Fatalf("not a citation: %q", got)
	}
}
