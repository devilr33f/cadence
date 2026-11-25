package tracing

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
)

// The privacy guard (rule 8) is a safety net under the instrumentation, not
// its licence: callers put ids, codes, numbers, sizes and timings on spans
// and nothing else. The guard drops attribute keys that name a URL with its
// query, a credential or a text, cuts every string, and keeps an error's
// status only when it is a code. It runs on every span the store keeps and
// every span the export sends, local or a device's.
const (
	// maxValue is the longest string attribute value kept, in bytes
	maxValue = 256
	// maxName is the longest span or event name kept
	maxName = 128
	// maxSlice is the most elements of a slice attribute kept
	maxSlice = 32
)

// deniedKeys are dropped whole: each carries a URL with its query, or a
// statement with its values.
var deniedKeys = map[string]bool{
	"url.full": true, "http.url": true, "url.query": true, "http.target": true, "url.original": true,
	"db.statement": true, "db.query.text": true, "exception.stacktrace": true,
}

// deniedParts drop any key holding one: a credential, or text a person said
// or a model wrote. exception.message is one (an error's text can carry a
// URL or a key), which is why End records codes.
var deniedParts = []string{
	"query", "token", "secret", "password", "passwd", "authorization", "cookie", "api_key", "apikey", "credential",
	"prompt", "completion", "transcript", "message", "content", "body.text", "lyrics", "title", "artist", "header.",
}

// statusCodeRe is what a status description may be: a code, never a text.
var statusCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,63}$`)

// keyAllowed reports whether the guard keeps an attribute of key.
func keyAllowed(key string) bool {
	k := strings.ToLower(key)
	if k == "" || deniedKeys[k] {
		return false
	}
	for _, p := range deniedParts {
		if strings.Contains(k, p) {
			return false
		}
	}
	return true
}

// cut keeps s up to n bytes, on a rune boundary, valid UTF-8.
func cut(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// statusCode is the description kept for an error status: a code as it is,
// anything else "error".
func statusCode(desc string) string {
	if statusCodeRe.MatchString(desc) {
		return desc
	}
	return "error"
}

// guardAttrs is the guarded copy of kvs, as the JSON views carry them.
func guardAttrs(kvs []attribute.KeyValue) map[string]any {
	var m map[string]any
	for _, kv := range kvs {
		k := string(kv.Key)
		if !keyAllowed(k) {
			continue
		}
		v, ok := guardValue(kv.Value)
		if !ok {
			continue
		}
		if m == nil {
			m = make(map[string]any, len(kvs))
		}
		m[k] = v
	}
	return m
}

// guardKVs is kvs with the guard applied, for the export.
func guardKVs(kvs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		if !keyAllowed(string(kv.Key)) {
			continue
		}
		switch kv.Value.Type() {
		case attribute.STRING:
			kv.Value = attribute.StringValue(cut(kv.Value.AsString(), maxValue))
		case attribute.STRINGSLICE:
			kv.Value = attribute.StringSliceValue(cutSlice(kv.Value.AsStringSlice()))
		case attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE:
			if sliceLen(kv.Value) > maxSlice {
				continue
			}
		case attribute.EMPTY:
			continue
		}
		out = append(out, kv)
	}
	return out
}

// guardValue is v as a JSON value, strings cut; false drops it.
func guardValue(v attribute.Value) (any, bool) {
	switch v.Type() {
	case attribute.STRING:
		return cut(v.AsString(), maxValue), true
	case attribute.STRINGSLICE:
		return cutSlice(v.AsStringSlice()), true
	case attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE:
		if sliceLen(v) > maxSlice {
			return nil, false
		}
		return v.AsInterface(), true
	case attribute.EMPTY:
		return nil, false
	}
	return v.AsInterface(), true
}

func cutSlice(s []string) []string {
	if len(s) > maxSlice {
		s = s[:maxSlice]
	}
	out := make([]string, len(s))
	for i, e := range s {
		out[i] = cut(e, maxValue)
	}
	return out
}

func sliceLen(v attribute.Value) int {
	switch v.Type() {
	case attribute.BOOLSLICE:
		return len(v.AsBoolSlice())
	case attribute.INT64SLICE:
		return len(v.AsInt64Slice())
	case attribute.FLOAT64SLICE:
		return len(v.AsFloat64Slice())
	case attribute.STRINGSLICE:
		return len(v.AsStringSlice())
	}
	return 0
}
