package skills

import (
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// SkillView is a skill as turns read it.
type SkillView struct {
	Name        string     `json:"name"`
	Description string     `json:"description" doc:"one line the model reads in every turn's skill list, and the pick's similarity reads"`
	Body        string     `json:"body" doc:"the instructions a turn loads"`
	Tools       []string   `json:"tools" nullable:"false" doc:"the tools it is about: a turn offered none of them does not list it; empty lists it always"`
	Triggers    []string   `json:"triggers" nullable:"false" doc:"words and phrases of an input that load it before the first step, matched in any case and word form"`
	Modes       []string   `json:"modes" nullable:"true" doc:"the agent modes that list it; null lists it in every mode"`
	Enabled     bool       `json:"enabled"`
	Source      string     `json:"source" enum:"builtin,owner" doc:"builtin: shipped with the server; owner: stored, replacing a built-in of its name when overrides is set"`
	Overrides   bool       `json:"overrides" doc:"an owner skill named as a built-in one, which it replaces; deleting it brings the built-in back"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty" doc:"when the owner last saved it; absent for a built-in"`
}

// PutSkillRequest is a whole skill.
type PutSkillRequest struct {
	Description string   `json:"description" minLength:"1" maxLength:"200" doc:"one line, no control or format character"`
	Body        string   `json:"body" minLength:"1" maxLength:"4000" doc:"the instructions, markdown"`
	Tools       []string `json:"tools,omitempty" nullable:"false" maxItems:"24" uniqueItems:"true" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"the tools it is about; a name no tool has yet is kept"`
	Triggers    []string `json:"triggers,omitempty" nullable:"false" maxItems:"48" doc:"each 1-64 characters with a letter or digit, one line"`
	Modes       []string `json:"modes,omitempty" nullable:"true" maxItems:"16" uniqueItems:"true" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"the agent modes that list it; null or absent: every mode"`
	Enabled     *bool    `json:"enabled,omitempty" doc:"absent: true"`
}

func view(e Entry) SkillView {
	v := SkillView{
		Name: e.Name, Description: e.Description, Body: e.Body, Tools: orEmpty(e.Tools), Triggers: orEmpty(e.Triggers),
		Modes: e.Modes, Enabled: e.Enabled, Source: e.Source, Overrides: e.Overrides,
	}
	if e.Source == SourceOwner && !e.UpdatedAt.IsZero() {
		at := e.UpdatedAt
		v.UpdatedAt = &at
	}
	return v
}

// entryOf is the skill a put stores, its lists trimmed, or the pointer of the
// first field the runtime could not use
func entryOf(name string, b PutSkillRequest) (Entry, string) {
	s := agent.Skill{Name: name, Description: strings.TrimSpace(b.Description), Body: strings.TrimSpace(b.Body),
		Tools: b.Tools, Modes: b.Modes}
	for _, t := range b.Triggers {
		s.Triggers = append(s.Triggers, strings.TrimSpace(t))
	}
	if path, ok := check(s); !ok {
		return Entry{}, path
	}
	enabled := b.Enabled == nil || *b.Enabled
	return Entry{Skill: s, Enabled: enabled, Source: SourceOwner}, ""
}
