package bluetooth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	maxName       = 32
	defaultWindow = 120
	minWindow     = 30
	maxWindow     = 300

	configSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"},` +
		`"name":{"type":"string","maxLength":32},` +
		`"receive":{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false},` +
		`"pairWindowSeconds":{"type":"integer","minimum":30,"maximum":300}},"additionalProperties":false}`

	deviceSchema = `{"type":"object","properties":{` +
		`"id":{"type":"string","pattern":"^[0-9a-f]{12}$"},"name":{"type":"string"},` +
		`"kind":{"enum":["phone","computer","speaker","headphones","other"]},` +
		`"paired":{"type":"boolean"},"connected":{"type":"boolean"},"playing":{"type":"boolean"}},` +
		`"required":["id","name","kind","paired","connected","playing"],"additionalProperties":false}`

	stateSchema = `{"type":"object","properties":{` +
		`"adapter":{"type":"object","properties":{"present":{"type":"boolean"},"powered":{"type":"boolean"},` +
		`"pairable":{"type":"boolean"},"discoverable":{"type":"boolean"}},` +
		`"required":["present","powered","pairable","discoverable"],"additionalProperties":false},` +
		`"window":{"type":"object","properties":{"open":{"type":"boolean"},"remainingSeconds":{"type":"integer","minimum":0}},` +
		`"required":["open","remainingSeconds"],"additionalProperties":false},` +
		`"devices":{"type":"array","items":` + deviceSchema + `},` +
		`"code":{"type":"string","pattern":"^([a-z0-9_]{1,64})?$"}},` +
		`"required":["adapter","window","devices","code"],"additionalProperties":false}`
)

// Config is the capability's.
type Config struct {
	Enabled bool
	// "" for the device's name
	Name              string
	Receive           bool
	PairWindowSeconds int
}

// DefaultConfig is what an empty config means.
func DefaultConfig() Config {
	return Config{Enabled: true, Receive: true, PairWindowSeconds: defaultWindow}
}

func parseConfig(raw json.RawMessage) (Config, bool) {
	var c struct {
		Enabled           json.RawMessage `json:"enabled"`
		Name              json.RawMessage `json:"name"`
		Receive           json.RawMessage `json:"receive"`
		PairWindowSeconds json.RawMessage `json:"pairWindowSeconds"`
	}
	if decodeObject(raw, &c) != nil {
		return Config{}, false
	}
	out := DefaultConfig()
	if !field(c.Enabled, &out.Enabled) || !field(c.Name, &out.Name) || !field(c.PairWindowSeconds, &out.PairWindowSeconds) {
		return Config{}, false
	}
	if !utf8.ValidString(out.Name) || utf8.RuneCountInString(out.Name) > maxName ||
		out.PairWindowSeconds < minWindow || out.PairWindowSeconds > maxWindow {
		return Config{}, false
	}
	if c.Receive != nil {
		var r struct {
			Enabled json.RawMessage `json:"enabled"`
		}
		if decodeObject(c.Receive, &r) != nil || !field(r.Enabled, &out.Receive) {
			return Config{}, false
		}
	}
	return out, true
}

// decodeObject decodes one JSON object into v and refuses unknown fields, wrong types and anything after it
func decodeObject(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalid
	}
	return nil
}

// an explicit null is no value of its type
func field(raw json.RawMessage, v any) bool {
	return raw == nil || !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, v) == nil
}
