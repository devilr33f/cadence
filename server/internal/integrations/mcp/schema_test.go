package mcp_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
)

func props(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"p%d":{"type":"string"}`, i)
	}
	return `{"type":"object","properties":{` + b.String() + `}}`
}

// nestedProps is properties nested levels deep: the root, then one property
// in each, a string last
func nestedProps(levels int) string {
	s := `{"type":"string"}`
	for range levels - 1 {
		s = `{"type":"object","properties":{"a":` + s + `}}`
	}
	return s
}

// arrays is n arrays one in another
func arrays(n int) string { return strings.Repeat("[", n) + `"x"` + strings.Repeat("]", n) }

// sized pads an object schema to n bytes with a description
func sized(n int) string {
	head := `{"type":"object","description":"`
	return head + strings.Repeat("d", n-len(head)-2) + `"}`
}

func TestSchemaCaps(t *testing.T) {
	for name, tc := range map[string]struct{ schema, skip string }{
		"plain":             {`{"type":"object","properties":{"text":{"type":"string"}}}`, ""},
		"8 KiB":             {sized(8 << 10), ""},
		"over 8 KiB":        {sized(8<<10 + 1), mcp.SkipSchemaSize},
		"properties 8 deep": {nestedProps(8), ""},
		"properties 9 deep": {nestedProps(9), mcp.SkipSchemaDepth},
		"arrays 8 deep":     {`{"type":"object","enum":` + arrays(7) + `}`, ""},
		"arrays 9 deep":     {`{"type":"object","enum":` + arrays(8) + `}`, mcp.SkipSchemaDepth},
		"a branch":          {`{"type":"object","properties":{"a":{"type":"array","items":{"anyOf":[{"type":"object","properties":{"b":{"enum":["x"]}}}]}}}}`, ""},
		"data 9 deep":       {`{"type":"object","default":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{}}}}}}}}}`, mcp.SkipSchemaDepth},
		"64 properties":     {props(64), ""},
		"65 properties":     {props(65), mcp.SkipSchemaProperties},
		"string type":       {`{"type":"string"}`, mcp.SkipSchemaType},
		"no type":           {`{"properties":{}}`, mcp.SkipSchemaType},
		"type list":         {`{"type":["object","null"]}`, mcp.SkipSchemaType},
		"array":             {`[]`, mcp.SkipSchemaType},
		"true":              {`true`, mcp.SkipSchemaType},
		"local ref":         {`{"type":"object","$defs":{"s":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/s"}}}`, ""},
		"remote ref":        {`{"type":"object","properties":{"a":{"$ref":"https://schemas.example/s.json"}}}`, mcp.SkipSchemaRef},
		"relative ref":      {`{"type":"object","properties":{"a":{"$ref":"other.json#/s"}}}`, mcp.SkipSchemaRef},
		"a property $ref":   {`{"type":"object","properties":{"$ref":{"type":"string"}}}`, ""},
	} {
		if got := mcp.CheckSchema(json.RawMessage(tc.schema)); got != tc.skip {
			t.Errorf("%s: %q, want %q", name, got, tc.skip)
		}
	}
}

func TestDescriptionIsSanitized(t *testing.T) {
	// a zero-width space and a right-to-left override with its pop
	zw, rlo, pdf := string(rune(0x200b)), string(rune(0x202e)), string(rune(0x202c))
	for in, want := range map[string]string{
		"Searches the web.":                            "[MCP gh] Searches the web.",
		"  Two\n\tlines\r\n  here ":                    "[MCP gh] Two lines here",
		"bell\x07 and nul\x00 and " + zw + "zw":        "[MCP gh] bell and nul and zw",
		"rtl " + rlo + "override" + pdf + " and \x1b[": "[MCP gh] rtl override and [",
		"": "[MCP gh]",
	} {
		if got := mcp.Describe("gh", in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	got := mcp.Describe("gh", strings.Repeat("é", 700))
	if utf8.RuneCountInString(got) != 600 || !strings.HasPrefix(got, "[MCP gh] éé") || !utf8.ValidString(got) {
		t.Fatalf("capped to %d code points: %q…", utf8.RuneCountInString(got), got[:20])
	}
}

func TestToolHashCoversWhatTheOwnerSaw(t *testing.T) {
	yes, endpoint := true, "https://mcp.example/mcp"
	base := mcp.RemoteTool{Name: "t", Description: "d", InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)}
	h := mcp.ToolHash(endpoint, base)
	same := base
	same.Name = "renamed"
	same.InputSchema = json.RawMessage(`{"properties": {"a": {"type": "string"}}, "type": "object"}`)
	if mcp.ToolHash(endpoint, same) != h {
		t.Fatal("key order, spacing or the name moved the hash")
	}
	for name, change := range map[string]func(*mcp.RemoteTool){
		"description": func(r *mcp.RemoteTool) { r.Description = "d2" },
		"schema":      func(r *mcp.RemoteTool) { r.InputSchema = json.RawMessage(`{"type":"object"}`) },
		"annotations": func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{ReadOnlyHint: true} },
		"a hint":      func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{DestructiveHint: &yes} },
		"no hints":    func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{} },
		"idempotent":  func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{IdempotentHint: true} },
		"open world":  func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{OpenWorldHint: &yes} },
		"a title":     func(r *mcp.RemoteTool) { r.Annotations = &sdk.ToolAnnotations{Title: "T"} },
	} {
		r := base
		change(&r)
		if mcp.ToolHash(endpoint, r) == h {
			t.Errorf("%s left the hash", name)
		}
	}
	if mcp.ToolHash("https://other.example/mcp", base) == h {
		t.Error("another endpoint left the hash")
	}
}

// hashChild makes the test binary the child of
// TestToolHashIgnoresTheSDKsEncoding: no database, its hashes printed
const hashChild = "CADENCE_MCP_HASH_CHILD"

// the hash takes the hints' values, never the SDK's JSON of them, which
// MCPGODEBUG=hintomitempty=1 changes (false hints omitted): a child process
// with it set hashes alike
func TestToolHashIgnoresTheSDKsEncoding(t *testing.T) {
	yes, no := true, false
	var lines []string
	for _, a := range []*sdk.ToolAnnotations{
		nil, {}, {ReadOnlyHint: true}, {DestructiveHint: &no, Title: "T"}, {IdempotentHint: true, OpenWorldHint: &yes},
	} {
		tool := mcp.RemoteTool{Name: "t", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: a}
		lines = append(lines, "hash="+mcp.ToolHash("https://mcp.example/mcp", tool))
	}
	sdkJSON, err := json.Marshal(sdk.ToolAnnotations{})
	if err != nil {
		t.Fatal(err)
	}
	lines = append(lines, "sdk="+string(sdkJSON))
	if os.Getenv(hashChild) != "" {
		fmt.Println(strings.Join(lines, "\n"))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolHashIgnoresTheSDKsEncoding$", "-test.count=1")
	cmd.Env = append(os.Environ(), hashChild+"=1", "MCPGODEBUG=hintomitempty=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var child []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "hash=") || strings.HasPrefix(l, "sdk=") {
			child = append(child, l)
		}
	}
	if len(child) != len(lines) || child[len(child)-1] != "sdk={}" || lines[len(lines)-1] == "sdk={}" {
		t.Fatalf("the child's SDK encodes alike, the test proves nothing:\n%s", out)
	}
	for i := range len(lines) - 1 {
		if child[i] != lines[i] {
			t.Errorf("annotations %d: %s here, %s with hintomitempty", i, lines[i], child[i])
		}
	}
}

// the model reads the JSON Schema vocabulary alone: comments, extensions and
// unknown keywords go, data and the names in name maps stay as written
func TestSchemaTextKeepsTheVocabularyOnly(t *testing.T) {
	raw := `{"type":"object","$comment":"secret-comment","x-internal":{"a":1},"foo":"bar",` +
		`"$defs":{"s":{"type":"string","x-s":1,"format":"email"}},` +
		`"properties":{"$comment":{"type":"string","$comment":"c"},"x-name":{"$ref":"#/$defs/s","x-ref":true},` +
		`"n":{"type":"integer","minimum":1,"exclusiveMaximum":9,"multipleOf":1,"deprecated":true,"readOnly":false}},` +
		`"dependentRequired":{"examples":["n"],"x-y":["n"]},` +
		`"dependencies":{"examples":{"properties":{"z":{"type":"string","examples":["e"],"x-z":1}}},"b":["examples"]},` +
		`"definitions":{"d":{"type":"null","unknownKw":1}},` +
		`"patternProperties":{"^p_":{"type":"string","x-p":1}},` +
		`"if":{"required":["n"]},"then":{"minProperties":1},"else":{"maxProperties":9},` +
		`"allOf":[{"not":{"required":["q"],"x-not":1}}],"unevaluatedProperties":false,` +
		`"prefixItems":[{"contentMediaType":"text/plain","contentEncoding":"base64","contentSchema":{"type":"string","x-c":1}}]}`
	want := `{"type":"object",` +
		`"$defs":{"s":{"type":"string","format":"email"}},` +
		`"properties":{"$comment":{"type":"string"},"x-name":{"$ref":"#/$defs/s"},` +
		`"n":{"type":"integer","minimum":1,"exclusiveMaximum":9,"multipleOf":1,"deprecated":true,"readOnly":false}},` +
		`"dependentRequired":{"examples":["n"],"x-y":["n"]},` +
		`"dependencies":{"examples":{"properties":{"z":{"type":"string"}}},"b":["examples"]},` +
		`"definitions":{"d":{"type":"null"}},` +
		`"patternProperties":{"^p_":{"type":"string"}},` +
		`"if":{"required":["n"]},"then":{"minProperties":1},"else":{"maxProperties":9},` +
		`"allOf":[{"not":{"required":["q"]}}],"unevaluatedProperties":false,` +
		`"prefixItems":[{"contentMediaType":"text/plain","contentEncoding":"base64","contentSchema":{"type":"string"}}]}`
	got, err := mcp.SchemaText(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(want), &w) != nil || !reflect.DeepEqual(g, w) {
		t.Fatalf("the model reads %s", got)
	}
}

// a pattern is at most 512 bytes; a longer one leaves the tool out
func TestSchemaPatternsAreCapped(t *testing.T) {
	at := strings.Repeat("a", 512)
	for name, tc := range map[string]struct {
		schema string
		ok     bool
	}{
		"pattern 512":              {`{"type":"object","properties":{"a":{"type":"string","pattern":"` + at + `"}}}`, true},
		"pattern 513":              {`{"type":"object","properties":{"a":{"type":"string","pattern":"` + at + `b"}}}`, false},
		"patternProperties 512":    {`{"type":"object","patternProperties":{"` + at + `":{}}}`, true},
		"patternProperties 513":    {`{"type":"object","patternProperties":{"` + at + `b":{}}}`, false},
		"a property named pattern": {`{"type":"object","properties":{"pattern":{"type":"string"}}}`, true},
		"pattern data":             {`{"type":"object","default":{"pattern":"` + at + `b"}}`, true},
	} {
		if _, err := mcp.SchemaText(json.RawMessage(tc.schema)); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
