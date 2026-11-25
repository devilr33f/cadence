package fake_test

import (
	"context"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestFakeLLMScript(t *testing.T) {
	llm := build(t, "fake-llm", `{"script":[
		{"toolCalls":[{"name":"a","arguments":{"x":1}}]},
		{"text":"done one"},
		{"text":"last"}]}`).(providers.LLM)
	// the last chunk of a step; a tool step marks where its calls start first
	step := func(msgs ...providers.Message) providers.LLMChunk {
		ch, err := llm.Stream(context.Background(), providers.LLMRequest{Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var all []providers.LLMChunk
		for c := range ch {
			all = append(all, c)
		}
		last := all[len(all)-1]
		if len(last.ToolCalls) > 0 && (len(all) != 2 || !all[0].ToolCallStart) || len(last.ToolCalls) == 0 && len(all) != 1 {
			t.Fatalf("chunks %+v", all)
		}
		return last
	}
	u := providers.Message{Role: "user", Content: "q"}
	as := providers.Message{Role: "assistant"}
	tool := providers.Message{Role: "tool", ToolCallID: "x"}
	// assistant messages before the last user message do not count
	first := step(as, u)
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "a" || string(first.ToolCalls[0].Arguments) != `{"x":1}` || first.ToolCalls[0].ID == "" {
		t.Fatalf("step 0 %+v", first)
	}
	if c := step(u, as, tool); c.Text != "done one" {
		t.Fatalf("step 1 %+v", c)
	}
	if c := step(u, as, tool, as, tool); c.Text != "last" {
		t.Fatalf("step 2 %+v", c)
	}
	if c := step(u, as, tool, as, tool, as, tool); c.Text != "last" {
		t.Fatalf("cycled step %+v", c)
	}
}

func TestFakeLLMRules(t *testing.T) {
	llm := build(t, "fake-llm", `{"reply":"plain","script":[{"text":"scripted"}],"rules":[
		{"system":"running summary","text":"{\"summary\":\"s\"}"},
		{"model":"m1","system":"long-term memory","text":"{\"evidence\":[{{lastUserId}}]}"},
		{"model":"m2","text":"second"}]}`).(providers.LLM)
	text := func(model string, msgs ...providers.Message) string {
		ch, err := llm.Stream(context.Background(), providers.LLMRequest{Model: model, Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for c := range ch {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	sys := func(s string) providers.Message { return providers.Message{Role: "system", Content: s} }
	user := func(s string) providers.Message { return providers.Message{Role: "user", Content: s} }
	if got := text("any", sys("You keep the running summary."), user("q")); got != `{"summary":"s"}` {
		t.Fatalf("by system prompt, any model: %q", got)
	}
	// the newest entry line of a user; an indented line continues a message and counts for nothing
	in := "Conversation:\n[7] User: hi\n[8] Assistant: hello\n[12] User: lamp\n    [99] User: inside\n[13] Assistant: ok"
	if got := text("m1", sys("You keep the long-term memory."), user(in)); got != `{"evidence":[12]}` {
		t.Fatalf("placeholder: %q", got)
	}
	if got := text("m1", sys("long-term memory"), user("no entries")); got != `{"evidence":[{{lastUserId}}]}` {
		t.Fatalf("unresolved placeholder: %q", got)
	}
	if got := text("m2", user("q")); got != "second" {
		t.Fatalf("by model: %q", got)
	}
	// the system prompt must open the request
	if got := text("other", user("q"), sys("running summary")); got != "scripted" {
		t.Fatalf("no rule matches: %q", got)
	}
}

// an event turn has a turn's system prompt: its rule matches the newest user
// message, the framing, with the seeded call and its output after it
func TestFakeLLMRulesMatchTheNewestUserMessage(t *testing.T) {
	llm := build(t, "fake-llm", `{"script":[{"text":"scripted"}],"rules":[
		{"system":"running summary","text":"summary"},
		{"user":"A background task changed","text":"news"}]}`).(providers.LLM)
	text := func(msgs ...providers.Message) string {
		ch, err := llm.Stream(context.Background(), providers.LLMRequest{Model: "m", Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for c := range ch {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	sys := providers.Message{Role: "system", Content: "You are Cadence."}
	event := providers.Message{Role: "user", Content: "A background task changed. Task t-1 is done."}
	seed := providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "e", Name: "task_inspect"}}}
	out := providers.Message{Role: "tool", ToolCallID: "e", Content: `{"status":"done"}`}
	if got := text(sys, event, seed, out); got != "news" {
		t.Fatalf("event turn: %q", got)
	}
	// a later turn has the framing in its history only
	if got := text(sys, event, seed, out, providers.Message{Role: "assistant", Content: "news"},
		providers.Message{Role: "user", Content: "what time is it"}); got != "scripted" {
		t.Fatalf("turn after it: %q", got)
	}
	// rules keep their order: the compaction's prompt renders the framing too
	if got := text(providers.Message{Role: "system", Content: "You keep the running summary."},
		providers.Message{Role: "user", Content: "[1] User: A background task changed."}); got != "summary" {
		t.Fatalf("compaction: %q", got)
	}
}

// a turn's notes (memories, the home, tasks, the board) are system messages
// after its system prompt: the e2e's home note check matches one of them
func TestFakeLLMRulesMatchATurnsNotes(t *testing.T) {
	llm := build(t, "fake-llm", `{"reply":"","rules":[{"note":"- light.hall (light, Hall)","text":"seen"}]}`).(providers.LLM)
	text := func(msgs ...providers.Message) string {
		ch, err := llm.Stream(context.Background(), providers.LLMRequest{Model: "m", Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for c := range ch {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	sys := func(s string) providers.Message { return providers.Message{Role: "system", Content: s} }
	user := func(s string) providers.Message { return providers.Message{Role: "user", Content: s} }
	note := "Home Assistant devices, live.\n- light.hall (light, Hall) \"Hall light\": on"
	if got := text(sys("You are Cadence."), sys("Memories."), sys(note), user("q")); got != "seen" {
		t.Fatalf("in a note: %q", got)
	}
	// neither the system prompt nor what a user says is a note
	if got := text(sys(note), user("q")); got != "" {
		t.Fatalf("in the system prompt: %q", got)
	}
	if got := text(sys("You are Cadence."), user(note)); got != "" {
		t.Fatalf("in a user message: %q", got)
	}
}
