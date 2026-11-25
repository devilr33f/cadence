package jsonschemax_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

const volume = `{
	"type": "object",
	"properties": {
		"level": {"type": "integer", "minimum": 0, "maximum": 100},
		"unit": {"enum": ["percent", "step"]}
	},
	"required": ["level"],
	"additionalProperties": false
}`

func TestCheckReportsReason(t *testing.T) {
	sch, err := jsonschemax.Compile(volume)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		raw, path string
		reason    []string
	}{
		{`{"level":150}`, "/level", []string{"maximum", "100"}},
		{`{}`, "", []string{"missing property", "level"}},
		{`{"level":5,"unit":"db"}`, "/unit", []string{"percent", "step"}},
		{`{"level":"loud"}`, "/level", []string{"want integer"}},
		{`{"level":5,"extra":1}`, "", []string{"extra", "not allowed"}},
		{`{"level":`, "", []string{"not valid JSON"}},
	}
	for _, c := range cases {
		f, ok := jsonschemax.Check(sch, []byte(c.raw))
		if ok || f.Path != c.path {
			t.Fatalf("%s: got %+v ok=%v, want path %q", c.raw, f, ok, c.path)
		}
		for _, w := range c.reason {
			if !strings.Contains(f.Reason, w) {
				t.Fatalf("%s: reason %q lacks %q", c.raw, f.Reason, w)
			}
		}
		if path, ok := jsonschemax.Validate(sch, []byte(c.raw)); ok || path != c.path {
			t.Fatalf("%s: Validate got %q ok=%v", c.raw, path, ok)
		}
	}
	if f, ok := jsonschemax.Check(sch, []byte(`{"level":40}`)); !ok || f != (jsonschemax.Failure{}) {
		t.Fatalf("valid: got %+v ok=%v", f, ok)
	}
	if _, ok := jsonschemax.Check(nil, []byte(`"anything"`)); !ok {
		t.Fatal("a nil schema refused valid JSON")
	}
}

// the model reads these reasons: no keyword jargon for the combinators, no digit grouping
func TestCheckReasonIsPlain(t *testing.T) {
	cases := []struct {
		schema, raw string
		want        []string
		not         []string
	}{
		{`{"type":"integer","maximum":1000}`, `31337`, []string{"got 31337, want 1000"}, []string{"31,337", "1,000"}},
		{`{"type":"number","minimum":1500.5}`, `1000`, []string{"got 1000, want 1500.5"}, []string{"1,000", "1,500"}},
		{`{"type":"string","maxLength":1000}`, `"` + strings.Repeat("a", 1200) + `"`, []string{"got 1200, want 1000"}, []string{"1,200", "1,000"}},
		{`{"oneOf":[{"type":"integer"},{"type":"number"}]}`, `5`, []string{"more than one of the allowed forms"}, []string{"oneOf", "subschemas"}},
		{`{"anyOf":[{"type":"string"},{"type":"boolean"}]}`, `5`, []string{"matches none of the allowed forms; the first fails: ", "want string"}, []string{"anyOf"}},
		{`{"oneOf":[{"required":["level"]},{"required":["change"]}]}`, `{}`, []string{"matches none of the allowed forms; the first fails: missing property 'level'"}, []string{"oneOf"}},
		{`{"properties":{"a":{"anyOf":[{"maximum":1000},{"type":"string"}]}}}`, `{"a":2000}`, []string{"matches none of the allowed forms; the first fails: maximum: got 2000, want 1000"}, []string{"2,000"}},
		{`{"anyOf":[{"properties":{"level":{"maximum":100}}},{"type":"string"}]}`, `{"level":150}`, []string{"matches none of the allowed forms; the first fails at /level: maximum: got 150, want 100"}, nil},
	}
	for _, c := range cases {
		sch, err := jsonschemax.Compile(c.schema)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := jsonschemax.Check(sch, []byte(c.raw))
		if ok {
			t.Fatalf("%s accepted %s", c.schema, c.raw)
		}
		for _, w := range c.want {
			if !strings.Contains(f.Reason, w) {
				t.Fatalf("%s: reason %q lacks %q", c.schema, f.Reason, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(f.Reason, n) {
				t.Fatalf("%s: reason %q has %q", c.schema, f.Reason, n)
			}
		}
	}
}
