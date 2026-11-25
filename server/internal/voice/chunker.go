package voice

import (
	"regexp"
	"strings"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

const maxChunkRunes = 200

// chunker regroups streamed text for TTS: a cut after `.`, `!`, `?` or `…`
// followed by a space, else at maxChunkRunes on a space, else inside the run
// (the chunk says so: the guard joins the next one without a space); what is
// left at the end is the last chunk. Chunks are trimmed; one without a letter
// or digit is dropped.
type chunker struct{ buf []rune }

// push adds text and returns the chunks it completed.
func (c *chunker) push(s string) []cmdguard.Chunk {
	c.buf = append(c.buf, []rune(s)...)
	var out []cmdguard.Chunk
	for n, inRun := cut(c.buf); n > 0; n, inRun = cut(c.buf) {
		out = appendChunk(out, c.buf[:n], inRun)
		c.buf = c.buf[n:]
	}
	return out
}

// rest returns what is left as the last chunk.
func (c *chunker) rest() []cmdguard.Chunk {
	out := appendChunk(nil, c.buf, false)
	c.buf = nil
	return out
}

// reset drops the unfinished chunk.
func (c *chunker) reset() { c.buf = nil }

// cut is the length of the next complete chunk in buf, 0 while it may grow,
// and whether it ends inside a run without whitespace.
func cut(buf []rune) (int, bool) {
	for i := 0; i+1 < len(buf) && i < maxChunkRunes; i++ {
		switch buf[i] {
		case '.', '!', '?', '…':
			if unicode.IsSpace(buf[i+1]) {
				return i + 1, false
			}
		}
	}
	if len(buf) <= maxChunkRunes {
		return 0, false
	}
	for i := maxChunkRunes; i > 0; i-- {
		if unicode.IsSpace(buf[i]) {
			return i, false
		}
	}
	return maxChunkRunes, true
}

// cites are the [n] marks a reply puts after a fact it took from the n-th
// web search result (citations, shown on screens): never spoken
var cites = regexp.MustCompile(`[ \t]*\[\d{1,2}(?:\s*,\s*\d{1,2})*\]`)

func appendChunk(out []cmdguard.Chunk, chunk []rune, inRun bool) []cmdguard.Chunk {
	s := strings.TrimSpace(cites.ReplaceAllString(string(chunk), ""))
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
		out = append(out, cmdguard.Chunk{Text: s, InRun: inRun})
	}
	return out
}
