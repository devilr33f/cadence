package voice

import (
	"regexp"
	"strings"
)

var wordPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)

// words are text's words as recognition writes them back: lower case, ё as е, no punctuation
func words(text string) []string {
	return wordPattern.FindAllString(strings.ReplaceAll(strings.ToLower(text), "ё", "е"), -1)
}

// isEcho: the microphone hears the speaker's own reply, and recognition writes it back as a run of the reply's words,
// wherever it starts, forgiving one misheard word in four; the reply's words in another order are speech
func isEcho(heard, spoken []string) bool {
	forgiven := len(heard) / 4
	for start := 0; start+len(heard) <= len(spoken); start++ {
		missed := 0
		for i := 0; i < len(heard) && missed <= forgiven; i++ {
			if heard[i] != spoken[start+i] {
				missed++
			}
		}
		if missed <= forgiven {
			return true
		}
	}
	return false
}
