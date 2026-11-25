package ha

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize makes Home Assistant text safe to put in a prompt or speak: control
// and format characters and double quotes become spaces, whitespace runs
// collapse to one space, the ends are trimmed, and the rest is cut to n code
// points.
func Sanitize(s string, n int) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == '"' || unicode.IsSpace(r) || !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Zs) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	for i := range out {
		if n <= 0 {
			return out[:i]
		}
		n--
	}
	return out
}

const (
	maxModelState = 64
	// maxPlainSensor is the longest non-numeric sensor state shown as it is
	maxPlainSensor = 32
	textState      = "(text)"
)

// freeTextDomains hold states anyone can write (a note, a todo item, a
// third-party feed), so the model never sees them.
var freeTextDomains = map[string]bool{
	"input_text": true, "text": true, "todo": true, "calendar": true, "event": true, "notify": true,
	"update": true, "weather": true, "conversation": true, "image": true, "tts": true, "stt": true,
}

// ModelState is e's state as the model may see it: (text) for a free-text
// domain and for a non-numeric sensor state longer than 32 characters, else
// sanitized to 64.
func ModelState(e Entity) string {
	if freeTextDomains[e.Domain] {
		return textState
	}
	if e.Domain == "sensor" && utf8.RuneCountInString(e.State) > maxPlainSensor {
		if _, err := strconv.ParseFloat(e.State, 64); err != nil {
			return textState
		}
	}
	return Sanitize(e.State, maxModelState)
}
