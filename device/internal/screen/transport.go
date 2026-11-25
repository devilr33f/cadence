package screen

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

// the largest frame either side sends: a page of cards with its images' refs fits well inside
const maxFrameBytes = 8 << 20 // the screen side reads up to 8 MiB (screen/ipc/seqpacket.cpp kMaxFrame)

// a send that finds the screen's socket full this long gives up: a screen that stops reading is hung
const sendTimeout = time.Second

var errFrameTooLarge = errors.New("screen: frame too large")

// frameConn carries whole frames: SOCK_SEQPACKET keeps their boundaries on linux and android; elsewhere (a developer's
// host, its tests) a stream socket carries each behind a 4-byte length
type frameConn interface {
	readFrame() ([]byte, error)
	writeFrame([]byte) error
	Close() error
}

// Listen serves the screen plane at path, an abstract name when it starts with @
func listen(path string) (net.Listener, error) { return net.Listen(network, path) }

// Dial is the screen's side, for the fake screen plane and tests
func Dial(ctx context.Context, path string) (FrameConn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, path)
	if err != nil {
		return FrameConn{}, err
	}
	return FrameConn{newFrameConn(c)}, nil
}

// FrameConn is one end of the screen plane's socket, as a screen holds it
type FrameConn struct{ c frameConn }

func (f FrameConn) Read() ([]byte, error) { return f.c.readFrame() }
func (f FrameConn) Write(b []byte) error  { return f.c.writeFrame(b) }
func (f FrameConn) Close() error          { return f.c.Close() }

func newFrameConn(c net.Conn) frameConn {
	if network == "unixpacket" {
		return &packetConn{c: c, buf: make([]byte, maxFrameBytes+1)}
	}
	return &streamConn{c: c}
}

type packetConn struct {
	c   net.Conn
	buf []byte
	wmu sync.Mutex
}

func (p *packetConn) readFrame() ([]byte, error) {
	n, err := p.c.Read(p.buf)
	if err != nil {
		return nil, err
	}
	if n > maxFrameBytes {
		return nil, errFrameTooLarge
	}
	return append([]byte(nil), p.buf[:n]...), nil
}

func (p *packetConn) writeFrame(b []byte) error {
	if len(b) > maxFrameBytes {
		return errFrameTooLarge
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.c.SetWriteDeadline(time.Now().Add(sendTimeout))
	_, err := p.c.Write(b)
	return err
}

func (p *packetConn) Close() error { return p.c.Close() }

type streamConn struct {
	c   net.Conn
	wmu sync.Mutex
}

func (s *streamConn) readFrame() ([]byte, error) {
	var head [4]byte
	if _, err := readFull(s.c, head[:]); err != nil {
		return nil, err
	}
	n := int(head[0])<<24 | int(head[1])<<16 | int(head[2])<<8 | int(head[3])
	if n > maxFrameBytes {
		return nil, errFrameTooLarge
	}
	b := make([]byte, n)
	if _, err := readFull(s.c, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *streamConn) writeFrame(b []byte) error {
	if len(b) > maxFrameBytes {
		return errFrameTooLarge
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(b)), uint32(len(b))) //nolint:gosec // at most maxFrameBytes
	frame = append(frame, b...)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.c.SetWriteDeadline(time.Now().Add(sendTimeout))
	_, err := s.c.Write(frame)
	return err
}

func (s *streamConn) Close() error { return s.c.Close() }

func readFull(c net.Conn, b []byte) (int, error) {
	read := 0
	for read < len(b) {
		n, err := c.Read(b[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}
