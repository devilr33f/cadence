package mcp

import (
	"context"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type session struct {
	cs  *sdk.ClientSession
	rt  *pinned
	era string
	// ctx is cancelled by Close alone: it aborts the session's requests
	ctx    context.Context
	cancel context.CancelFunc
	// inflight holds the requests running on the session; retiring waits for them
	inflight sync.WaitGroup
	// broken closes once the session takes no new request; code and lost say why
	broken chan struct{}
	once   sync.Once
	code   string
	lost   bool
	// retired is guarded by Client.mu
	retired bool
	closed  chan struct{}
}

func (s *session) isBroken() bool {
	select {
	case <-s.broken:
		return true
	default:
		return false
	}
}

// close ends the SDK session; a legacy session's DELETE gets closeGrace
// before the transport fails it.
func (s *session) close() {
	grace := time.AfterFunc(closeGrace, s.rt.kill)
	_ = s.cs.Close()
	grace.Stop()
	s.rt.kill()
	s.cancel()
}

// acquire holds the current session for one request; release with
// inflight.Done.
func (c *Client) acquire() (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.cur
	if s == nil || s.isBroken() {
		return nil, &Error{Code: CodeUnavailable, unsent: true}
	}
	s.inflight.Add(1)
	return s, nil
}

// breakOn takes s out of service for new requests when err says it cannot
// serve them; the supervisor then retires it and reconnects.
func (c *Client) breakOn(s *session, err error) {
	e, ok := breaking(err)
	if !ok {
		return
	}
	s.once.Do(func() {
		s.code, s.lost = e.Code, e.lost
		close(s.broken)
	})
	c.mu.Lock()
	if c.cur == s {
		c.status.State, c.status.LastCode, c.status.ConnectedAt = StateUnavailable, e.Code, time.Time{}
	}
	c.mu.Unlock()
}

// retire takes s out of service: new requests no longer reach it, the ones
// in flight finish under their own deadlines, then it closes.
func (c *Client) retire(s *session) {
	c.mu.Lock()
	if c.cur == s {
		c.cur = nil
		c.status.State, c.status.ConnectedAt = StateUnavailable, time.Time{}
	}
	if s.retired {
		c.mu.Unlock()
		return
	}
	s.retired = true
	c.mu.Unlock()
	s.once.Do(func() { close(s.broken) })
	go func() {
		s.inflight.Wait()
		s.close()
		c.mu.Lock()
		delete(c.sessions, s)
		c.mu.Unlock()
		close(s.closed)
	}()
}
