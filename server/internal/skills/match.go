package skills

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// words is s lowercased, ё read as е, split at anything but letters and
// digits
func words(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// sameWord is a stem-tolerant match of a spoken word and a trigger's word:
// one is a prefix of the other ("таймер", "таймера"), or they share all but
// the last three letters of the shorter and at least four ("погода",
// "погоду"; "включи", "включить"). Words of three letters or fewer match
// whole only, so "на" never matches "наушники".
func sameWord(heard, trigger string) bool {
	if heard == trigger {
		return true
	}
	h, t := utf8.RuneCountInString(heard), utf8.RuneCountInString(trigger)
	if min(h, t) <= 3 {
		return false
	}
	p := commonPrefix(heard, trigger)
	return p == min(h, t) || p >= max(4, min(h, t)-3)
}

func commonPrefix(a, b string) int {
	n := 0
	for a != "" && b != "" {
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			break
		}
		n++
		a, b = a[sa:], b[sb:]
	}
	return n
}

// phraseScore is how much a trigger phrase matches the input's words: its
// word count when every word of it is heard (in any order), else 0, so a
// phrase outweighs a lone word
func phraseScore(heard []string, phrase string) int {
	ws := words(phrase)
	if len(ws) == 0 {
		return 0
	}
	for _, w := range ws {
		found := false
		for _, h := range heard {
			if sameWord(h, w) {
				found = true
				break
			}
		}
		if !found {
			return 0
		}
	}
	return len(ws)
}

// keywordScore sums a skill's trigger phrases that match the input
func keywordScore(heard []string, triggers []string) int {
	n := 0
	for _, t := range triggers {
		n += phraseScore(heard, t)
	}
	return n
}
