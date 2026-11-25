package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// LoadSkill is the tool a turn pulls a listed skill in with.
const LoadSkill = "load_skill"

// Skill is a named instruction pack a turn loads only when its input calls
// for it, so the system prompt stays small. Its text is the owner's or the
// server's own: trusted, and never logged.
type Skill struct {
	Name, Description, Body string
	// TitleRU and TitleEN are the quiet step's display names; empty falls back
	// to another language, then to Name
	TitleRU, TitleEN string
	// Tools are the tools it is about: a turn offered none of them does not
	// list it. None: always listed.
	Tools []string
	// Triggers are words and phrases of an input it fits, any language
	Triggers []string
	// Modes are the modes it is listed in; nil: every mode
	Modes []string
}

// SkillQuery is what a turn's skill pick reads: its input, and the input's
// embedding when the memory recall made one. Embed names the embed instance,
// so the skills' own vectors can be made for later turns.
type SkillQuery struct {
	Input  string
	Vector []float32
	Model  string
	Embed  string
}

// Skills supplies a turn's skills. Both calls answer from memory at once.
type Skills interface {
	// For is the enabled skills of mode, by name.
	For(mode string) []Skill
	// Pick is the names of the skills to load before the first step, best
	// first and few: deterministic for an input and the vectors at hand.
	Pick(skills []Skill, q SkillQuery) []string
}

// turnSkills is what a turn listed and loaded; load_skill runs beside other
// Read calls, so it is locked.
type turnSkills struct {
	listed []Skill

	mu     sync.Mutex
	loaded []string
}

func (s *turnSkills) load(name string) (Skill, bool, bool) {
	i := slices.IndexFunc(s.listed, func(k Skill) bool { return k.Name == name })
	if i < 0 {
		return Skill{}, false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.loaded, name) {
		return s.listed[i], true, true
	}
	s.loaded = append(s.loaded, name)
	return s.listed[i], true, false
}

func (s *turnSkills) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.loaded)
}

// all reports every listed skill loaded, so load_skill has nothing left
func (s *turnSkills) all() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.loaded) >= len(s.listed)
}

// maxListedSkills bounds the index's lines
const maxListedSkills = 24

// prepareSkills lists the mode's skills that fit the turn's tools and loads
// the ones its input calls for. An event turn has no user's words to fit, so
// it gets none.
func (r *Runner) prepareSkills(tr *turn, q SkillQuery) (index, loaded string) {
	if r.d.Skills == nil || EventTurn(tr.Trigger) {
		return "", ""
	}
	offered := toolNames(r.offered(tr))
	var listed []Skill
	for _, s := range r.d.Skills.For(tr.mode.Name) {
		if len(s.Tools) == 0 || slices.ContainsFunc(s.Tools, func(t string) bool { return slices.Contains(offered, t) }) {
			listed = append(listed, s)
		}
		if len(listed) == maxListedSkills {
			break
		}
	}
	if len(listed) == 0 {
		return "", ""
	}
	tr.skills = &turnSkills{listed: listed}
	tr.toolset = append(slices.Clip(tr.toolset), loadSkillTool(r, tr))
	var picked []Skill
	for _, name := range r.d.Skills.Pick(listed, q) {
		if s, ok, again := tr.skills.load(name); ok && !again {
			picked = append(picked, s)
		}
	}
	if len(picked) > 0 {
		r.publish(tr, "skills_loaded", map[string]any{"names": skillNames(picked), "by": "input"})
	}
	return skillsIndex(listed), skillsNote(picked)
}

const skillsIndexHeader = "Skills: extra instructions for some kinds of request. Those that fit this request are " +
	"loaded below; when it needs another one listed here, call load_skill with its name first."

// skillsIndex lists the skills by name and description; it changes only with
// the skills or the tools a turn offers, so it stays in the cached prefix
func skillsIndex(listed []Skill) string {
	var b strings.Builder
	b.WriteString(skillsIndexHeader)
	for _, s := range listed {
		b.WriteString("\n- " + s.Name + ": " + oneLine(s.Description))
	}
	return b.String()
}

// skillsNote is the loaded skills' text, late in the context: it follows the
// input, so it would break the cached prefix anywhere earlier
func skillsNote(picked []Skill) string {
	if len(picked) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Loaded skills: follow them for this request.")
	for _, s := range picked {
		b.WriteString("\n\n## " + s.Name + "\n" + strings.TrimSpace(s.Body))
	}
	return b.String()
}

func skillNames(ss []Skill) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

const loadSkillInput = `{
	"type": "object",
	"properties": {"name": {"type": "string", "minLength": 1, "maxLength": 48}},
	"required": ["name"],
	"additionalProperties": false
}`

// loadSkillTool is a turn's own tool: it loads a skill the turn listed, and
// only those, whatever the mode's or the device's tool lists say (it reads
// the server's and the owner's text, never the world)
func loadSkillTool(r *Runner, tr *turn) Tool {
	t := Tool{
		Name: LoadSkill,
		Description: "Load a listed skill's instructions by its name, before acting on a request it is about. " +
			"Say nothing about it to the user.",
		Input:   loadSkillInput,
		Effects: Read, Output: Trusted,
		turnScoped: true,
		Run: func(_ context.Context, c Call) (json.RawMessage, *ToolError) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, &ToolError{Code: "invalid_arguments", Message: "The arguments are not a JSON object.", Retryable: true}
			}
			s, ok, again := tr.skills.load(strings.TrimSpace(a.Name))
			if !ok {
				return nil, &ToolError{Code: "unknown_skill", Message: "No skill of that name is listed.",
					Candidates: skillNames(tr.skills.listed)}
			}
			if again {
				return skillResult(map[string]any{"name": s.Name, "loaded": true, "note": "already in the context"})
			}
			r.publish(tr, "skills_loaded", map[string]any{"names": []string{s.Name}, "by": "tool"})
			return skillResult(map[string]any{"name": s.Name, "instructions": strings.TrimSpace(s.Body)})
		},
	}
	t, _ = Compile(t)
	return t
}

func skillResult(v map[string]any) (json.RawMessage, *ToolError) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: "The skill could not be read."}
	}
	return b, nil
}
