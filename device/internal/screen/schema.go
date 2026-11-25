package screen

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schema checks a config or args against the JSON Schema the capability reports, strictly: the server's check is not
// a guarantee
type schema struct{ s *jsonschema.Schema }

// mustSchema compiles one of pkg/capschema's schemas, which its own tests compile
func mustSchema(src string) schema {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		panic("screen: schema: " + err.Error())
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("s.json", doc); err != nil {
		panic("screen: schema: " + err.Error())
	}
	s, err := c.Compile("s.json")
	if err != nil {
		panic("screen: schema: " + err.Error())
	}
	return schema{s}
}

// valid decodes raw into v when it is one JSON value the schema takes; an empty raw is an empty object
func (s schema) valid(raw []byte, v any) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || s.s.Validate(doc) != nil {
		return false
	}
	if v == nil {
		return true
	}
	return json.Unmarshal(raw, v) == nil
}

func encode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("screen: encode: " + err.Error())
	}
	return b
}

// offer puts v in a channel of one that keeps the latest value; it never blocks
func offer(ch chan json.RawMessage, v json.RawMessage) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
