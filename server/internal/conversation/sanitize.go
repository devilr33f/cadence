package conversation

import (
	"bytes"
	"encoding/json"
	"strings"
)

// postgres text and jsonb reject NUL, which would lose the whole turn
func clean(s string) string { return strings.ReplaceAll(s, "\x00", "\uFFFD") }

// cleanJSON leaves arguments byte-exact unless they hold a NUL; only then are
// they decoded (numbers kept as written), sanitized and re-encoded.
func cleanJSON(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte{0}) && !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return json.RawMessage(clean(string(raw)))
	}
	b, err := json.Marshal(cleanValue(v))
	if err != nil {
		return json.RawMessage(clean(string(raw)))
	}
	return b
}

func cleanValue(v any) any {
	switch x := v.(type) {
	case string:
		return clean(x)
	case []any:
		for i := range x {
			x[i] = cleanValue(x[i])
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[clean(k)] = cleanValue(e)
		}
		return out
	}
	return v
}
