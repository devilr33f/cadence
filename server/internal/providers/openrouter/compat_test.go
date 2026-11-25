package openrouter

import (
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func wire(t *testing.T, r providers.LLMRequest, c Config) string {
	t.Helper()
	b, err := json.Marshal(buildRequest(r, c))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// openrouter keeps its reasoning object; an openai-compatible server gets reasoning_effort, and nothing for none,
// which a model that does not reason would refuse
func TestReasoningSpelling(t *testing.T) {
	r := providers.LLMRequest{Model: "m", Reasoning: "medium"}
	if got := wire(t, r, Config{}); !strings.Contains(got, `"reasoning":{"effort":"medium"}`) || strings.Contains(got, "reasoning_effort") {
		t.Fatalf("openrouter %s", got)
	}
	if got := wire(t, r, Config{OpenAIReasoning: true}); !strings.Contains(got, `"reasoning_effort":"medium"`) ||
		strings.Contains(got, `"reasoning":`) {
		t.Fatalf("openai %s", got)
	}
	r.Reasoning = "none"
	if got := wire(t, r, Config{OpenAIReasoning: true}); strings.Contains(got, "reasoning") {
		t.Fatalf("none %s", got)
	}
}

func TestDefaultModel(t *testing.T) {
	if got := wire(t, providers.LLMRequest{}, Config{Model: "main"}); !strings.Contains(got, `"model":"main"`) {
		t.Fatalf("default %s", got)
	}
	if got := wire(t, providers.LLMRequest{Model: "small"}, Config{Model: "main"}); !strings.Contains(got, `"model":"small"`) {
		t.Fatalf("the request's own %s", got)
	}
}
