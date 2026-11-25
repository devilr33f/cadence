package modes

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

const (
	maxModelBytes = 200
	maxAliases    = 10
	maxAliasChars = 40
	// a first-token timeout under this is a typo, not a model
	minFirstTokenMs = 2000
)

// modeOf is the mode a put stores, or the pointer of the first field the
// runtime could not use. The schema has bounded the enums, the budgets and the
// tools; this checks what it cannot: no first-token timeout between 0 and 2 s,
// the model's bytes and characters, and the aliases once trimmed, distinct in
// any case as the agent_mode tool matches them. A language with no aliases is
// left out.
func modeOf(name string, b PutModeRequest) (agent.Mode, string) {
	if len(b.Model) > maxModelBytes || hidden(b.Model) {
		return agent.Mode{}, "/body/model"
	}
	aliases := map[string][]string{}
	for i, list := range b.Aliases.lists() {
		lang := languages[i]
		if len(list) > maxAliases {
			return agent.Mode{}, "/body/aliases/" + lang
		}
		var clean []string
		for j, a := range list {
			a = strings.TrimSpace(a)
			n := utf8.RuneCountInString(a)
			key := agent.AliasKey(a)
			if n == 0 || n > maxAliasChars || hidden(a) || slices.ContainsFunc(clean, func(c string) bool { return agent.AliasKey(c) == key }) {
				return agent.Mode{}, fmt.Sprintf("/body/aliases/%s/%d", lang, j)
			}
			clean = append(clean, a)
		}
		if len(clean) > 0 {
			aliases[lang] = clean
		}
	}
	var maxTokens int
	if b.MaxTokens != nil {
		maxTokens = *b.MaxTokens
	}
	var firstMs int
	if b.FirstTokenTimeoutMs != nil {
		firstMs = *b.FirstTokenTimeoutMs
	}
	if firstMs != 0 && firstMs < minFirstTokenMs {
		return agent.Mode{}, "/body/firstTokenTimeoutMs"
	}
	return agent.Mode{
		Name: name, Model: b.Model, Reasoning: b.Reasoning, MaxSteps: b.MaxSteps,
		MaxDuration: time.Duration(b.MaxSeconds) * time.Second, MaxTokens: maxTokens,
		FirstTokenTimeout: time.Duration(firstMs) * time.Millisecond, Interim: b.Interim, Tools: b.Tools, Aliases: aliases,
	}, ""
}

// hidden reports a control or format character or a line or paragraph
// separator: none shows, and some reverse or break what follows.
func hidden(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) })
}
