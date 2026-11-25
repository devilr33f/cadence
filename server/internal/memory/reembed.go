package memory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	reembedBatch = providers.MaxEmbedInputs
	// ReembedEvery is the loop's default interval.
	ReembedEvery   = 60 * time.Second
	reembedTimeout = 30 * time.Second
)

type ReembedDeps struct {
	Store *Store
	// Embedder is the instance memories should be embedded with, or an error
	// while there is none; the pass then does nothing.
	Embedder func(ctx context.Context) (providers.Embedder, error)
	Log      *slog.Logger
	// Every is the loop's interval; zero is ReembedEvery.
	Every time.Duration
}

// Reembedder gives memories a vector of the current embed model (M5a-D2):
// those embedded with another model, or with none, until then match by
// being pinned only.
type Reembedder struct {
	d  ReembedDeps
	mu sync.Mutex
}

func NewReembedder(d ReembedDeps) *Reembedder {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Every <= 0 {
		d.Every = ReembedEvery
	}
	return &Reembedder{d: d}
}

// Run passes at once and then every interval until ctx ends; stop waits for the loop.
func (j *Reembedder) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pass := func() {
			n, err := j.Once(ctx)
			switch {
			case ctx.Err() != nil:
			case err != nil:
				j.d.Log.Warn("memory_reembed_failed", "code", reembedCode(err), "count", n)
			case n > 0:
				j.d.Log.Info("memory_reembedded", "count", n)
			}
		}
		pass()
		t := time.NewTicker(j.d.Every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pass()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Once re-embeds every stale memory in batches and returns how many it did. A
// pass already running makes this one return at once, and a batch that gets
// nowhere ends the pass, so a row the store refuses cannot hold it in a loop.
func (j *Reembedder) Once(ctx context.Context) (int, error) {
	if !j.mu.TryLock() {
		return 0, nil
	}
	defer j.mu.Unlock()
	emb, err := j.d.Embedder(ctx)
	if err != nil || emb == nil {
		return 0, nil
	}
	model := emb.Model()
	total := 0
	for ctx.Err() == nil {
		stale, err := j.d.Store.StaleModel(ctx, model, reembedBatch)
		if err != nil || len(stale) == 0 {
			return total, err
		}
		n, err := j.batch(ctx, emb, model, stale)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
	return total, ctx.Err()
}

// batch embeds the memories with text and stores what the embedder answered.
func (j *Reembedder) batch(ctx context.Context, emb providers.Embedder, model string, ms []Memory) (int, error) {
	var texts []string
	var rows []Memory
	for _, m := range ms {
		if strings.TrimSpace(m.Text) != "" {
			texts = append(texts, m.Text)
			rows = append(rows, m)
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ectx, cancel := context.WithTimeout(ctx, reembedTimeout)
	defer cancel()
	vecs, err := emb.Embed(ectx, texts)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(rows) {
		return 0, errors.New(providers.CodeEmbedFailed)
	}
	n := 0
	var first error
	for i, m := range rows {
		err := j.d.Store.SetEmbeddingIfText(ctx, m.ID, m.Text, vecs[i], model)
		switch {
		case err == nil:
			n++
		case errors.Is(err, ErrNotFound):
			// deleted or edited meanwhile: an edit drops the vector, so the next pass embeds the new text
		case first == nil:
			first = err
		}
	}
	if n == 0 {
		return 0, first
	}
	return n, nil
}

// reembedCode is a log code for an error: the providers' own codes, else a
// fixed word, never the error's text.
func reembedCode(err error) string {
	switch {
	case errors.Is(err, ErrDimension):
		return "dimension"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, context.DeadlineExceeded):
		return providers.CodeEmbedTimeout
	}
	switch c := err.Error(); c {
	case providers.CodeEmbedUnavailable, providers.CodeEmbedFailed, providers.CodeEmbedTimeout:
		return c
	}
	return "error"
}
