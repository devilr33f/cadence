// Package link keeps the device's websocket link to the server.
package link

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const (
	dialWait = 15 * time.Second
	// a server that stops reading fails the write instead of parking the sender
	writeWait    = 10 * time.Second
	pingInterval = 20 * time.Second
	pingTimeout  = 10 * time.Second
	// a shutdown waits this long for the server to answer its close
	closeWait = time.Second
)

var (
	ErrUnauthorized = errors.New("link: token refused")
	errDial         = errors.New("link: dial failed")
	errScheme       = errors.New("link: server url must be http:// or https://")
)

// Dialer opens the link at <URL>/v2/device with the device token. TLS is
// verified against RootCAs, or the system pool when nil.
type Dialer struct {
	URL, Token string
	RootCAs    *x509.CertPool
	// once up, the link pings every PingInterval (default 20 s); a ping not
	// answered within PingTimeout (default 10 s) ends it
	PingInterval, PingTimeout time.Duration
	// Redial, when set, makes the link drop whatever it holds, dials or waits
	// for and dial again at once, starting its backoff over: the network under
	// it changed (a wi-fi switch)
	Redial <-chan struct{}
	// Blocked, when set and true, fails a dial before it starts, as an
	// unreachable server would (the sim's fake network with no route)
	Blocked   func() bool
	closeWait time.Duration // a shutdown's wait for the close reply, 1 s when zero
}

type Session interface {
	Send(env *cadencev2.Envelope) error
	Recv(ctx context.Context) (*cadencev2.Envelope, error)
	Close()
}

func (d Dialer) endpoint() (string, error) {
	base := strings.TrimRight(d.URL, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + "/v2/device", nil
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + "/v2/device", nil
	}
	return "", errScheme
}

// client dials through raw, which keeps the tcp connection it made last.
func (d Dialer) client(raw *net.Conn) *http.Client {
	var dialer net.Dialer
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: d.RootCAs, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := dialer.DialContext(ctx, network, addr)
				if err == nil {
					*raw = c
				}
				return c, err
			},
		},
		// a redirect could carry the bearer to another host
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}
}

func (d Dialer) Dial(ctx context.Context) (Session, error) { return d.dial(ctx) }

func (d Dialer) dial(ctx context.Context) (*session, error) {
	u, err := d.endpoint()
	if err != nil {
		return nil, err
	}
	if d.Blocked != nil && d.Blocked() {
		return nil, errDial
	}
	ctx, cancel := context.WithTimeout(ctx, dialWait)
	defer cancel()
	var raw net.Conn
	client := d.client(&raw)
	// a refused handshake leaves its connection kept alive in this dial's own pool
	defer client.CloseIdleConnections()
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + d.Token}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		// the library's error quotes the url and transport text
		return nil, errDial
	}
	c.SetReadLimit(wire.MaxFrameBytes)
	return &session{c: c, raw: raw}, nil
}

// withDefaults fills in the zero timings.
func (d Dialer) withDefaults() Dialer {
	if d.PingInterval <= 0 {
		d.PingInterval = pingInterval
	}
	if d.PingTimeout <= 0 {
		d.PingTimeout = pingTimeout
	}
	if d.closeWait <= 0 {
		d.closeWait = closeWait
	}
	return d
}

type session struct {
	c *websocket.Conn
	// the tcp connection under c: closing it ends whatever c is doing, which
	// the library's own close cannot once its handshake has begun
	raw net.Conn
}

// Send writes env as one binary frame; it is safe for concurrent use.
func (s *session) Send(env *cadencev2.Envelope) error {
	b, err := wire.Encode(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	return s.c.Write(ctx, websocket.MessageBinary, b)
}

// Recv reads the next envelope. A frame of a type this side does not know
// returns wire.ErrUnknown and leaves the link up; an invalid frame closes it
// with 1007, as the server does; a done ctx closes it too.
func (s *session) Recv(ctx context.Context) (*cadencev2.Envelope, error) {
	typ, b, err := s.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	env, err := wire.Decode(b)
	switch {
	case typ == websocket.MessageBinary && errors.Is(err, wire.ErrUnknown):
		return nil, wire.ErrUnknown
	case typ != websocket.MessageBinary || err != nil:
		_ = s.c.Close(websocket.StatusInvalidFramePayloadData, "")
		return nil, wire.ErrInvalid
	}
	return env, nil
}

func (s *session) Close() { _ = s.c.Close(websocket.StatusNormalClosure, "") }

// keepalive pings every interval until ctx is done or the link closes, and
// reports a ping left unanswered for timeout. A concurrent Recv reads the pongs.
func (s *session) keepalive(ctx context.Context, interval, timeout time.Duration) bool {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		err := s.c.Ping(pctx)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return true
		case err != nil:
			return false
		}
	}
}

// shutdown closes the link with 1001 and drops it if the server has not
// answered within wait.
func (s *session) shutdown(wait time.Duration) {
	t := time.AfterFunc(wait, s.drop)
	defer t.Stop()
	_ = s.c.Close(websocket.StatusGoingAway, "shutdown")
}

func (s *session) drop() { _ = s.raw.Close() }
