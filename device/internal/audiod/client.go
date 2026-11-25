// Package audiod talks to the audiod media daemon over its unix SOCK_SEQPACKET
// socket and exposes it as the session controller's audio.IO.
package audiod

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const (
	recvBuf     = 8
	sendTimeout = time.Second
)

var (
	ErrClosed   = errors.New("audiod: closed")
	ErrTooLarge = errors.New("audiod: frame too large")
)

type Option func(*Client)

func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// Client is one connection to audiod: Send is safe for concurrent callers and
// one reader goroutine feeds Recv, which closes when the connection ends.
type Client struct {
	conn *net.UnixConn
	log  *slog.Logger
	recv chan *audiodv1.Envelope
	done chan struct{}

	wmu  sync.Mutex
	once sync.Once
}

func Dial(ctx context.Context, socket string, opts ...Option) (*Client, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unixpacket", socket)
	if err != nil {
		return nil, err
	}
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		_ = nc.Close()
		return nil, errors.New("audiod: not a unix connection")
	}
	c := &Client{
		conn: uc,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		recv: make(chan *audiodv1.Envelope, recvBuf),
		done: make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) Recv() <-chan *audiodv1.Envelope { return c.recv }

// Done closes when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Send(env *audiodv1.Envelope) error { return c.send(env, -1) }

// SendWithFD sends env with fd beside it in the same frame (SCM_RIGHTS): audiod gets its own copy, so the caller
// still owns fd and closes it once this returns. Only BluetoothStream carries one; audiod refuses any other.
func (c *Client) SendWithFD(env *audiodv1.Envelope, fd int) error {
	if fd < 0 {
		return errors.New("audiod: no fd")
	}
	return c.send(env, fd)
}

func (c *Client) send(env *audiodv1.Envelope, fd int) error {
	b, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	if len(b) > wire.MaxAudiodFrameBytes {
		return ErrTooLarge
	}
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	if fd >= 0 {
		// one sendmsg: the frame and its fd arrive together or not at all
		_, _, err = c.conn.WriteMsgUnix(b, syscall.UnixRights(fd), nil)
	} else {
		_, err = c.conn.Write(b)
	}
	// audiod drains its socket in real time: one that stops is hung, and a
	// blocked send would stall the controller and the supervisor's health check
	if errors.Is(err, os.ErrDeadlineExceeded) {
		c.log.Warn("audiod_send_timeout")
		c.Close()
	}
	return err
}

func (c *Client) Close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *Client) readLoop() {
	defer close(c.recv)
	defer c.Close()
	buf := make([]byte, wire.MaxAudiodFrameBytes+1)
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			return
		}
		if n > wire.MaxAudiodFrameBytes {
			c.log.Warn("audiod_ipc_invalid", "code", "oversize")
			return
		}
		env := &audiodv1.Envelope{}
		if err := proto.Unmarshal(buf[:n], env); err != nil {
			c.log.Warn("audiod_ipc_invalid", "code", "garbage")
			return
		}
		if env.GetBody() == nil {
			continue
		}
		select {
		case c.recv <- env:
		case <-c.done:
			return
		}
	}
}
