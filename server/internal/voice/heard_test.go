package voice

import (
	"slices"
	"testing"
)

func TestWordsAsRecognitionWritesThem(t *testing.T) {
	if got := words("Ёлка, СТОП! it's 3:45…"); !slices.Equal(got, []string{"елка", "стоп", "it", "s", "3", "45"}) {
		t.Fatalf("got %q", got)
	}
}

// stop words alone ask for silence, not an answer (M6a-D6, v1 isStopRequest)
func TestStopRequest(t *testing.T) {
	for _, c := range []struct {
		text, lang string
		stop       bool
	}{
		{"стоп", "ru", true},
		{"Стоп!", "ru", true},
		{"стоп стоп", "ru", true},
		{"Хватит, пауза.", "ru", true},
		{"ПАУЗА", "ru", true},
		{"стоп музыку", "ru", false},
		{"стой", "ru", false},
		{"stop", "ru", false},
		{"stop", "en", true},
		{"Stop. Stop!", "en", true},
		{"stop the music", "en", false},
		{"стоп", "en", false},
		{"stop", "de", true},
		{"…", "ru", false},
		{"", "en", false},
	} {
		if got := isStopRequest(c.text, c.lang); got != c.stop {
			t.Errorf("%s %q: %v", c.lang, c.text, got)
		}
	}
}

// echo is a run of the reply's words, wherever it starts, forgiving one misheard word in four; the reply's words in
// another order are speech
func TestEcho(t *testing.T) {
	spoken := words("It is sunny and warm today, enjoy the walk.")
	for _, c := range []struct {
		heard string
		echo  bool
	}{
		{"sunny and warm today", true},
		{"It is sunny", true},
		{"sunny and worm today", true},
		{"sunny und worm today", false},
		{"enjoy the walk", true},
		{"today warm and sunny", false},
		{"what about tomorrow", false},
		{"walk the dog", false},
		{"enjoy the walk please", false},
	} {
		if got := isEcho(words(c.heard), spoken); got != c.echo {
			t.Errorf("%q: %v", c.heard, got)
		}
	}
	// ё reads as е, as in the reply's own words
	if !isEcho(words("Всё готово"), words("Все готово, приятного вечера.")) {
		t.Fatal("ё not read as е")
	}
}
