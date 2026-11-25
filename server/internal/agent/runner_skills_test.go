package agent_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type skillsFake struct {
	all    []agent.Skill
	picked []string
	asked  []agent.SkillQuery
}

func (f *skillsFake) For(string) []agent.Skill { return f.all }

func (f *skillsFake) Pick(listed []agent.Skill, q agent.SkillQuery) []string {
	f.asked = append(f.asked, q)
	var out []string
	for _, name := range f.picked {
		if slices.ContainsFunc(listed, func(s agent.Skill) bool { return s.Name == name }) {
			out = append(out, name)
		}
	}
	return out
}

const homeBody = "SECRET-HOME-BODY: call ha_control for every change."

func skillSet() *skillsFake {
	return &skillsFake{all: []agent.Skill{
		{Name: "home", Description: "Home devices.", Body: homeBody, Tools: []string{"ha_control"}},
		{Name: "notes", Description: "How to keep notes.", Body: "NOTES-BODY"},
		{Name: "tv", Description: "TV.", Body: "TV-BODY", Tools: []string{"catalog_search"}},
	}}
}

// the input's skill loads before the first step, late in the context; the
// list of the rest sits right after the system prompt, without the skills
// whose tools the turn is not offered; the turn records the names alone
func TestSkillLoadedByInput(t *testing.T) {
	h := newHarness(t, llmStep{text: "Done."})
	ha, _ := fakeTool("ha_control", agent.Write, agent.Trusted, `{"ok":true}`, nil)
	h.tools = []agent.Tool{ha}
	h.skills = skillSet()
	h.skills.(*skillsFake).picked = []string{"home"}
	h.run(wake, "Включи свет")
	req := h.requests(1)[0]
	index := req.Messages[1].Content
	if req.Messages[1].Role != "system" || !strings.Contains(index, "- home: Home devices.") ||
		!strings.Contains(index, "- notes:") || strings.Contains(index, "tv") || strings.Contains(index, homeBody) {
		t.Fatalf("index %q", index)
	}
	n := len(req.Messages)
	if loaded := req.Messages[n-3].Content; !strings.Contains(loaded, "## home\n"+homeBody) || strings.Contains(loaded, "NOTES-BODY") {
		t.Fatalf("loaded note %q (messages %+v)", loaded, req.Messages)
	}
	if !strings.HasPrefix(req.Messages[n-2].Content, "Local time:") {
		t.Fatal("the loaded skills do not sit before the volatile note")
	}
	if got := toolNames(req.Tools); !slices.Equal(got, []string{"ha_control", agent.LoadSkill}) {
		t.Fatalf("tools %v", got)
	}
	if got := h.convs.stored[0].meta.Skills; !reflect.DeepEqual(got, []string{"home"}) {
		t.Fatalf("recorded skills %v", got)
	}
	if ev := eventsOf(h.events(), "skills_loaded"); len(ev) != 1 || !reflect.DeepEqual(ev[0]["names"], []string{"home"}) {
		t.Fatalf("events %v", ev)
	}
	if q := h.skills.(*skillsFake).asked; len(q) != 1 || q[0].Input != "Включи свет" {
		t.Fatalf("pick asked %+v", q)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `"skills":"home"`) || strings.Contains(logs, "SECRET-HOME-BODY") || strings.Contains(logs, "Включи") {
		t.Fatalf("log %s", logs)
	}
}

// the model loads a listed skill mid-turn, even where the mode's tool list
// does not name load_skill; an unlisted name is refused with the candidates
func TestLoadSkillTool(t *testing.T) {
	h := newHarness(t,
		llmStep{calls: []providers.ToolCall{toolCall("c1", agent.LoadSkill, `{"name":"tv"}`)}},
		llmStep{calls: []providers.ToolCall{toolCall("c2", agent.LoadSkill, `{"name":"notes"}`)}},
		llmStep{text: "Noted."},
	)
	mode := fastMode
	mode.Tools = []string{"weather"}
	h.modes["fast"] = mode
	h.skills = skillSet()
	h.run(wake, "remember this")
	reqs := h.requests(3)
	if c := last(reqs[1].Messages).Content; !strings.Contains(c, `"code":"unknown_skill"`) || !strings.Contains(c, `"notes"`) {
		t.Fatalf("unlisted: %s", c)
	}
	if c := last(reqs[2].Messages).Content; !strings.Contains(c, `"instructions":"NOTES-BODY"`) {
		t.Fatalf("loaded: %s", c)
	}
	// every listed skill is loaded: nothing left to call (the last list is
	// sent for the history's sake, with no tool choice)
	if reqs[2].ToolChoice != "none" {
		t.Fatalf("load_skill offered with nothing left: %q", reqs[2].ToolChoice)
	}
	if got := h.convs.stored[0].meta.Skills; !reflect.DeepEqual(got, []string{"notes"}) {
		t.Fatalf("recorded skills %v", got)
	}
}

// an event turn has no user's words: no skills, no load_skill
func TestEventTurnsGetNoSkills(t *testing.T) {
	h := newHarness(t, llmStep{text: "Готово."})
	h.skills = skillSet()
	h.skills.(*skillsFake).picked = []string{"notes"}
	h.run(cadencev2.Trigger_TRIGGER_EVENT, "a task finished")
	req := h.requests(1)[0]
	if strings.Contains(systemNotes(req), "Skills:") || slices.Contains(toolNames(req.Tools), agent.LoadSkill) {
		t.Fatalf("event turn %+v", req)
	}
	if h.convs.stored[0].meta.Skills != nil {
		t.Fatal("event turn recorded skills")
	}
}
