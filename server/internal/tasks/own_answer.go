package tasks

import (
	"context"
	"sync"
)

// ownAnswers is what an event turn answered of its own task (Ruling M5b-R6):
// per short id, the seq of the event the answer's resume made. The turn's
// notice stands under that event, so the resume does not leave it stale.
type ownAnswers struct {
	mu   sync.Mutex
	seqs map[string]int
}

type ownAnswersKey struct{}

// withOwnAnswers has the answers a call under ctx makes recorded.
func withOwnAnswers(ctx context.Context) (context.Context, *ownAnswers) {
	a := &ownAnswers{seqs: map[string]int{}}
	return context.WithValue(ctx, ownAnswersKey{}, a), a
}

func ownAnswersOf(ctx context.Context) *ownAnswers {
	a, _ := ctx.Value(ownAnswersKey{}).(*ownAnswers)
	return a
}

func (a *ownAnswers) note(shortID string, seq int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seqs[shortID] = max(a.seqs[shortID], seq)
}

func (a *ownAnswers) seq(shortID string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.seqs[shortID]
	return s, ok
}
