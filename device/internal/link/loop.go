package link

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

// Handler drives one link at a time. Connected runs first on each new link and
// may Send and Recv; Message then gets every envelope in order, on the reading
// goroutine; Disconnected follows every Connected once the link is closed. The
// ctx both get is done when the link ends, or as soon as Run's ctx is, while
// the link still closes with 1001. Message must return well within
// PingTimeout: pongs are read only between messages; a longer block ends the
// link.
type Handler interface {
	Connected(ctx context.Context, s Session) error
	Message(ctx context.Context, s Session, env *cadencev2.Envelope) error
	Disconnected()
}

// Run keeps the link up until ctx is done, waiting Delay between attempts,
// then closes it with 1001 and gives the server 1 s to answer. A signal on
// d.Redial ends the link or the dial in flight (a link up closes with 1001) or
// the wait, and dials again at once with the backoff started over.
func Run(ctx context.Context, d Dialer, h Handler, log *slog.Logger) error {
	return run(ctx, d, h, log, func(attempt int) time.Duration { return Delay(attempt, rand.Float64) })
}

type outcome struct {
	up     time.Duration
	reason string
	code   websocket.StatusCode // -1 when the link ended without a close frame
	redial bool                 // ended by a signal on Dialer.Redial
}

func run(ctx context.Context, d Dialer, h Handler, log *slog.Logger, delay func(attempt int) time.Duration) error {
	if _, err := d.endpoint(); err != nil {
		return err
	}
	d = d.withDefaults()
	attempt, refused := 0, false
	for {
		o := connectRedial(ctx, d, h, log, &refused)
		if ctx.Err() != nil {
			return nil
		}
		if o.redial {
			attempt = 0
			log.Info("link_down", "reason", "redial")
			continue
		}
		attempt = nextAttempt(attempt, o.up)
		wait := delay(attempt)
		attrs := []any{"attempt", attempt, "delay_ms", wait.Milliseconds(), "reason", o.reason}
		if o.code >= 0 {
			attrs = append(attrs, "code", int(o.code))
		}
		log.Info("link_down", attrs...)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		case <-d.Redial:
			t.Stop()
			attempt = 0
			log.Info("link_redial")
		}
	}
}

// connectRedial is connect, cut short by a signal on d.Redial.
func connectRedial(ctx context.Context, d Dialer, h Handler, log *slog.Logger, refused *bool) outcome {
	if d.Redial == nil {
		return connect(ctx, d, h, log, refused)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	var kicked atomic.Bool
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-d.Redial:
			kicked.Store(true)
			cancel()
		case <-stop:
		}
	})
	o := connect(cctx, d, h, log, refused)
	close(stop)
	watcher.Wait()
	o.redial = kicked.Load() && ctx.Err() == nil
	return o
}

// connect runs one link from dial to close. refused carries the streak of
// refused tokens, so a revoked device logs it once, not at every attempt.
func connect(ctx context.Context, d Dialer, h Handler, log *slog.Logger, refused *bool) outcome {
	s, err := d.dial(ctx)
	switch {
	case errors.Is(err, ErrUnauthorized):
		if !*refused {
			log.Warn("link_unauthorized")
			*refused = true
		}
		return outcome{reason: "unauthorized", code: -1}
	case err != nil:
		return outcome{reason: "dial_failed", code: -1}
	}
	*refused = false
	lctx, cancel := context.WithCancel(ctx)
	// reads and pings outlive ctx: a read whose ctx ends drops the link before the close goes out
	rctx, stopPings := context.WithCancel(context.WithoutCancel(ctx))
	shutDone := make(chan struct{})
	stopShutdown := context.AfterFunc(ctx, func() {
		defer close(shutDone)
		s.shutdown(d.closeWait)
	})
	var pings sync.WaitGroup
	var pingFailed atomic.Bool
	defer func() {
		if !stopShutdown() {
			<-shutDone
		}
		cancel()
		stopPings()
		pings.Wait()
		s.Close()
		h.Disconnected()
	}()
	if err := h.Connected(lctx, s); err != nil {
		return outcome{reason: "handshake_failed", code: websocket.CloseStatus(err)}
	}
	log.Info("link_up")
	pings.Go(func() {
		if s.keepalive(rctx, d.PingInterval, d.PingTimeout) {
			pingFailed.Store(true)
			s.drop()
		}
	})
	start := time.Now()
	end := func(reason string, code websocket.StatusCode) outcome {
		if pingFailed.Load() {
			reason, code = "ping", -1
		}
		return outcome{up: time.Since(start), reason: reason, code: code}
	}
	unknown := 0
	for {
		env, err := s.Recv(rctx)
		switch {
		case errors.Is(err, wire.ErrUnknown):
			// a newer server's message type; logged at 1, 2, 4, … so a stream of them cannot flood the log
			if unknown++; unknown&(unknown-1) == 0 {
				log.Info("link_unknown_message", "count", unknown)
			}
			continue
		case errors.Is(err, wire.ErrInvalid):
			return end("invalid", -1)
		case err != nil:
			return end("closed", websocket.CloseStatus(err))
		}
		if err := h.Message(lctx, s, env); err != nil {
			return end("handler_failed", -1)
		}
	}
}
