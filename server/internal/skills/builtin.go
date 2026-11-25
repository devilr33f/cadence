package skills

import (
	"embed"
	"errors"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

//go:embed builtin/*.md
var builtinFiles embed.FS

// Builtins are the skills the server ships, by name. A file that does not
// parse is a programming error and panics at start.
func Builtins() []agent.Skill {
	entries, err := builtinFiles.ReadDir("builtin")
	if err != nil {
		panic("skills: builtin directory: " + err.Error())
	}
	var out []agent.Skill
	for _, e := range entries {
		b, err := builtinFiles.ReadFile("builtin/" + e.Name())
		if err != nil {
			panic("skills: " + e.Name() + ": " + err.Error())
		}
		s, err := parse(string(b))
		if err != nil {
			panic("skills: " + e.Name() + ": " + err.Error())
		}
		if path, ok := check(s); !ok {
			panic("skills: " + e.Name() + ": invalid " + path)
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b agent.Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// parse reads a skill file: a front matter of name, description, tools and
// triggers (comma lists), then its body.
func parse(text string) (agent.Skill, error) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return agent.Skill{}, errors.New("no front matter")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return agent.Skill{}, errors.New("front matter not closed")
	}
	s := agent.Skill{Body: strings.TrimSpace(body)}
	for line := range strings.SplitSeq(head, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return agent.Skill{}, errors.New("bad front matter line")
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		case "title.ru":
			s.TitleRU = val
		case "title.en":
			s.TitleEN = val
		case "tools":
			s.Tools = list(val)
		case "triggers":
			s.Triggers = list(val)
		default:
			return agent.Skill{}, errors.New("unknown front matter key")
		}
	}
	return s, nil
}

func list(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
