package t3code

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Subscription is one stream kept across connections: on every new
// connection it is opened again with afterSequence set to the last item its
// handler took, so the server replays what was missed (or sends a snapshot).
type Subscription struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	cursor int64
	code   string
}

// Close ends the subscription and waits for its handler to return; call it
// from outside the handler.
func (s *Subscription) Close() {
	s.cancel()
	<-s.done
}

// Done is closed once the subscription ended: closed, Run returned, or T3
// refused it (Err says why).
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Err is the code the subscription ended with: "" after Close, else
// unavailable when Run returned, or T3's refusal (refused, forbidden).
func (s *Subscription) Err() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Cursor is the sequence of the last item handled.
func (s *Subscription) Cursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// item is one decoded stream value: its resume cursor (0 keeps the old one)
// and whether it decoded at all.
type decodeFn func(json.RawMessage) (cursor int64, ok bool)

// subscribe runs the stream method with the payload payloadAt builds for a
// cursor (0 for a fresh start), feeding each value to handle in order and
// acking each chunk once handled.
func (c *Client) subscribe(method string, payloadAt func(cursor int64) any, handle decodeFn) *Subscription {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Subscription{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		c.runSubscription(ctx, s, method, payloadAt, handle)
	}()
	return s
}

func (c *Client) runSubscription(ctx context.Context, s *Subscription, method string, payloadAt func(int64) any, handle decodeFn) {
	delay := c.t.backoffMin
	for {
		cn, changed, stopped := c.current()
		if stopped {
			s.end(CodeUnavailable)
			return
		}
		if cn == nil || cn.ended() {
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
			continue
		}
		code, retry := c.streamOnce(ctx, cn, s, method, payloadAt, handle)
		if ctx.Err() != nil {
			return
		}
		if !retry {
			s.end(code)
			return
		}
		if code == "" {
			// the connection ended: the next one resumes at once
			delay = c.t.backoffMin
			continue
		}
		c.log.Info("t3code_stream_retry", "method", method, "code", code, "delay_ms", delay.Milliseconds())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(2*delay, c.t.backoffMax)
	}
}

// streamOnce runs the stream on one connection until it, the connection or
// ctx ends. retry false means T3 refused it for good; code "" with retry
// means the connection went away.
func (c *Client) streamOnce(ctx context.Context, cn *conn, s *Subscription, method string, payloadAt func(int64) any, handle decodeFn) (string, bool) {
	p, err := json.Marshal(payloadAt(s.Cursor()))
	if err != nil {
		return CodeBadResponse, false
	}
	id, msgs, err := cn.openStream(ctx, method, p)
	if err != nil {
		return "", true
	}
	for {
		select {
		case <-ctx.Done():
			cn.interrupt(id)
			return "", true
		case <-cn.done:
			return "", true
		case m := <-msgs:
			if m.exit != nil {
				return streamExit(m.exit)
			}
			for _, v := range m.values {
				var cur int64
				var ok bool
				if providers.Guard(c.log, "t3code", CodeBadResponse, func() { cur, ok = handle(v) }) {
					// a handler that panicked took the item: move past it
					ok = true
				}
				if !ok {
					c.log.Warn("t3code_bad_item", "method", method)
					continue
				}
				if cur > 0 {
					s.mu.Lock()
					s.cursor = cur
					s.mu.Unlock()
				}
			}
			cn.ack(id)
		}
	}
}

// streamExit: a stream that ended or was interrupted is opened again after
// a backoff; a typed refusal (a thread T3 does not have, a missing scope)
// ends the subscription.
func streamExit(e *ExitValue) (string, bool) {
	if e.Tag == "Success" {
		return CodeUnavailable, true
	}
	code := ErrorCode(exitError(e))
	if code == CodeRefused || code == CodeForbidden {
		return code, false
	}
	return code, true
}

func (s *Subscription) end(code string) {
	s.mu.Lock()
	s.code = code
	s.mu.Unlock()
}
