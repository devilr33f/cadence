package media

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var errNotObject = errors.New("media: not one json object")

// decodeObject decodes one JSON object into v and refuses unknown fields,
// wrong types and anything after it. The fields of v are pointers where
// presence matters.
func decodeObject(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errNotObject
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errNotObject
	}
	return nil
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// plain structs of strings, numbers and bools cannot fail to marshal
		panic(err)
	}
	return b
}

// webURL reports whether u is an http or https url (any case, as audiod
// takes it) with something after the scheme, within audiod's bound.
func webURL(u string) bool {
	if len(u) > maxURLBytes {
		return false
	}
	for _, scheme := range []string{"http://", "https://"} {
		if len(u) > len(scheme) && strings.EqualFold(u[:len(scheme)], scheme) {
			return true
		}
	}
	return false
}

// capText cuts s to at most n bytes before a character that does not fit:
// audiod drops a client whose frame has a longer field.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// publishLatest puts a state in a channel of one, replacing an unread one.
func publishLatest(ch chan json.RawMessage, v any) { offer(ch, json.RawMessage(encode(v))) }
