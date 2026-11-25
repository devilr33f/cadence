package skills

import (
	"slices"
	"strings"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

const (
	// maxPicked bounds the skills loaded before the first step, and
	// pickedChars their text together: the rest stay one load_skill away
	maxPicked   = 2
	pickedChars = 4500
	// minSimilarity is the cosine an input's embedding must reach with a
	// skill's to load it without a trigger word
	minSimilarity = 0.5
)

type scored struct {
	s    agent.Skill
	kw   int
	sim  float64
	hasV bool
}

// Pick loads the skills whose trigger words the input has, the strongest
// first, then those its embedding is close to; a skill well below the best
// one's trigger score is left to load_skill. Ties go by similarity, then by
// name, so one input picks the same skills while the vectors stay the same.
func (l *Library) Pick(skills []agent.Skill, q agent.SkillQuery) []string {
	heard := words(q.Input)
	if len(heard) == 0 {
		return nil
	}
	var vecs map[string][]float32
	if len(q.Vector) > 0 && q.Model != "" {
		vecs = l.vec.lookup(q.Embed, q.Model, skills)
	}
	var cands []scored
	top := 0
	for _, s := range skills {
		c := scored{s: s, kw: keywordScore(heard, s.Triggers)}
		if v, ok := vecs[s.Name]; ok {
			c.sim, c.hasV = cosine(q.Vector, v), true
		}
		if c.kw > 0 || (c.hasV && c.sim >= minSimilarity) {
			cands = append(cands, c)
			top = max(top, c.kw)
		}
	}
	slices.SortFunc(cands, func(a, b scored) int {
		switch {
		case a.kw != b.kw:
			return b.kw - a.kw
		case a.sim != b.sim:
			if a.sim > b.sim {
				return -1
			}
			return 1
		}
		return strings.Compare(a.s.Name, b.s.Name)
	})
	var out []string
	chars := 0
	for _, c := range cands {
		if len(out) == maxPicked {
			break
		}
		// a weak match beside a strong one ("включи" beside "включи песню")
		if c.kw > 0 && 2*c.kw < top {
			continue
		}
		if c.kw == 0 && top > 0 && len(out) > 0 {
			continue
		}
		n := utf8.RuneCountInString(c.s.Body)
		if chars+n > pickedChars && len(out) > 0 {
			continue
		}
		chars += n
		out = append(out, c.s.Name)
	}
	return out
}
