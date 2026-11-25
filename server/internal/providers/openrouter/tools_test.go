package openrouter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
)

func TestOpenRouterToolCallDeltas(t *testing.T) {
	chunks := sse(t,
		`data: {"choices":[{"delta":{"content":"ok "}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"alpha","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"beta","arguments":"{\"b\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"content":"late","tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"data: [DONE]")
	// the start is marked once, at the first tool call delta, before text that comes with it
	if len(chunks) != 4 || chunks[0].Text != "ok " || chunks[0].ToolCallStart ||
		!chunks[1].ToolCallStart || chunks[1].Text != "" || len(chunks[1].ToolCalls) != 0 ||
		chunks[2].Text != "late" || chunks[2].ToolCallStart || len(chunks[3].ToolCalls) != 2 || chunks[3].ToolCallStart {
		t.Fatalf("chunks %+v", chunks)
	}
	a, b := chunks[3].ToolCalls[0], chunks[3].ToolCalls[1]
	if a.ID != "c0" || a.Name != "alpha" || string(a.Arguments) != `{"a":1}` ||
		b.ID != "c1" || b.Name != "beta" || string(b.Arguments) != `{"b":2}` {
		t.Fatalf("calls %+v %+v", a, b)
	}
}

func TestOpenRouterToolCallEdgeArguments(t *testing.T) {
	chunks := sse(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":""}},{"index":1,"id":"c1","function":{"name":"b","arguments":"{oops"}}]}}]}`,
		"data: [DONE]")
	if len(chunks) != 2 || !chunks[0].ToolCallStart || len(chunks[1].ToolCalls) != 2 {
		t.Fatalf("chunks %+v", chunks)
	}
	if got := string(chunks[1].ToolCalls[0].Arguments); got != "{}" {
		t.Fatalf("empty args %s", got)
	}
	if got := string(chunks[1].ToolCalls[1].Arguments); got != `"{oops"` {
		t.Fatalf("invalid args %s", got)
	}
}

func TestOpenRouterReasoningAndToolChoice(t *testing.T) {
	seen := make(chan map[string]any, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		seen <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	llm := openrouter.New(openrouter.Config{BaseURL: srv.URL}, "k", httpx.Client(roots(srv)))
	_, err := collectReq(t, llm, providers.LLMRequest{
		Model: "m/x", ToolChoice: "none", Reasoning: "low", MaxTokens: 50,
		Messages: []providers.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "c1", Name: "t", Arguments: json.RawMessage(`{"x":1}`)}}},
			{Role: "tool", ToolCallID: "c1", Content: "res"},
		},
		Tools: []providers.ToolSpec{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := <-seen
	r, _ := b["reasoning"].(map[string]any)
	so, _ := b["stream_options"].(map[string]any)
	tools, _ := b["tools"].([]any)
	if b["tool_choice"] != "none" || r["effort"] != "low" || so["include_usage"] != true || b["max_tokens"] != float64(50) || len(tools) != 1 {
		t.Fatalf("body %v", b)
	}
	tool := tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if tool["type"] != "function" || fn["name"] != "t" || fn["description"] != "d" {
		t.Fatalf("tool %v", tool)
	}
	msgs := b["messages"].([]any)
	as := msgs[1].(map[string]any)
	tc := as["tool_calls"].([]any)[0].(map[string]any)
	if as["content"] != nil || tc["id"] != "c1" || tc["type"] != "function" || tc["function"].(map[string]any)["arguments"] != `{"x":1}` {
		t.Fatalf("assistant %v", as)
	}
	if m := msgs[2].(map[string]any); m["role"] != "tool" || m["tool_call_id"] != "c1" || m["content"] != "res" {
		t.Fatalf("tool message %v", m)
	}
}

func TestOpenRouterUsage(t *testing.T) {
	chunks := sse(t,
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
		"data: [DONE]")
	if len(chunks) != 2 || chunks[0].Text != "a" || chunks[1].Usage == nil ||
		chunks[1].Usage.PromptTokens != 12 || chunks[1].Usage.CompletionTokens != 3 {
		t.Fatalf("chunks %+v", chunks)
	}
}
