package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
)

// the part of the cap {"text":""} takes
var textFrame = len(`{"text":""}`)

func TestResultsReachTheModelAsData(t *testing.T) {
	const limit = 256
	r := newTools(t, mcptest.Options{}, withMaxResult(limit))
	add := func(name string, res *sdk.CallToolResult) {
		r.f.AddResult(&sdk.Tool{Name: name, Description: "R.", InputSchema: schema(anyObject)}, res)
	}
	parts := func(c ...sdk.Content) *sdk.CallToolResult { return &sdk.CallToolResult{Content: c} }
	image := &sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"}
	add("structured", &sdk.CallToolResult{
		StructuredContent: map[string]any{"temp": 21.5, "note": "a\u0007b\nc", "k\u0001": []any{"x\u001by"}},
		Content:           []sdk.Content{&sdk.TextContent{Text: "the same as text"}},
	})
	add("joined", parts(&sdk.TextContent{Text: "one"}, &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///a", Text: "two"}}))
	add("mixed", parts(
		&sdk.TextContent{Text: "one"}, image, &sdk.AudioContent{Data: []byte{1}, MIMEType: "audio/wav"},
		&sdk.ResourceLink{URI: "file:///b", Name: "b"}, &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///c", Blob: []byte{1, 2}}},
	))
	add("nothing", parts())
	add("controls", parts(&sdk.TextContent{Text: "a\x00b\x1bc\td\re\nf"}))
	add("exact", parts(&sdk.TextContent{Text: strings.Repeat("x", limit-textFrame)}))
	add("over", parts(&sdk.TextContent{Text: strings.Repeat("x", limit-textFrame+1)}))
	long := strings.Repeat(`日本"`, 200)
	add("long", parts(&sdk.TextContent{Text: long}))
	add("deep", &sdk.CallToolResult{StructuredContent: map[string]any{"items": strings.Repeat("y", 400)}})
	r.start("deep")
	r.set("long", asRead)
	r.set("deep", asRead)
	for _, c := range []struct{ tool, want string }{
		{"structured", `{"result":{"k":["xy"],"note":"ab\nc","temp":21.5}}`},
		{"joined", `{"text":"one\ntwo"}`},
		{"mixed", `{"text":"one\n[non-text content omitted]"}`},
		{"image", `{"text":"[non-text content omitted]"}`},
		{"nothing", `{"ok":true}`},
		{"controls", `{"text":"abc\tde\nf"}`},
		{"exact", `{"text":"` + strings.Repeat("x", limit-textFrame) + `"}`},
	} {
		out, e := r.call(r.tool("mcp_srv_"+c.tool), `{}`)
		if e != nil || string(out) != c.want {
			t.Errorf("%s: %s %+v, want %s", c.tool, out, e, c.want)
		}
	}
	_, e := r.call(r.tool("mcp_srv_fail"), `{}`)
	failed(t, e, mcp.CodeToolError, false)
	// a Write tool's side effect happened: its result is not cut short but refused
	_, e = r.call(r.tool("mcp_srv_over"), `{}`)
	failed(t, e, mcp.CodeTooLarge, false)
	if !strings.Contains(e.Message, "ran") || r.f.Calls("over") != 1 {
		t.Fatalf("over: %+v, %d runs", e, r.f.Calls("over"))
	}
	// a Read tool's head is still worth reading
	for tool, whole := range map[string]string{"long": long, "deep": `{"items":"` + strings.Repeat("y", 400) + `"}`} {
		got := truncated(t, r, tool, limit)
		if !strings.HasPrefix(whole, got) {
			t.Errorf("%s: %q is not a head of the result", tool, got)
		}
	}
	r.private(long, "the same as text")
}

// a Read result over the instance's default cap of 8192 is cut to it
func TestReadResultsAreCutAtTheDefaultCap(t *testing.T) {
	r := newTools(t, mcptest.Options{})
	r.start("huge")
	r.set("huge", asRead)
	if got := truncated(t, r, "huge", 8192); strings.Trim(got, "h") != "" {
		t.Fatalf("text %s…", head([]byte(got)))
	}
}

// truncated calls tool and checks it answered a head of its text, marked
// truncated, within limit code points and no shorter than needed
func truncated(t *testing.T, r *toolRig, tool string, limit int) string {
	t.Helper()
	out, e := r.call(r.tool("mcp_srv_"+tool), `{}`)
	if e != nil {
		t.Fatalf("%s: %+v", tool, e)
	}
	var got struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(out, &got); err != nil || !got.Truncated {
		t.Fatalf("%s: %s…", tool, head(out))
	}
	if n := utf8.RuneCount(out); n > limit || n < limit-6 || !utf8.ValidString(got.Text) {
		t.Fatalf("%s: %d code points: %s…", tool, n, head(out))
	}
	return got.Text
}

func head(b []byte) string { return string(b[:min(len(b), 80)]) }

// structuredContent arrives as float64, so a whole number of 2^53 or more may
// have lost digits: the server's text mirror of it is used instead, and with
// none the structured form says so
func TestStructuredResultsThatLostPrecision(t *testing.T) {
	r := newTools(t, mcptest.Options{})
	add := func(name string, structured any, text string) {
		res := &sdk.CallToolResult{StructuredContent: structured}
		if text != "" {
			res.Content = []sdk.Content{&sdk.TextContent{Text: text}}
		}
		r.f.AddResult(&sdk.Tool{Name: name, Description: "R.", InputSchema: schema(anyObject)}, res)
	}
	add("mirrored", map[string]any{"id": uint64(12345678901234567890)}, `{"id":12345678901234567890}`)
	add("bare", map[string]any{"ids": []any{map[string]any{"id": int64(-9007199254740992)}}}, "")
	add("under", map[string]any{"id": int64(9007199254740991), "x": 1.5}, "under as text")
	r.start("under")
	for _, c := range []struct{ tool, want string }{
		{"mirrored", `{"text":"{\"id\":12345678901234567890}"}`},
		{"bare", `{"precisionLost":true,"result":{"ids":[{"id":-9007199254740992}]}}`},
		{"under", `{"result":{"id":9007199254740991,"x":1.5}}`},
	} {
		out, e := r.call(r.tool("mcp_srv_"+c.tool), `{}`)
		if e != nil || string(out) != c.want {
			t.Errorf("%s: %s %+v, want %s", c.tool, out, e, c.want)
		}
	}
}

// what can hide or reorder text goes from a result, in its text and in
// every string of its structure, keys too: C0 but \n and \t, DEL, C1, bidi
// controls, tag characters and U+FEFF; the joiners emoji and scripts need
// stay
func TestResultsDropSpoofingCharacters(t *testing.T) {
	r := newTools(t, mcptest.Options{})
	spoof := "a\x00b\u007fc\u0085d\u009fe\u202af\u202eg\u2066h\u2069i\u200ej\u200fk\u061cl\U000e0001m\U000e0041n\U000e007fo\ufeffp"
	r.f.AddResult(&sdk.Tool{Name: "text", Description: "R.", InputSchema: schema(anyObject)},
		&sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: spoof + "\n\tq\u200dr\u200cs"}}})
	r.f.AddResult(&sdk.Tool{Name: "structured", Description: "R.", InputSchema: schema(anyObject)},
		&sdk.CallToolResult{StructuredContent: map[string]any{"k\u202e\ufeff": []any{spoof}, "n": "x\u200dy"}})
	r.start("structured")
	for tool, want := range map[string]string{
		"text":       "{\"text\":\"abcdefghijklmnop\\n\\tq\u200dr\u200cs\"}",
		"structured": "{\"result\":{\"k\":[\"abcdefghijklmnop\"],\"n\":\"x\u200dy\"}}",
	} {
		out, e := r.call(r.tool("mcp_srv_"+tool), `{}`)
		if e != nil || string(out) != want {
			t.Errorf("%s: %+q %+v, want %+q", tool, out, e, want)
		}
	}
}
