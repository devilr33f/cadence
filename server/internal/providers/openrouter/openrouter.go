// Package openrouter streams chat completions over SSE. Upstream bodies never
// reach an error, chunk or log.
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultBaseURL   = "https://openrouter.ai/api/v1"
	defaultMaxTokens = 180
	maxLineBytes     = 1 << 20
)

type Config struct {
	BaseURL string
	// Module names the provider in a recovered panic's log line; "openrouter" when empty
	Module string
	// OpenAIReasoning sends OpenAI's reasoning_effort, and none for "none", instead of OpenRouter's reasoning
	// object: an OpenAI-compatible server other than OpenRouter knows only the former
	OpenAIReasoning bool
	// Model is used when a request names none
	Model string
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
}

type llm struct {
	log    *slog.Logger
	base   string
	key    string
	client *http.Client
	cfg    Config
}

func New(c Config, apiKey string, client *http.Client) providers.LLM {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Module == "" {
		c.Module = "openrouter"
	}
	return &llm{log: c.Log, base: strings.TrimRight(c.BaseURL, "/"), key: apiKey, client: client, cfg: c}
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type reasoning struct {
	Effort string `json:"effort"`
}

type request struct {
	Model         string         `json:"model"`
	Messages      []wireMessage  `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions map[string]any `json:"stream_options"`
	MaxTokens     int            `json:"max_tokens"`
	Tools         []wireTool     `json:"tools,omitempty"`
	ToolChoice    string         `json:"tool_choice,omitempty"`
	Reasoning     *reasoning     `json:"reasoning,omitempty"`
	// OpenAI's spelling, for servers other than OpenRouter (Config.OpenAIReasoning)
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type delta struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func buildRequest(r providers.LLMRequest, c Config) request {
	req := request{
		Model: r.Model, Stream: true, StreamOptions: map[string]any{"include_usage": true},
		MaxTokens: defaultMaxTokens, ToolChoice: r.ToolChoice,
	}
	if req.Model == "" {
		req.Model = c.Model
	}
	if r.MaxTokens > 0 {
		req.MaxTokens = r.MaxTokens
	}
	switch {
	case r.Reasoning == "":
	case !c.OpenAIReasoning:
		req.Reasoning = &reasoning{Effort: r.Reasoning}
	case r.Reasoning != "none":
		// a model that does not reason refuses the field, so none is left off rather than sent
		req.ReasoningEffort = r.Reasoning
	}
	req.Messages = make([]wireMessage, 0, len(r.Messages))
	for _, m := range r.Messages {
		w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Content != "" || len(m.ToolCalls) == 0 {
			c := m.Content
			w.Content = &c
		}
		for _, tc := range m.ToolCalls {
			c := wireCall{ID: tc.ID, Type: "function"}
			c.Function.Name = tc.Name
			c.Function.Arguments = wireArguments(tc.Arguments)
			w.ToolCalls = append(w.ToolCalls, c)
		}
		req.Messages = append(req.Messages, w)
	}
	for _, t := range r.Tools {
		w := wireTool{Type: "function"}
		w.Function.Name, w.Function.Description, w.Function.Parameters = t.Name, t.Description, t.Parameters
		req.Tools = append(req.Tools, w)
	}
	return req
}

// wireArguments sends a call's arguments back as the JSON text the api expects.
func wireArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var s string
	if raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

type pendingCall struct {
	id, name string
	args     strings.Builder
}

// assemble returns the calls in index order with their argument fragments joined.
func assemble(pending map[int]*pendingCall) []providers.ToolCall {
	idx := make([]int, 0, len(pending))
	for i := range pending {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	calls := make([]providers.ToolCall, 0, len(idx))
	for _, i := range idx {
		p := pending[i]
		args := strings.TrimSpace(p.args.String())
		switch {
		case args == "":
			args = "{}"
		case !json.Valid([]byte(args)):
			q, _ := json.Marshal(args)
			args = string(q)
		}
		calls = append(calls, providers.ToolCall{ID: p.id, Name: p.name, Arguments: json.RawMessage(args)})
	}
	return calls
}

var errFailed = errors.New(providers.CodeLLMFailed)

// Stream reports every failure as one chunk with Err, so callers have a single path.
func (l *llm) Stream(ctx context.Context, r providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	body, err := json.Marshal(buildRequest(r, l.cfg))
	if err != nil {
		return nil, errFailed
	}
	out := make(chan providers.LLMChunk, 8)
	go func() {
		defer close(out)
		var err error
		if providers.Guard(l.log, l.cfg.Module, providers.CodeLLMFailed, func() { err = l.run(ctx, body, out) }) {
			err = errFailed
		}
		if err != nil && ctx.Err() == nil {
			select {
			case out <- providers.LLMChunk{Err: providers.SafeError(err, providers.CodeLLMFailed)}:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

func (l *llm) run(ctx context.Context, body []byte, out chan<- providers.LLMChunk) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := l.client.Do(req)
	if err != nil {
		return errFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		code := providers.CodeProviderError
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			code = providers.CodeAuth
		case http.StatusTooManyRequests:
			code = providers.CodeRateLimited
		}
		return &providers.CallError{Code: code, Status: resp.StatusCode}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 4096), maxLineBytes)
	pending := map[int]*pendingCall{}
	started := false
	var usage *providers.Usage
	send := func(c providers.LLMChunk) error {
		select {
		case out <- c:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			if len(pending) > 0 {
				if err := send(providers.LLMChunk{ToolCalls: assemble(pending)}); err != nil {
					return err
				}
			}
			if usage != nil {
				return send(providers.LLMChunk{Usage: usage})
			}
			return nil
		}
		if decodeHook != nil {
			decodeHook([]byte(data))
		}
		var d delta
		if json.Unmarshal([]byte(data), &d) != nil || hasError(d.Error) {
			return errFailed
		}
		if d.Usage != nil {
			usage = &providers.Usage{PromptTokens: d.Usage.PromptTokens, CompletionTokens: d.Usage.CompletionTokens}
		}
		if len(d.Choices) == 0 {
			continue
		}
		if len(d.Choices[0].Delta.ToolCalls) > 0 && !started {
			// lets the caller stop speaking text that came with tool calls
			started = true
			if err := send(providers.LLMChunk{ToolCallStart: true}); err != nil {
				return err
			}
		}
		for _, tc := range d.Choices[0].Delta.ToolCalls {
			p := pending[tc.Index]
			if p == nil {
				p = &pendingCall{}
				pending[tc.Index] = p
			}
			if tc.ID != "" {
				p.id = tc.ID
			}
			if tc.Function.Name != "" {
				p.name = tc.Function.Name
			}
			p.args.WriteString(tc.Function.Arguments)
		}
		if text := d.Choices[0].Delta.Content; text != "" {
			if err := send(providers.LLMChunk{Text: text}); err != nil {
				return err
			}
		}
	}
	// the stream ended without [DONE]
	return errFailed
}

// absent and literal null both mean no error
func hasError(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}
