package t3code

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// streamMsg is a stream's chunk of values, or its end (exit set).
type streamMsg struct {
	values []json.RawMessage
	exit   *ExitValue
}

type callResult struct {
	value json.RawMessage
	err   error
}

// conn is one WebSocket connection: requests matched to their answers by id,
// a read loop and a pinger. It never reconnects; the Client does.
type conn struct {
	ws  *websocket.Conn
	log *slog.Logger
	t   timings

	mu      sync.Mutex
	next    int64
	calls   map[int64]chan callResult
	streams map[int64]chan streamMsg
	code    string

	lastRead atomic.Int64
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func newConn(ws *websocket.Conn, log *slog.Logger, t timings) *conn {
	c := &conn{ws: ws, log: log, t: t, next: 1, calls: map[int64]chan callResult{}, streams: map[int64]chan streamMsg{}, done: make(chan struct{})}
	c.lastRead.Store(time.Now().UnixNano())
	return c
}

func (c *conn) start() {
	c.wg.Add(2)
	go c.readLoop()
	go c.pinger()
}

// close ends the connection once with code; later codes are dropped.
func (c *conn) close(code string) {
	c.once.Do(func() {
		c.mu.Lock()
		c.code = code
		calls := c.calls
		c.calls = map[int64]chan callResult{}
		c.mu.Unlock()
		for _, ch := range calls {
			ch <- callResult{err: fail(CodeUnavailable)}
		}
		close(c.done)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}

func (c *conn) ended() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *conn) closedCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func (c *conn) write(ctx context.Context, f Frame) error {
	b, err := EncodeFrame(f)
	if err != nil {
		return fail(CodeBadResponse)
	}
	wctx, cancel := context.WithTimeout(ctx, c.t.write)
	defer cancel()
	if err := c.ws.Write(wctx, websocket.MessageText, b); err != nil {
		c.close(CodeUnavailable)
		return fail(CodeUnavailable)
	}
	return nil
}

func (c *conn) id() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.next
	c.next++
	return id
}

// call sends one request and waits for its Exit.
func (c *conn) call(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	id := c.id()
	ch := make(chan callResult, 1)
	c.mu.Lock()
	if c.code != "" {
		c.mu.Unlock()
		return nil, fail(CodeUnavailable)
	}
	c.calls[id] = ch
	c.mu.Unlock()
	if err := c.write(ctx, requestFrame(id, method, payload)); err != nil {
		c.drop(id)
		return nil, err
	}
	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		if c.drop(id) {
			_ = c.write(context.Background(), interruptFrame(id))
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fail(CodeTimeout)
		}
		return nil, ctx.Err()
	}
}

// drop forgets a call and reports whether it was still waiting.
func (c *conn) drop(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.calls[id]
	delete(c.calls, id)
	return ok
}

// openStream sends a streaming request; its chunks and end arrive on the
// channel, which is never closed: watch c.done.
func (c *conn) openStream(ctx context.Context, method string, payload json.RawMessage) (int64, <-chan streamMsg, error) {
	id := c.id()
	ch := make(chan streamMsg, c.t.streamBuffer)
	c.mu.Lock()
	if c.code != "" {
		c.mu.Unlock()
		return 0, nil, fail(CodeUnavailable)
	}
	c.streams[id] = ch
	c.mu.Unlock()
	if err := c.write(ctx, requestFrame(id, method, payload)); err != nil {
		c.endStream(id)
		return 0, nil, err
	}
	return id, ch, nil
}

func (c *conn) endStream(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.streams[id]
	delete(c.streams, id)
	return ok
}

// interrupt stops a stream the caller no longer reads.
func (c *conn) interrupt(id int64) {
	if c.endStream(id) {
		_ = c.write(context.Background(), interruptFrame(id))
	}
}

func (c *conn) ack(id int64) { _ = c.write(context.Background(), ackFrame(id)) }

func (c *conn) readLoop() {
	defer c.wg.Done()
	ctx := context.Background()
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			c.close(CodeUnavailable)
			return
		}
		c.lastRead.Store(time.Now().UnixNano())
		if typ != websocket.MessageText {
			continue
		}
		frames, err := DecodeFrames(b)
		if err != nil {
			c.log.Warn("t3code_bad_frame")
			c.close(CodeBadResponse)
			return
		}
		for _, f := range frames {
			if !c.handle(f) {
				return
			}
		}
	}
}

// handle routes one frame; false ends the read loop.
func (c *conn) handle(f Frame) bool {
	switch f.Tag {
	case tagChunk:
		id, ok := frameID(f.RequestID)
		if !ok {
			return true
		}
		c.mu.Lock()
		ch := c.streams[id]
		c.mu.Unlock()
		if ch == nil || len(f.Values) == 0 {
			return true
		}
		select {
		case ch <- streamMsg{values: f.Values}:
		case <-c.done:
			return false
		}
	case tagExit:
		id, ok := frameID(f.RequestID)
		if !ok || f.Exit == nil {
			return true
		}
		c.mu.Lock()
		call := c.calls[id]
		delete(c.calls, id)
		st := c.streams[id]
		delete(c.streams, id)
		c.mu.Unlock()
		switch {
		case call != nil:
			call <- exitResult(f.Exit)
		case st != nil:
			select {
			case st <- streamMsg{exit: f.Exit}:
			case <-c.done:
				return false
			}
		}
	case tagClientProtocolError:
		c.mu.Lock()
		var ended []chan callResult
		if f.RequestIDs == nil {
			for id, ch := range c.calls {
				ended = append(ended, ch)
				delete(c.calls, id)
			}
		} else {
			for _, raw := range f.RequestIDs {
				if id, ok := frameID(raw); ok && c.calls[id] != nil {
					ended = append(ended, c.calls[id])
					delete(c.calls, id)
				}
			}
		}
		c.mu.Unlock()
		for _, ch := range ended {
			ch <- callResult{err: fail(CodeBadResponse)}
		}
	case tagDefect:
		// a defect ends every request on the socket: start over on a new one
		c.log.Warn("t3code_defect")
		c.close(CodeBadResponse)
		return false
	}
	// Pong only refreshes lastRead; unknown tags (a server Request) are ignored
	return true
}

func exitResult(e *ExitValue) callResult {
	if e.Tag == "Success" {
		return callResult{value: e.Value}
	}
	return callResult{err: exitError(e)}
}

// pinger sends Ping every pingEvery and closes the connection when nothing
// was read for pingEvery plus pongWait.
func (c *conn) pinger() {
	defer c.wg.Done()
	tick := time.NewTicker(c.t.pingEvery)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
			if time.Since(time.Unix(0, c.lastRead.Load())) > c.t.pingEvery+c.t.pongWait {
				c.log.Warn("t3code_ping_timeout")
				c.close(CodeTimeout)
				return
			}
			_ = c.write(context.Background(), Frame{Tag: tagPing})
		}
	}
}
