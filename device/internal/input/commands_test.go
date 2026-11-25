package input_test

import (
	"testing"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// the list rides Configure's command_words, whose nanopb side is capped (audiod.options): more or longer
// phrases than it takes and audiod refuses the whole envelope undecoded, which crash-loops it
func TestCommandWordsFitTheWire(t *testing.T) {
	words := input.CommandWords()
	if len(words) > 32 {
		t.Fatalf("%d words, the envelope takes 32", len(words))
	}
	seen := make(map[string]bool, len(words))
	for _, w := range words {
		if seen[w] {
			t.Fatalf("a phrase twice: %q", w)
		}
		seen[w] = true
		if utf8.RuneCountInString(w) == 0 || utf8.RuneCountInString(w) > 64 {
			t.Fatalf("a phrase off the wire's sizes: %q", w)
		}
	}
}
