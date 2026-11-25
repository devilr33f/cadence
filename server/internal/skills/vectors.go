package skills

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
)

const (
	embedTimeout = 20 * time.Second
	// failedWait is how long a failed fill waits before a turn tries again
	failedWait = time.Minute
)

// vectors are the skills' embeddings, per model and per skill text, kept in
// memory: there are few skills, and a model change or an edit makes them
// again. A turn never waits for them: a miss starts a fill for later turns.
type vectors struct {
	embeds memory.Embeds
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	byKey   map[vecKey][]float32
	filling map[string]bool
	failed  map[string]time.Time
}

type vecKey struct {
	model string
	text  [32]byte
}

func newVectors(e memory.Embeds, log *slog.Logger) *vectors {
	return &vectors{embeds: e, log: log, now: time.Now, byKey: map[vecKey][]float32{}, filling: map[string]bool{},
		failed: map[string]time.Time{}}
}

// embedText is what a skill's vector is made of: what it is about, not its
// instructions
func embedText(s agent.Skill) string {
	return s.Name + ": " + s.Description + " " + strings.Join(s.Triggers, ", ")
}

// lookup is the vectors of model at hand for skills, by name; a miss starts
// a fill with the embed instance, unless one runs or failed just now
func (v *vectors) lookup(embed, model string, skills []agent.Skill) map[string][]float32 {
	if v == nil || v.embeds == nil {
		return nil
	}
	out := map[string][]float32{}
	var missing []agent.Skill
	v.mu.Lock()
	for _, s := range skills {
		if vec, ok := v.byKey[vecKey{model, sha256.Sum256([]byte(embedText(s)))}]; ok {
			out[s.Name] = vec
		} else {
			missing = append(missing, s)
		}
	}
	start := len(missing) > 0 && embed != "" && !v.filling[model] && v.now().Sub(v.failed[model]) >= failedWait
	if start {
		v.filling[model] = true
	}
	v.mu.Unlock()
	if start {
		go v.fill(embed, model, missing)
	}
	return out
}

func (v *vectors) fill(embed, model string, skills []agent.Skill) {
	ok := false
	defer func() {
		// the value can carry text, so it is dropped
		_ = recover()
		v.mu.Lock()
		delete(v.filling, model)
		if !ok {
			v.failed[model] = v.now()
		}
		v.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), embedTimeout)
	defer cancel()
	e, err := v.embeds.Embed(ctx, embed)
	if err != nil {
		v.log.Warn("skills_embed_failed", "code", "instance")
		return
	}
	// a vector of another model than the input's compares to nothing
	if e.Model() != model {
		v.log.Warn("skills_embed_failed", "code", "model_mismatch")
		return
	}
	texts := make([]string, len(skills))
	for i, s := range skills {
		texts[i] = embedText(s)
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil || len(vecs) != len(skills) {
		v.log.Warn("skills_embed_failed", "code", "embed")
		return
	}
	v.mu.Lock()
	for i := range skills {
		if len(vecs[i]) > 0 {
			v.byKey[vecKey{model, sha256.Sum256([]byte(texts[i]))}] = vecs[i]
		}
	}
	v.mu.Unlock()
	ok = true
	v.log.Info("skills_embedded", "count", len(skills))
}

// cosine is the vectors' cosine similarity, 0 for ones of other lengths
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
