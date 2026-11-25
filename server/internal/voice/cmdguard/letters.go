package cmdguard

import (
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// noLetter stands for a letter a voice reads as several (a hangul syllable): it is part of no stem
const noLetter rune = -1

// latin letters with a caron a voice reads as these cyrillic ones, which stripping the mark would lose
var carons = map[rune]rune{'č': 'ч', 'š': 'ш', 'ž': 'ж'}

// joiner is '+' stress markup, a hyphen or an apostrophe: part of a word when a letter, mark or format character follows
func joiner(r rune) bool {
	switch r {
	case '+', '-', '\u2010', '\u2011', '\'', '\u2019':
		return true
	}
	return false
}

// inWord is a letter, a combining mark or a format character (soft hyphen, zero-width joiner)
func inWord(r rune) bool { return unicode.In(r, unicode.L, unicode.M, unicode.Cf) }

// wordish may be part of a word still to come: what follows a trailing joiner decides
func wordish(r rune) bool { return inWord(r) || joiner(r) }

// hidden is what a voice reads a word through
func hidden(r rune) bool { return unicode.In(r, unicode.M, unicode.Cf) || joiner(r) }

// wordEnd is the end of the word whose first letter ends at i
func wordEnd(text string, i int) int {
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if inWord(r) {
			i += n
			continue
		}
		if next, _ := utf8.DecodeRuneInString(text[i+n:]); joiner(r) && i+n < len(text) && inWord(next) {
			i += n
			continue
		}
		break
	}
	return i
}

// lettersOf is text composed first, so a caron typed as a combining mark stays on its letter, without what is hidden
func lettersOf(text string) []rune {
	var out []rune
	for _, r := range norm.NFC.String(text) {
		if !hidden(r) {
			out = append(out, r)
		}
	}
	return out
}

func heardOf(letters []rune) []rune {
	out := make([]rune, len(letters))
	for i, l := range letters {
		out[i] = heardAs(l)
	}
	return out
}

func heardIn(text string) []rune { return heardOf(lettersOf(text)) }

// heardAs is the letter a voice reads: a caron letter as its cyrillic one, any other its base without marks, in
// lower case
func heardAs(r rune) rune {
	lower := unicode.ToLower(r)
	if c, ok := carons[lower]; ok {
		return c
	}
	base := noLetter
	for _, d := range norm.NFD.String(string(lower)) {
		if unicode.Is(unicode.M, d) {
			continue
		}
		if base != noLetter {
			return noLetter
		}
		base = d
	}
	return base
}

func lastRunes(rs []rune, n int) []rune {
	if len(rs) > n {
		return rs[len(rs)-n:]
	}
	return rs
}
