package memory

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// EmbedBudget bounds the embedding of a turn's input; past it the turn
	// goes on with the pinned memories only.
	EmbedBudget = 400 * time.Millisecond

	nearestLimit  = 8
	minSimilarity = 0.3
	// recallChars bounds the text of all recalled memories together.
	recallChars = 1500

	storeBudget   = 400 * time.Millisecond
	hitTimeout    = 5 * time.Second
	unknownLogGap = time.Hour
)

// why a recall used the pinned memories only
const (
	SkipNoInput         = "no_input"
	SkipNoInstance      = "no_instance"
	SkipUnknownInstance = "unknown_instance"
	SkipTimeout         = "timeout"
	SkipCanceled        = "canceled"
	SkipDimension       = "dimension"
	SkipModelMismatch   = "model_mismatch"
	SkipError           = "error"
	SkipStore           = "store"
)

// Embeds resolves an embed instance by name.
type Embeds interface {
	Embed(ctx context.Context, name string) (providers.Embedder, error)
}

type RecallerDeps struct {
	Store  *Store
	Embeds Embeds
	Log    *slog.Logger
	Now    func() time.Time
	// GlobalEmbed names the embed instance of the global settings, empty when
	// unset; the re-embed job serves memories with that one only.
	GlobalEmbed func(ctx context.Context) (string, error)
	// Budget bounds the input's embedding; zero is EmbedBudget.
	Budget time.Duration
}

// Recaller picks the memories a turn reads (M5a-D3).
type Recaller struct {
	d RecallerDeps

	mu      sync.Mutex
	unknown map[string]time.Time
}

func NewRecaller(d RecallerDeps) *Recaller {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Budget <= 0 {
		d.Budget = EmbedBudget
	}
	return &Recaller{d: d, unknown: map[string]time.Time{}}
}

// Query is one recall. Embed names the embed instance, empty for none; Scopes
// are the allowed memory scopes ("global", "device"), nil for both.
type Query struct {
	DeviceID string
	Input    string
	Embed    string
	Scopes   []string
}

// Recalled holds the memories to show, pinned first, then by similarity.
// Skipped says why only the pinned ones were looked at, empty when the
// input was matched.
type Recalled struct {
	Memories []Memory
	Pinned   int
	Skipped  string
	// Vector is the input's embedding and Model the model that made it, nil
	// and "" when none was made: the turn's skill pick reads it, so the input
	// is embedded once a turn
	Vector []float32
	Model  string
}

// Recall never fails: whatever goes wrong narrows the result, so a turn
// never waits on or breaks over memory.
func (r *Recaller) Recall(ctx context.Context, q Query) Recalled {
	sc := Scopes{Global: q.Scopes == nil || slices.Contains(q.Scopes, ScopeGlobal)}
	if q.Scopes == nil || slices.Contains(q.Scopes, ScopeDevice) {
		sc.DeviceID = q.DeviceID
	}
	if !sc.Global && sc.DeviceID == "" {
		return Recalled{}
	}
	// the pinned read runs beside the embedding, so a caller's deadline that
	// cuts the embedding short still finds the pinned memories read
	type pinnedRead struct {
		ms  []Memory
		err error
	}
	pinnedCh := make(chan pinnedRead, 1)
	go func() {
		defer func() {
			// the value can carry memory text, so it is dropped
			if recover() != nil {
				pinnedCh <- pinnedRead{err: errPanicked}
			}
		}()
		sctx, cancel := context.WithTimeout(ctx, storeBudget)
		defer cancel()
		ms, err := r.d.Store.Pinned(sctx, sc)
		pinnedCh <- pinnedRead{ms, err}
	}()
	vec, model, reason := r.embed(ctx, q)
	read := <-pinnedCh
	pinned := read.ms
	if read.err != nil {
		r.skipped(q, SkipStore)
		return Recalled{Skipped: SkipStore}
	}
	res := Recalled{Pinned: len(pinned), Vector: vec, Model: model}
	picked := slices.Clone(pinned)
	if reason != "" {
		res.Skipped = reason
	} else {
		sctx, cancel := context.WithTimeout(ctx, storeBudget)
		near, err := r.d.Store.Nearest(sctx, vec, model, sc, nearestLimit+len(pinned), minSimilarity)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			res.Skipped = SkipTimeout
		case errors.Is(err, ErrDimension):
			res.Skipped = SkipDimension
		case err != nil:
			res.Skipped = SkipStore
		}
		if res.Skipped != "" {
			r.skipped(q, res.Skipped)
		}
		n := 0
		for _, s := range near {
			if n == nearestLimit {
				break
			}
			if slices.ContainsFunc(pinned, func(p Memory) bool { return p.ID == s.ID }) {
				continue
			}
			picked = append(picked, s.Memory)
			n++
		}
	}
	res.Memories, res.Pinned = trim(picked, len(pinned))
	r.hit(res.Memories)
	return res
}

// embed returns the input's vector and the model that made it, or why none.
func (r *Recaller) embed(ctx context.Context, q Query) ([]float32, string, string) {
	switch {
	case isBlank(q.Input):
		return nil, "", SkipNoInput
	case q.Embed == "":
		return nil, "", SkipNoInstance
	}
	type answer struct {
		vec   []float32
		model string
		err   error
	}
	ectx, cancel := context.WithTimeout(ctx, r.d.Budget)
	defer cancel()
	done := make(chan answer, 1)
	go func() {
		defer func() {
			// the value can carry text, so it is dropped
			if recover() != nil {
				done <- answer{err: errPanicked}
			}
		}()
		e, err := r.d.Embeds.Embed(ectx, q.Embed)
		if err != nil {
			done <- answer{err: err}
			return
		}
		if err := r.checkGlobal(ectx, q, e); err != nil {
			done <- answer{err: err}
			return
		}
		vecs, err := e.Embed(ectx, []string{q.Input})
		if err == nil && (len(vecs) != 1 || len(vecs[0]) == 0) {
			err = errors.New(providers.CodeEmbedFailed)
		}
		if err != nil {
			done <- answer{err: err}
			return
		}
		done <- answer{vec: vecs[0], model: e.Model()}
	}()
	var a answer
	select {
	case a = <-done:
	case <-ectx.Done():
		a.err = ectx.Err()
	}
	if a.err == nil {
		return a.vec, a.model, ""
	}
	reason := embedReason(ctx, a.err)
	switch reason {
	case SkipUnknownInstance, SkipModelMismatch:
		r.logOnce(reason+":"+q.Embed, reason, q)
	default:
		r.skipped(q, reason)
	}
	return nil, "", reason
}

func embedReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return SkipCanceled
	case errors.Is(err, errModelMismatch):
		return SkipModelMismatch
	case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrWrongKind), errors.Is(err, providers.ErrUnknownModule):
		return SkipUnknownInstance
	case errors.Is(err, context.DeadlineExceeded), err.Error() == providers.CodeEmbedTimeout:
		return SkipTimeout
	}
	return SkipError
}

func (r *Recaller) skipped(q Query, reason string) {
	r.d.Log.Info("memory_embed_skipped", "device_id", q.DeviceID, "reason", reason)
}

// hit counts the recall on the memories without holding the turn.
func (r *Recaller) hit(ms []Memory) {
	if len(ms) == 0 {
		return
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	go func() {
		defer func() {
			if recover() != nil {
				r.d.Log.Warn("memory_hit_failed", "count", len(ids))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), hitTimeout)
		defer cancel()
		if err := r.d.Store.Hit(ctx, ids); err != nil {
			r.d.Log.Warn("memory_hit_failed", "count", len(ids))
		}
	}()
}

// trim keeps memories, in order, while their text fits recallChars; one that
// does not fit is passed over for a shorter one behind it. The first
// pinnedN are the pinned ones; it returns how many of them stayed.
func trim(ms []Memory, pinnedN int) ([]Memory, int) {
	var out []Memory
	used, pinned := 0, 0
	for i, m := range ms {
		n := utf8.RuneCountInString(m.Text)
		if used+n > recallChars {
			continue
		}
		used += n
		out = append(out, m)
		if i < pinnedN {
			pinned++
		}
	}
	return out, pinned
}

func isBlank(s string) bool {
	for _, c := range s {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

var (
	errModelMismatch = errors.New("memory: embed model differs from the global one")
	// errPanicked stands for a recovered panic, whose value is dropped
	errPanicked = errors.New("memory: recall panicked")
)

// checkGlobal flags what keeps a device's recall from ever matching: the
// re-embed job gives memories the model of the global `embed` instance only,
// so a device on another model finds none of them, and one that sets `embed`
// while the global one is unset gets no re-embedding at all.
func (r *Recaller) checkGlobal(ctx context.Context, q Query, e providers.Embedder) error {
	if r.d.GlobalEmbed == nil {
		return nil
	}
	name, err := r.d.GlobalEmbed(ctx)
	if err != nil || name == q.Embed {
		return nil
	}
	if name == "" {
		r.logOnce("global_embed_unset:"+q.Embed, "global_embed_unset", q)
		return nil
	}
	g, err := r.d.Embeds.Embed(ctx, name)
	if err == nil && g.Model() != e.Model() {
		return errModelMismatch
	}
	return nil
}

// logOnce logs a skip reason that every turn of a device would repeat (a name
// settings do not validate, a model that never matches): once per key per hour.
func (r *Recaller) logOnce(key, reason string, q Query) {
	now := r.d.Now()
	r.mu.Lock()
	last, seen := r.unknown[key]
	if seen && now.Sub(last) < unknownLogGap {
		r.mu.Unlock()
		return
	}
	r.unknown[key] = now
	r.mu.Unlock()
	r.skipped(q, reason)
}
