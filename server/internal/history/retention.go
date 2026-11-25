package history

import (
	"context"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
)

// RunRetention prunes on the transcripts' schedule and retention window.
func (s *Service) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		n, err := s.q.PrunePlayHistory(ctx, stamp(s.now().Add(-conversation.Retention)))
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("history_prune_failed")
		default:
			log.Info("history_pruned", "count", n)
		}
	}
	go func() {
		defer close(done)
		prune()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	return func() { cancel(); <-done }
}
