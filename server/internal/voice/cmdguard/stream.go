package cmdguard

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunk is one piece of a reply as the chunker cut it, trimmed. InRun says
// the chunker cut it inside a run without whitespace, so the next chunk goes
// on in the same word.
type Chunk struct {
	Text  string
	InRun bool
}

// Stream guards a reply chunk by chunk and joins the chunks itself: TTS
// joins what it is sent as it is, and a stem can span chunks. A chunk's
// trailing word waits for the next chunk or the end (in en only one with a
// Cyrillic letter or whose end may begin a raise, so other English goes out
// at once). A Stream is used from one goroutine.
type Stream struct {
	t     *tables
	held  string
	inRun bool
	// a chunk came since the start, the last Reset or Flush
	started bool
}

func NewStream(lang string) *Stream { return &Stream{t: tablesFor(lang)} }

// Push returns what of c may go to TTS now, rewritten; never whitespace alone.
func (s *Stream) Push(c Chunk) []string {
	if strings.TrimSpace(c.Text) == "" {
		return nil
	}
	text := s.held + s.joint(c.Text) + c.Text
	s.started, s.inRun = true, c.InRun
	cut := s.t.heldFrom(text)
	if strings.TrimSpace(text[:cut]) == "" {
		s.held = text
		return nil
	}
	s.held = text[cut:]
	return []string{s.t.rewriteText(text[:cut])}
}

// Flush returns the held tail, rewritten, and starts the stream afresh.
func (s *Stream) Flush() []string {
	held := s.held
	s.Reset()
	if strings.TrimSpace(held) == "" {
		return nil
	}
	return []string{s.t.rewriteText(held)}
}

// Reset drops the held word and starts the stream afresh (a discard, M3-R9).
func (s *Stream) Reset() { s.held, s.inRun, s.started = "", false, false }

// the chunker trims the whitespace it cuts at, so a chunk gets one space back before it; none at the start, where
// the chunker cut a run without whitespace, or where a raise reads, or may yet read, across the join: the two then
// are one word to rewrite (fail-closed for rule 7). another stem across a trimmed space stays two words, as the text
// had them (просто | попробуй)
func (s *Stream) joint(chunk string) string {
	if first, _ := utf8.DecodeRuneInString(chunk); !s.started || s.inRun || unicode.IsSpace(first) {
		return ""
	}
	if s.held != "" && s.t.readsAcross(s.held, chunk) {
		return ""
	}
	return " "
}

// heldFrom is where text's trailing word, which waits, begins, or the end when none waits
func (t *tables) heldFrom(text string) int {
	cut := len(text)
	for cut > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:cut])
		if !wordish(r) {
			break
		}
		cut -= n
	}
	if cut == len(text) || (!t.waitAll && !waitsInEnglish(text[cut:])) {
		return len(text)
	}
	return cut
}

// waitsInEnglish: a word with a cyrillic letter, or one whose end may begin a raise read from latin
func waitsInEnglish(word string) bool {
	for _, r := range word {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	heard := lastRunes(heardIn(word), span)
	for start := range heard {
		if _, open := raise.read(heard, start); open {
			return true
		}
	}
	return false
}

// readsAcross reports whether a raise read from the held word's letters goes on into the chunk's first word, or may
// go on later while that word runs to the chunk's end
func (t *tables) readsAcross(held, chunk string) bool {
	head := 0
	for head < len(chunk) {
		r, n := utf8.DecodeRuneInString(chunk[head:])
		if !wordish(r) {
			break
		}
		head += n
	}
	before := lastRunes(heardIn(held), span)
	heard := append(append([]rune(nil), before...), heardIn(chunk[:head])...)
	goesOn := head == len(chunk)
	for start := range before {
		ends, open := raise.read(heard, start)
		if open && goesOn {
			return true
		}
		for _, e := range ends {
			if e > len(before) {
				return true
			}
		}
	}
	return false
}
