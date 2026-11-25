package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	fws "github.com/fasthttp/websocket"
	"github.com/gofiber/contrib/v3/websocket"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const (
	queueSize = 256
	// a peer that stops reading fails the write instead of parking the writer
	writeWait = 10 * time.Second
	closeWait = time.Second
)

// Conn is one device link as the handlers see it.
type Conn interface {
	DeviceID() string
	Send(env *cadencev2.Envelope) error
}

type conn struct {
	id      string
	ws      *websocket.Conn
	ping    time.Duration // the writer pings this often past the welcome
	idle    time.Duration // a frame or pong extends the read deadline by this
	welcome chan []byte   // the writer's first frame, nil for none
	queue   chan []byte
	// held to move the read deadline, so an extend never undoes sendClose's
	deadline sync.Mutex
	// done once the link closes: the handlers' ctx and the writer's stop
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	code   int // with reason, set once before ctx is done
	reason string
	wrote  chan struct{} // closed when the writer returns
	ended  chan struct{} // closed when the reader returns
	// frames of an unknown type, counted by the reader only
	unknown int
}

func newConn(id string, ws *websocket.Conn, ping, idle time.Duration) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{
		id: id, ws: ws, ping: ping, idle: idle, welcome: make(chan []byte, 1), queue: make(chan []byte, queueSize),
		ctx: ctx, cancel: cancel, wrote: make(chan struct{}), ended: make(chan struct{}),
	}
}

func (c *conn) DeviceID() string { return c.id }

// Send queues env for the writer without blocking.
func (c *conn) Send(env *cadencev2.Envelope) error {
	b, err := wire.Encode(env)
	if err != nil {
		return err
	}
	if c.closing() {
		return ErrOffline
	}
	select {
	case c.queue <- b:
		return nil
	default:
		return ErrBackpressure
	}
}

func (c *conn) closing() bool { return c.ctx.Err() != nil }

// closeWith ends the link with code, 0 for no close frame; the first call wins.
func (c *conn) closeWith(code int, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		c.cancel()
	})
}

// write sends the welcome, then the queue and the pings, until the link
// closes. It is the only writer apart from the library's own control replies.
func (c *conn) write() {
	defer close(c.wrote)
	select {
	case <-c.ctx.Done():
		c.sendClose()
		return
	case b := <-c.welcome:
		if b != nil && c.frame(b) != nil {
			c.abort()
			return
		}
	}
	ping := time.NewTicker(c.ping)
	defer ping.Stop()
	for {
		// a closing link sends nothing more, even with frames queued
		select {
		case <-c.ctx.Done():
			c.sendClose()
			return
		default:
		}
		select {
		case <-c.ctx.Done():
			c.sendClose()
			return
		case b := <-c.queue:
			if c.frame(b) != nil {
				c.abort()
				return
			}
		case <-ping.C:
			if c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)) != nil {
				c.abort()
				return
			}
		}
	}
}

func (c *conn) frame(b []byte) error {
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteMessage(websocket.BinaryMessage, b)
}

// extend gives an open link idle more to send its next frame or pong.
func (c *conn) extend() {
	c.deadline.Lock()
	defer c.deadline.Unlock()
	if !c.closing() {
		_ = c.ws.SetReadDeadline(time.Now().Add(c.idle))
	}
}

// sendClose writes the close frame and gives the reader closeWait for the reply.
func (c *conn) sendClose() {
	if c.code != 0 {
		_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(c.code, c.reason), time.Now().Add(closeWait))
	}
	c.deadline.Lock()
	defer c.deadline.Unlock()
	_ = c.ws.SetReadDeadline(time.Now().Add(closeWait))
}

// abort drops a link whose writes fail; the closed socket ends the reader.
func (c *conn) abort() {
	c.closeWith(0, "")
	_ = c.ws.Close()
}

// tooBig reports a hit read limit: the library and the fiber wrapper each
// return their own error for it.
func tooBig(err error) bool {
	return errors.Is(err, fws.ErrReadLimit) || errors.Is(err, websocket.ErrReadLimit)
}

// closeCode is the code the server sent, else the peer's, else abnormal.
func (c *conn) closeCode(err error) int {
	if c.code != 0 {
		return c.code
	}
	var ce *fws.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return websocket.CloseAbnormalClosure
}
