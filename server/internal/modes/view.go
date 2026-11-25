// Package modes serves the agent modes over the admin api: each one's model,
// reasoning, budgets, tools and voice aliases, as the runtime reads them per
// turn.
package modes

import (
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// ModeView is an agent mode as the runtime holds it.
type ModeView struct {
	Name                string      `json:"name"`
	Model               string      `json:"model" doc:"a model id at the device's llm instance; empty uses the device's model setting"`
	Reasoning           string      `json:"reasoning" enum:"none,low,medium,high"`
	MaxSteps            int         `json:"maxSteps" doc:"model calls a turn makes before its answer is forced"`
	MaxSeconds          int         `json:"maxSeconds" doc:"seconds a turn takes before its answer is forced"`
	MaxTokens           *int        `json:"maxTokens,omitempty" nullable:"true" minimum:"64" maximum:"32768" doc:"the ceiling on the model's max_tokens a step allows; null keeps the built-in default (4096 with reasoning on, else 1024)"`
	FirstTokenTimeoutMs *int        `json:"firstTokenTimeoutMs,omitempty" nullable:"true" doc:"milliseconds a step waits for the model's first token before it counts as stalled: 0 or 2000–120000; null or 0 keeps the built-in 10 s (slow reasoning models need more)"`
	Interim             string      `json:"interim" enum:"drop,speak" doc:"whether text that comes with tool calls is spoken"`
	Tools               []string    `json:"tools" nullable:"true" doc:"the built-in tools the mode offers, within the device's tools setting; null offers every one"`
	Aliases             ModeAliases `json:"aliases"`
}

// ModeAliases are the words that name a mode by voice, per language; the
// agent_mode tool matches them in any case.
type ModeAliases struct {
	En []string `json:"en,omitempty" nullable:"false" maxItems:"10" minLength:"1"`
	Ru []string `json:"ru,omitempty" nullable:"false" maxItems:"10" minLength:"1"`
}

// PutModeRequest is a whole mode; the fields the table defaults may be left
// out: model (empty), maxTokens and firstTokenTimeoutMs (null), tools (null) and aliases (none).
type PutModeRequest struct {
	Model               string      `json:"model,omitempty" maxLength:"200" doc:"a model id at the device's llm instance, at most 200 bytes with no control or format character and no line or paragraph separator; empty uses the device's model setting"`
	Reasoning           string      `json:"reasoning" enum:"none,low,medium,high"`
	MaxSteps            int         `json:"maxSteps" minimum:"1" maximum:"50" doc:"model calls a turn makes before its answer is forced"`
	MaxSeconds          int         `json:"maxSeconds" minimum:"5" maximum:"600" doc:"seconds a turn takes before its answer is forced"`
	MaxTokens           *int        `json:"maxTokens,omitempty" nullable:"true" minimum:"64" maximum:"32768" doc:"the ceiling on the model's max_tokens a step allows; null keeps the built-in default (4096 with reasoning on, else 1024)"`
	FirstTokenTimeoutMs *int        `json:"firstTokenTimeoutMs,omitempty" nullable:"true" minimum:"0" maximum:"120000" doc:"milliseconds a step waits for the model's first token before it counts as stalled: 0 or 2000–120000; null or 0 keeps the built-in 10 s (slow reasoning models need more)"`
	Interim             string      `json:"interim" enum:"drop,speak" doc:"whether text that comes with tool calls is spoken"`
	Tools               []string    `json:"tools,omitempty" nullable:"true" maxItems:"64" uniqueItems:"true" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"the built-in tools the mode offers, within the device's tools setting; null offers every one; a name no tool has yet is kept"`
	Aliases             ModeAliases `json:"aliases,omitempty" doc:"each phrase trimmed of white space, then 1–40 characters with no control or format character and no line or paragraph separator; distinct in any case within its list, and from every other mode's aliases in any language and its name (409 alias_taken)"`
}

// languages are the alias lists in order, by key
var languages = []string{"en", "ru"}

func (a ModeAliases) lists() [][]string { return [][]string{a.En, a.Ru} }

func view(m agent.Mode) ModeView {
	v := ModeView{
		Name: m.Name, Model: m.Model, Reasoning: m.Reasoning, MaxSteps: m.MaxSteps,
		MaxSeconds: int(m.MaxDuration / time.Second), Interim: m.Interim, Tools: m.Tools,
		Aliases: ModeAliases{En: m.Aliases["en"], Ru: m.Aliases["ru"]},
	}
	if m.MaxTokens > 0 {
		tokens := m.MaxTokens
		v.MaxTokens = &tokens
	}
	if m.FirstTokenTimeout > 0 {
		ms := int(m.FirstTokenTimeout.Milliseconds())
		v.FirstTokenTimeoutMs = &ms
	}
	return v
}
