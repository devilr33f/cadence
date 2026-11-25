package memory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func newReembedder(r *rig, emb providers.Embedder) *memory.Reembedder {
	return memory.NewReembedder(memory.ReembedDeps{
		Store: r.s, Embedder: func(context.Context) (providers.Embedder, error) {
			if emb == nil {
				return nil, providers.ErrNotFound
			}
			return emb, nil
		},
	})
}

func TestReembedStaleModelRows(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	stale := r.add(memory.NewMemory{Text: "old", Vector: []float32{1, 0, 0}, EmbedModel: "embed-old"})
	none := r.add(memory.NewMemory{Text: "none", Scope: memory.ScopeDevice, DeviceID: r.d1})
	pending := r.add(memory.NewMemory{Text: "pending", Status: memory.StatusPending})
	cur := r.add(memory.NewMemory{Text: "cur", Vector: []float32{0, 1, 0}, EmbedModel: "embed-new"})
	emb := &embedFake{model: "embed-new", vec: []float32{0, 0, 1}}
	n, err := newReembedder(r, emb).Once(ctx)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	for _, m := range []memory.Memory{stale, none, pending} {
		got, _ := r.s.Get(ctx, m.ID)
		if got.EmbedModel != "embed-new" || !got.HasEmbedding {
			t.Fatalf("%s: %+v", m.Text, got)
		}
	}
	if got, _ := r.s.Get(ctx, cur.ID); !got.UpdatedAt.Equal(cur.UpdatedAt) {
		t.Fatal("a current row was rewritten")
	}
	if left, _ := r.s.StaleModel(ctx, "embed-new", 10); len(left) != 0 {
		t.Fatalf("%d stale left", len(left))
	}
}

func TestReembedBatchesOf32(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 70; i++ {
		r.add(memory.NewMemory{Text: "m"})
	}
	emb := &embedFake{model: "embed-new", vec: []float32{0, 0, 1}}
	n, err := newReembedder(r, emb).Once(context.Background())
	if err != nil || n != 70 {
		t.Fatalf("%d %v", n, err)
	}
	var sizes []int
	for _, in := range emb.inputs {
		sizes = append(sizes, len(in))
	}
	if len(sizes) != 3 || sizes[0] != 32 || sizes[1] != 32 || sizes[2] != 6 {
		t.Fatalf("batches %v", sizes)
	}
}

func TestReembedSkipsWithoutInstanceAndOnErrors(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Text: "m"})
	if n, err := newReembedder(r, nil).Once(ctx); n != 0 || err != nil {
		t.Fatalf("no instance: %d %v", n, err)
	}
	failing := &embedFake{model: "embed-new", err: errors.New(providers.CodeEmbedFailed)}
	if n, err := newReembedder(r, failing).Once(ctx); n != 0 || err == nil {
		t.Fatalf("embed error: %d %v", n, err)
	}
	if got, _ := r.s.Get(ctx, m.ID); got.HasEmbedding {
		t.Fatal("embedded despite the error")
	}
	// a vector the store refuses stops the pass instead of spinning on it
	bad := &embedFake{model: "embed-new", vec: []float32{0, 0, 0}}
	if n, err := newReembedder(r, bad).Once(ctx); n != 0 || err == nil {
		t.Fatalf("zero vector: %d %v", n, err)
	}
	if bad.calls() != 1 {
		t.Fatalf("%d batches tried", bad.calls())
	}
}

// one pass at a time: a second caller while one runs does nothing
func TestReembedOneAtATime(t *testing.T) {
	r := newRig(t)
	r.add(memory.NewMemory{Text: "m"})
	var running, peak, calls atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	emb := blockingEmbed{running: &running, peak: &peak, calls: &calls, entered: entered, release: release}
	job := newReembedder(r, emb)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = job.Once(context.Background())
		}()
	}
	<-entered
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if peak.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("peak %d calls %d", peak.Load(), calls.Load())
	}
}

type blockingEmbed struct {
	running, peak, calls *atomic.Int32
	entered              chan struct{}
	release              chan struct{}
}

func (blockingEmbed) Model() string { return "embed-new" }

func (b blockingEmbed) Embed(_ context.Context, in []string) ([][]float32, error) {
	n := b.running.Add(1)
	defer b.running.Add(-1)
	b.calls.Add(1)
	if n > b.peak.Load() {
		b.peak.Store(n)
	}
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	out := make([][]float32, len(in))
	for i := range in {
		out[i] = []float32{0, 0, 1}
	}
	return out, nil
}

func TestReembedLoopRunsEveryInterval(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Text: "m"})
	emb := &embedFake{model: "embed-new", vec: []float32{0, 0, 1}}
	job := memory.NewReembedder(memory.ReembedDeps{
		Store: r.s, Every: 50 * time.Millisecond,
		Embedder: func(context.Context) (providers.Embedder, error) { return emb, nil },
	})
	stop := job.Run(ctx)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, _ := r.s.Get(ctx, m.ID); got.HasEmbedding {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("never re-embedded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// an edit between the read and the write must not get the old text's vector
func TestReembedSkipsMemoryEditedMeanwhile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := r.add(memory.NewMemory{Text: "old text"})
	edit := func() {
		txt := "new text"
		if _, err := r.s.Update(ctx, m.ID, memory.Patch{Text: &txt}); err != nil {
			t.Error(err)
		}
	}
	emb := &editingEmbed{embedFake: embedFake{model: "embed-new", vec: []float32{0, 0, 1}}, before: edit}
	job := newReembedder(r, emb)
	if n, err := job.Once(ctx); n != 0 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if got, _ := r.s.Get(ctx, m.ID); got.HasEmbedding || got.Text != "new text" {
		t.Fatalf("%+v", got)
	}
	emb.before = nil
	if n, err := job.Once(ctx); n != 1 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if got, _ := r.s.Get(ctx, m.ID); !got.HasEmbedding || got.EmbedModel != "embed-new" {
		t.Fatalf("%+v", got)
	}
}

type editingEmbed struct {
	embedFake
	before func()
}

func (e *editingEmbed) Embed(ctx context.Context, in []string) ([][]float32, error) {
	if e.before != nil {
		e.before()
	}
	return e.embedFake.Embed(ctx, in)
}
