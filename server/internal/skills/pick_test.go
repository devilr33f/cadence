package skills_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/skills"
)

func builtinLib() *skills.Library {
	return skills.New(skills.Deps{Builtins: skills.Builtins()})
}

// every built-in parses, is valid and lists in every mode
func TestBuiltinsParse(t *testing.T) {
	var names []string
	for _, s := range skills.Builtins() {
		names = append(names, s.Name)
		if s.Description == "" || s.Body == "" || len(s.Triggers) == 0 || len(s.Tools) == 0 || s.Modes != nil {
			t.Errorf("%s: %+v", s.Name, s)
		}
	}
	if !slices.Equal(names, []string{"home", "lookup", "music", "timers", "tv", "youtube"}) {
		t.Fatalf("built-ins %v", names)
	}
}

// the owner's turns, and the picks they need: Russian word forms, phrases
// outweighing lone words, and nothing for small talk
func TestPickByTriggers(t *testing.T) {
	l := builtinLib()
	all := l.For("fast")
	for input, want := range map[string][]string{
		"Включи свет":                                   {"home"},
		"Алиса выключи кондиционер":                     {"home"},
		"Открой экран для поиска аниме фридом":          {"tv"},
		"Найди аниме фрирен":                            {"tv"},
		"включи песню группы Кино":                      {"music"},
		"поставь таймер на десять минут":                {"timers"},
		"разбуди меня завтра в семь":                    {"timers"},
		"какая погода завтра":                           {"lookup"},
		"What's the weather like":                       {"lookup"},
		"включи следующую серию на телевизоре":          {"tv"},
		"найди последнее видео на канале Markiplier":    {"youtube"},
		"сколько просмотров у нового ролика Markiplier": {"youtube"},
		"как дела": nil,
		"":         nil,
	} {
		if got := l.Pick(all, agent.SkillQuery{Input: input}); !slices.Equal(got, want) {
			t.Errorf("%q: picked %v, want %v", input, got, want)
		}
	}
	// a skill the turn does not list is never picked
	if got := l.Pick(nil, agent.SkillQuery{Input: "Включи свет"}); got != nil {
		t.Errorf("unlisted: %v", got)
	}
}

type embedder struct{ model string }

func (e embedder) Model() string { return e.model }
func (e embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if len(t) >= 3 && t[:3] == "tv:" {
			out[i] = []float32{1, 0}
		} else {
			out[i] = []float32{0, 1}
		}
	}
	return out, nil
}

type embeds struct{ e embedder }

func (s embeds) Embed(context.Context, string) (providers.Embedder, error) { return s.e, nil }

// without a trigger word, the input's embedding loads the closest skill once
// its vectors are made; the first turn does not wait for them
func TestPickBySimilarity(t *testing.T) {
	l := skills.New(skills.Deps{Builtins: skills.Builtins(), Embeds: embeds{embedder{"m1"}}})
	q := agent.SkillQuery{Input: "покажи что-нибудь про волшебницу эльфийку", Vector: []float32{0.9, 0.1}, Model: "m1", Embed: "e1"}
	all := l.For("fast")
	if got := l.Pick(all, q); got != nil {
		t.Fatalf("first turn picked %v before the vectors", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := l.Pick(all, q)
		if slices.Equal(got, []string{"tv"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("picked %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// a vector of another model compares to nothing
	if got := l.Pick(all, agent.SkillQuery{Input: q.Input, Vector: q.Vector, Model: "m2"}); got != nil {
		t.Fatalf("other model picked %v", got)
	}
}
