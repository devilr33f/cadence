package mcp

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// omitted is the line a result's text gains when parts of it were dropped.
const omitted = "[non-text content omitted]"

// render is a call's result as the tool's output, the server's untrusted
// data: its structuredContent as {"result": …}, else its text and embedded
// resource text joined as {"text": …}, with image, audio, blob and link parts
// dropped and noted; {"ok": true} when nothing is left. What can hide or
// reorder text is stripped (strip). Over limit code points, as
// the model reads it, a Read tool's output is cut to fit and marked
// truncated; a Write tool's fails mcp_too_large, since its effect happened
// and a part of its result could be mistaken for all of it (Ruling M5d-R4).
// structuredContent arrives as float64: one with a whole number of 2^53 or
// more may have lost digits, so the text the server should mirror it in is
// used instead, or, with none, the structured form says precisionLost.
func render(res *Result, limit int, effects agent.Effect) (json.RawMessage, string) {
	var out json.RawMessage
	var text string
	structured, lost := res.Structured != nil, false
	if structured && lostPrecision(res.Structured) {
		// only the note is no text
		if t := joined(res.Content); t != "" && t != omitted {
			structured = false
		} else {
			lost = true
		}
	}
	if structured {
		v := clean(res.Structured)
		if lost {
			out = marshal(map[string]any{"result": v, "precisionLost": true})
		} else {
			out = marshal(map[string]any{"result": v})
		}
		text = plain(v)
	} else {
		if text = joined(res.Content); text == "" {
			return json.RawMessage(`{"ok":true}`), ""
		}
		out = marshal(map[string]string{"text": text})
	}
	switch {
	case out == nil:
		return nil, CodeProtocol
	case utf8.RuneCount(out) <= limit:
		return out, ""
	case effects != agent.Read:
		return nil, CodeTooLarge
	}
	return cut(text, limit, lost), ""
}

// exact is 2^53: from here on a float64 no longer holds every whole number
const exact = 1 << 53

// lostPrecision is whether a number in v may not be the one the server sent.
func lostPrecision(v any) bool {
	switch v := v.(type) {
	case float64:
		return math.Abs(v) >= exact && v == math.Trunc(v)
	case []any:
		return slices.ContainsFunc(v, lostPrecision)
	case map[string]any:
		for _, x := range v {
			if lostPrecision(x) {
				return true
			}
		}
	}
	return false
}

// joined is the text of the content parts, one part a line, and the note
// when a part had none.
func joined(content []sdk.Content) string {
	var lines []string
	dropped := false
	for _, c := range content {
		switch c := c.(type) {
		case *sdk.TextContent:
			lines = appendText(lines, c.Text)
		case *sdk.EmbeddedResource:
			if c.Resource == nil || len(c.Resource.Blob) > 0 {
				dropped = true
			}
			if c.Resource != nil {
				lines = appendText(lines, c.Resource.Text)
			}
		default:
			dropped = true
		}
	}
	if dropped {
		lines = append(lines, omitted)
	}
	return strings.Join(lines, "\n")
}

func appendText(lines []string, s string) []string {
	if s = strip(s); s != "" {
		return append(lines, s)
	}
	return lines
}

// strip drops what can hide or reorder text: the control characters but
// \n and \t (C0, DEL, C1), the bidi controls, the tag characters and U+FEFF.
// The joiners stay: emoji sequences and scripts need them.
func strip(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), r >= 0xE0000 && r <= 0xE007F, r == 0xFEFF:
			return -1
		}
		return r
	}, s)
}

// clean strips every string of a structured result, keys too; of keys that
// strip alike, the first in order counts.
func clean(v any) any {
	switch v := v.(type) {
	case string:
		return strip(v)
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = clean(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if sk := strip(k); !hasKey(out, sk) {
				out[sk] = clean(v[k])
			}
		}
		return out
	}
	return v
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// marshal encodes v as the agent passes it on to the model, so the cap
// counts what the model reads; nil when v does not encode.
func marshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// plain is v as JSON text, without the escapes a string of it gets again.
func plain(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

type truncated struct {
	Text          string `json:"text"`
	Truncated     bool   `json:"truncated"`
	PrecisionLost bool   `json:"precisionLost,omitempty"`
}

// cut is the longest head of text, cut on a code point, whose output is
// within limit code points; lost marks a structured result whose numbers
// may have lost digits.
func cut(text string, limit int, lost bool) json.RawMessage {
	// ends[k] is where the head of k code points ends; each takes at least
	// one code point of the output, so no head past limit fits
	ends := []int{0}
	for i := range text {
		if len(ends) > limit {
			break
		}
		if i > 0 {
			ends = append(ends, i)
		}
	}
	if len(ends) <= limit {
		ends = append(ends, len(text))
	}
	fits := func(end int) bool { return utf8.RuneCount(marshal(truncated{text[:end], true, lost})) <= limit }
	n := sort.Search(len(ends), func(i int) bool { return !fits(ends[i]) })
	return marshal(truncated{text[:ends[max(n-1, 0)]], true, lost})
}
