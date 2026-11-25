package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

var llmModule = providers.Module{
	ID:    "fake-llm",
	Kind:  providers.KindLLM,
	Probe: probe,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{"reply":{"type":"string"},` +
		`"script":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"text":{"type":"string"},` +
		`"toolCalls":{"type":"array","items":{"type":"object","required":["name"],"additionalProperties":false,` +
		`"properties":{"name":{"type":"string"},"arguments":{}}}}}}},` +
		`"rules":{"type":"array","items":{"type":"object","required":["text"],"additionalProperties":false,` +
		`"properties":{"model":{"type":"string"},"system":{"type":"string"},"note":{"type":"string"},` +
		`"user":{"type":"string"},"text":{"type":"string"}}}}}}`,
	New: func(cfg json.RawMessage, _ map[string]string) (any, error) {
		c := struct {
			Reply  string       `json:"reply"`
			Script []scriptStep `json:"script"`
			Rules  []rule       `json:"rules"`
		}{Reply: "It is test time. All good."}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		return &llm{reply: c.Reply, script: c.Script, rules: c.Rules}, nil
	},
}

type scriptStep struct {
	Text      string `json:"text"`
	ToolCalls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"toolCalls"`
}

// rule answers, ahead of the script and the reply, a request on Model (any
// when empty) whose first message is a system prompt containing System, one
// of whose later system messages (a turn's notes: memories, the home, tasks,
// the board) contains Note and whose newest user message contains User (each
// any when empty): the e2e's maintenance model, which compacts and learns, its
// event turns, which have a turn's system prompt and the server's framing as
// their input, and its home note check. In
// Text, lastUserIDPlaceholder becomes the id of the newest user entry the
// request shows, and stays without one.
type rule struct {
	Model  string `json:"model"`
	System string `json:"system"`
	Note   string `json:"note"`
	User   string `json:"user"`
	Text   string `json:"text"`
}

const lastUserIDPlaceholder = "{{lastUserId}}"

// userEntry is a user's message in the segment the learning prompt renders:
// "[id] User: ..." at a line's start, a message's own lines being indented
var userEntry = regexp.MustCompile(`(?m)^\[(\d+)\] User:`)

type llm struct {
	reply  string
	script []scriptStep
	rules  []rule
}

// match is the text of the first rule for r, if any.
func (l *llm) match(r providers.LLMRequest) (string, bool) {
	for _, ru := range l.rules {
		if ru.Model != "" && ru.Model != r.Model {
			continue
		}
		if ru.System != "" && (len(r.Messages) == 0 || r.Messages[0].Role != "system" || !strings.Contains(r.Messages[0].Content, ru.System)) {
			continue
		}
		if ru.Note != "" && !inNote(r.Messages, ru.Note) {
			continue
		}
		if ru.User != "" && !strings.Contains(lastUser(r.Messages), ru.User) {
			continue
		}
		if id := lastUserID(r.Messages); id != "" {
			return strings.ReplaceAll(ru.Text, lastUserIDPlaceholder, id), true
		}
		return ru.Text, true
	}
	return "", false
}

// lastUserID is the id of the newest user entry in the user messages, empty
// without one.
func lastUserID(msgs []providers.Message) string {
	id := ""
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if all := userEntry.FindAllStringSubmatch(m.Content, -1); len(all) > 0 {
			id = all[len(all)-1][1]
		}
	}
	return id
}

// inNote reports a system message after the first that contains s.
func inNote(msgs []providers.Message, s string) bool {
	for i, m := range msgs {
		if i > 0 && m.Role == "system" && strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

// lastUser is the content of the newest user message, empty without one.
func lastUser(msgs []providers.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// agentStep counts the assistant messages after the last user message
func agentStep(msgs []providers.Message) int {
	n := 0
	for i := len(msgs) - 1; i >= 0 && msgs[i].Role != "user"; i-- {
		if msgs[i].Role == "assistant" {
			n++
		}
	}
	return n
}

func (l *llm) Stream(ctx context.Context, r providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	var chunks []providers.LLMChunk
	if text, ok := l.match(r); ok {
		for _, p := range split3(text) {
			chunks = append(chunks, providers.LLMChunk{Text: p})
		}
	} else if len(l.script) > 0 {
		step := agentStep(r.Messages)
		s := l.script[min(step, len(l.script)-1)]
		if s.Text != "" {
			chunks = append(chunks, providers.LLMChunk{Text: s.Text})
		}
		if len(s.ToolCalls) > 0 {
			calls := make([]providers.ToolCall, len(s.ToolCalls))
			for i, c := range s.ToolCalls {
				args := c.Arguments
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				calls[i] = providers.ToolCall{ID: fmt.Sprintf("fake-%d-%d", step, i), Name: c.Name, Arguments: args}
			}
			chunks = append(chunks, providers.LLMChunk{ToolCallStart: true}, providers.LLMChunk{ToolCalls: calls})
		}
	} else {
		for _, p := range split3(l.reply) {
			chunks = append(chunks, providers.LLMChunk{Text: p})
		}
	}
	out := make(chan providers.LLMChunk, len(chunks))
	go func() {
		defer close(out)
		for _, c := range chunks {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// split3 cuts at word boundaries into at most three chunks that rejoin exactly
func split3(s string) []string {
	words := strings.SplitAfter(s, " ")
	if len(words) <= 3 {
		return words
	}
	per := (len(words) + 2) / 3
	var out []string
	for i := 0; i < len(words); i += per {
		out = append(out, strings.Join(words[i:min(i+per, len(words))], ""))
	}
	return out
}
