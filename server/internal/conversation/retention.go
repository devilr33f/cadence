package conversation

import (
	"context"
	"log/slog"
	"time"
)

const Retention = 14 * 24 * time.Hour

// RunRetention prunes at start and then every interval until ctx ends; stop waits for the loop.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		n, err := s.Prune(ctx, time.Now().Add(-Retention))
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("conversation_prune_failed")
		default:
			log.Info("conversation_pruned", "count", n.Messages, "turns", n.Turns)
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
