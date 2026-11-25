// Package cmdguard keeps command words out of what the server sends to TTS
// (M6a-D6). The device acts on тише heard within 2 s of a reply frame, the
// server on a final transcript of стоп, хватит or пауза, and громче is a voice
// request, while the microphone hears the speaker's own replies: a reply must
// never say one (rule 7: an echo громче must not raise the volume).
//
// The stems стоп, хватит, пауз, тише and громче are matched inside any word,
// read the way a voice reads them: Cyrillic as written, Latin look-alikes in
// both languages, and Latin sound-alikes in ru for every stem but in en only
// for громче, so English stop and pause pass in en. A word runs through
// combining marks, format characters, '+' stress markup, and a hyphen or an
// apostrophe before a letter, so none of them can hide a stem. A word without
// a stem comes back byte for byte; one with a stem is replaced or loses the
// stem's letters.
//
// Accepted gap (as in v1): in en, a Latin look-alike fragment that ends a
// chunk cut inside its run, with the Cyrillic rest in the next chunk (C | ТОП),
// reaches TTS unguarded. It can only form стоп, хватит or тише (cancel or
// lower), never громче: a word whose end may begin one waits.
package cmdguard

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// the stems the device's stock command model acts on, in its order
var stems = []string{"стоп", "хватит", "пауз", "тише", "громче"}

// the stem that raises the volume (rule 7)
const raiseStem = "громче"

const (
	pauseStem = "пауз"
	pauseWord = "перерыв"
)

// whole words a voice still understands, said instead
var replacements = []struct{ word, said string }{
	{"громче", "сильнее"},
	{"погромче", "сильнее"},
	{"тише", "слабее"},
	{"потише", "слабее"},
	{"стоп", "всё"},
	{"хватит", "достаточно"},
}

// tables are one language's readings
type tables struct {
	stems []reading
	// en waits only on a trailing word that may hold a cyrillic letter or begin a raise
	waitAll bool
}

var (
	raise = readingOf(raiseStem, true)
	// only for a word that has a stem, so read by look and sound: they pick a speakable replacement
	wholeWords = func() []struct {
		r    reading
		said string
	} {
		out := make([]struct {
			r    reading
			said string
		}, len(replacements))
		for i, w := range replacements {
			out[i].r, out[i].said = readingOf(w.word, true), w.said
		}
		return out
	}()
	pauses = []reading{readingOf(pauseStem, true)}

	ruTables = tables{stems: readingsFor(true), waitAll: true}
	enTables = tables{stems: readingsFor(false)}

	// the most letters one reading of a stem takes: a reading that crosses a join starts no further back
	span = func() int {
		most := 0
		for _, r := range ruTables.stems {
			most = max(most, r.longest())
		}
		return most
	}()
)

// cyrillic reads as written and latin by look in both languages; a russian voice reads latin by sound as well, so ru
// guards every stem by sound and en only a raise
func readingsFor(ru bool) []reading {
	out := make([]reading, len(stems))
	for i, s := range stems {
		out[i] = readingOf(s, ru || s == raiseStem)
	}
	return out
}

// tablesFor is ru for "ru" and en for any other language
func tablesFor(lang string) *tables {
	if lang == "ru" {
		return &ruTables
	}
	return &enTables
}

// Rewrite returns text in which no word reads as a command stem in lang; text
// without one comes back byte for byte.
func Rewrite(text, lang string) string { return tablesFor(lang).rewriteText(text) }

func (t *tables) rewriteText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if !unicode.IsLetter(r) {
			b.WriteString(text[i : i+n])
			i += n
			continue
		}
		end := wordEnd(text, i+n)
		b.WriteString(t.rewriteWord(text[i:end]))
		i = end
	}
	return b.String()
}

// rewriteWord rebuilds a word with a stem from its letters alone
func (t *tables) rewriteWord(word string) string {
	letters := lettersOf(word)
	heard := heardOf(letters)
	if _, _, ok := findStem(heard, t.stems); !ok {
		return word
	}
	said, whole := "", false
	for _, w := range wholeWords {
		if w.r.endsAt(heard, 0, len(heard)) {
			said, whole = w.said, true
			break
		}
	}
	if !whole {
		if _, _, ok := findStem(heard, pauses); ok {
			said = pauseWord
		} else {
			said = withoutStems(letters, heard, t.stems)
		}
	}
	if startsUpper(word) {
		return capitalised(said)
	}
	return said
}

// a removal can join the letters around it into another stem, so stems go until none is left
func withoutStems(letters, heard []rune, rs []reading) string {
	for start, length, ok := findStem(heard, rs); ok; start, length, ok = findStem(heard, rs) {
		letters = append(letters[:start], letters[start+length:]...)
		heard = append(heard[:start], heard[start+length:]...)
	}
	return string(letters)
}

// findStem is the first stem read anywhere in heard, compared letter by letter so a match maps back onto the letters
func findStem(heard []rune, rs []reading) (start, length int, ok bool) {
	for _, r := range rs {
		for start := range heard {
			if ends, _ := r.read(heard, start); len(ends) > 0 {
				return start, slicesMax(ends) - start, true
			}
		}
	}
	return 0, 0, false
}

func slicesMax(xs []int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		m = max(m, x)
	}
	return m
}

func startsUpper(word string) bool {
	r, _ := utf8.DecodeRuneInString(word)
	return unicode.ToLower(r) != r
}

func capitalised(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
