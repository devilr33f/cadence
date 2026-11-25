package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/providers/openrouter"
)

// LLMModule streams chat completions from {baseUrl}/chat/completions, as the openrouter module does, for a server of
// the operator's (a gateway, a self-hosted model): reasoning goes out as OpenAI's reasoning_effort, and model is used
// when neither the agent mode nor the model setting names one. The key is optional, TLS always verified.
var LLMModule = providers.Module{
	ID:   "openai-compatible-llm",
	Kind: providers.KindLLM,
	ConfigSchema: `{"type":"object","required":["baseUrl"],"additionalProperties":false,"properties":{` +
		`"baseUrl":{"type":"string","pattern":"^https://."},"model":{"type":"string","minLength":1}}}`,
	OptionalSecrets: []string{"apiKey"},
	Probe:           probeLLM,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		var c struct {
			BaseURL string `json:"baseUrl"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, errors.New("openai-compatible-llm: bad config")
		}
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("openai-compatible-llm: bad base url")
		}
		client := httpx.Client(nil)
		base := strings.TrimRight(c.BaseURL, "/")
		return &compatLLM{
			LLM: openrouter.New(openrouter.Config{
				BaseURL: base, Module: "openai-compatible-llm", OpenAIReasoning: true, Model: c.Model,
			}, sec["apiKey"], client),
			base: base, key: sec["apiKey"], client: client,
		}, nil
	},
}

type compatLLM struct {
	providers.LLM
	base, key string
	client    *http.Client
}

// probeLLM lists the server's models (GET {baseUrl}/models): it generates nothing, so nothing is billed
func probeLLM(ctx context.Context, v any) error {
	l, ok := v.(*compatLLM)
	if !ok {
		return providers.ProbeBadResponse
	}
	h := http.Header{}
	if l.key != "" {
		h.Set("Authorization", "Bearer "+l.key)
	}
	_, err := providers.ProbeGET(ctx, l.client, l.base+"/models", h)
	return err
}
