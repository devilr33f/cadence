package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	helloWait    = 10 * time.Second
	pingInterval = 20 * time.Second
	idleTimeout  = 60 * time.Second
	// the link protocol this server speaks; a hello below 2 or above it is refused with 1002.
	// protocol 3 adds the device-update frames (ops.proto) over 2: a 2 daemon links as before (it
	// never gets an ops_start, which the update service gates on the hello's protocol), so the
	// server takes both until every device runs one
	protocol = 3
)

// Handlers are called on the connection's reader goroutine. Per device,
// OnHello, OnMessage and OnClose run one at a time, in connection order. A
// connection's ctx is done once it was replaced or began to close; from then
// on it gets no OnMessage, and OnClose only if nothing replaced it.
// OnMessage must return promptly; a newer link of the device waits for it.
// An OnMessage running past IdleTimeout (counted from the last frame) drops
// the link once it returns.
type Handlers struct {
	OnHello   func(ctx context.Context, c Conn, h *cadencev2.Hello) *cadencev2.Welcome
	OnMessage func(ctx context.Context, c Conn, env *cadencev2.Envelope)
	// OnClose fires only when the closing connection is still the device's one.
	OnClose func(deviceID string)
	// Seen runs once per accepted hello, before OnHello.
	Seen func(ctx context.Context, deviceID string)
	// The server pings every PingInterval (default 20 s); a link that sends
	// no frame and answers no ping for IdleTimeout (default 60 s) is dropped.
	PingInterval, IdleTimeout time.Duration
}

type deviceKey struct{}

type gateway struct {
	hub *Hub
	hs  Handlers
	log *slog.Logger
}

// Mount serves the device link at GET /device; the bearer is checked before the upgrade.
func Mount(r fiber.Router, hub *Hub, authDevice func(ctx context.Context, token string) (string, error), hs Handlers, log *slog.Logger) {
	if hs.PingInterval <= 0 {
		hs.PingInterval = pingInterval
	}
	if hs.IdleTimeout <= 0 {
		hs.IdleTimeout = idleTimeout
	}
	g := &gateway{hub: hub, hs: hs, log: log}
	r.Get("/device", authorize(authDevice, log), websocket.New(g.serve, websocket.Config{RecoverHandler: g.recovered}))
}

// recovered logs a handler panic by event only: its value can carry anything.
func (g *gateway) recovered(ws *websocket.Conn) {
	if recover() != nil {
		id, _ := ws.Locals(deviceKey{}).(string)
		g.log.Error("link_panic", "device_id", id)
	}
}

func authorize(authDevice func(context.Context, string) (string, error), log *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || token == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		id, err := authDevice(c.Context(), token)
		switch {
		case err == nil:
			c.Locals(deviceKey{}, id)
			return c.Next()
		case errors.Is(err, auth.ErrUnauthorized):
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		default:
			// the cause can carry driver text, so only the event is logged
			log.Error("auth_unavailable")
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "unavailable"})
		}
	}
}

func (g *gateway) serve(ws *websocket.Conn) {
	id, _ := ws.Locals(deviceKey{}).(string)
	c := newConn(id, ws, g.hs.PingInterval, g.hs.IdleTimeout)
	var err error
	go c.write()
	// deferred, so a panicking handler still leaves the hub
	defer func() {
		c.closeWith(0, "")
		g.hub.detach(c, func() { g.hs.OnClose(id) })
		g.finish(c, err)
		<-c.wrote
		close(c.ended)
	}()

	ws.SetReadLimit(wire.MaxFrameBytes)
	_ = ws.SetReadDeadline(time.Now().Add(helloWait))
	var env *cadencev2.Envelope
	env, err = g.receive(c, false)
	hello := env.GetHello()
	switch {
	case hello == nil && env != nil:
		g.invalid(c)
	case hello != nil && (hello.GetProtocol() < 2 || hello.GetProtocol() > protocol):
		g.log.Warn("link_refused", "device_id", id, "reason", "protocol")
		c.closeWith(websocket.CloseProtocolError, "")
	}
	if hello == nil || c.closing() {
		for err == nil {
			_, err = g.receive(c, false)
		}
		return
	}
	// from here on a frame or a pong extends the deadline, until a close sets it
	c.extend()
	ws.SetPongHandler(func(string) error {
		c.extend()
		return nil
	})

	g.hs.Seen(c.ctx, id)
	var welcome *cadencev2.Welcome
	if g.hub.attach(c, func() { welcome = g.hs.OnHello(c.ctx, c, hello) }) {
		g.log.Info("link_open", "device_id", id)
	}
	first, encErr := encodeWelcome(welcome)
	if encErr != nil {
		c.closeWith(websocket.CloseInternalServerErr, "")
	}
	// the welcome bypasses the queue, so it precedes anything OnHello sent
	c.welcome <- first

	// a refused or replaced link keeps reading, discarding, until the close is answered
	for err == nil {
		if env, err = g.receive(c, true); env != nil {
			// a frame's own trace context, if it carries one, parents the work it starts
			ctx := FrameContext(c.ctx, env)
			g.hub.deliver(c, func() { g.hs.OnMessage(ctx, c, env) })
		}
	}
	// past a close the deadline is the close's own wait, not silence
	if errors.Is(err, os.ErrDeadlineExceeded) && !c.closing() {
		g.log.Info("link_idle", "device_id", id)
	}
}

// receive reads the next envelope. It returns none for a frame read while the
// link closes, for one of a type this side does not know once past the hello
// (skipped and counted), or for an invalid one, which closes it with 1007; an
// error ends the link.
func (g *gateway) receive(c *conn, pastHello bool) (*cadencev2.Envelope, error) {
	mt, b, err := c.ws.ReadMessage()
	if tooBig(err) {
		// the library has sent 1009 already
		c.closeWith(websocket.CloseMessageTooBig, "")
	}
	if err != nil || c.closing() {
		return nil, err
	}
	if pastHello {
		c.extend()
	}
	env, err := wire.Decode(b)
	switch {
	case mt == websocket.BinaryMessage && pastHello && errors.Is(err, wire.ErrUnknown):
		g.unknown(c)
		return nil, nil
	case mt != websocket.BinaryMessage || err != nil:
		g.invalid(c)
		return nil, nil
	}
	return env, nil
}

// unknown counts a newer device's message type; it is logged at 1, 2, 4, …
// so a stream of them cannot flood the log.
func (g *gateway) unknown(c *conn) {
	if c.unknown++; c.unknown&(c.unknown-1) == 0 {
		g.log.Info("link_unknown_message", "device_id", c.id, "count", c.unknown)
	}
}

func encodeWelcome(w *cadencev2.Welcome) ([]byte, error) {
	if w == nil {
		return nil, nil
	}
	return wire.Encode(&cadencev2.Envelope{Body: &cadencev2.Envelope_Welcome{Welcome: w}})
}

func (g *gateway) invalid(c *conn) {
	g.log.Warn("link_invalid", "device_id", c.id)
	c.closeWith(websocket.CloseInvalidFramePayloadData, "")
}

// finish closes the socket once nothing is left unread: closing over unread
// bytes resets the connection, which can drop the close frame on its way out.
func (g *gateway) finish(c *conn, err error) {
	if tooBig(err) {
		// the library stopped inside the frame, so the rest is read raw until
		// the peer hangs up or the close deadline passes
		_, _ = io.Copy(io.Discard, c.ws.NetConn())
	}
	_ = c.ws.Close()
	g.log.Info("link_closed", "device_id", c.id, "code", c.closeCode(err))
}
