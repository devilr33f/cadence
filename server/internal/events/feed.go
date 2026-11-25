package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	pingInterval = 20 * time.Second
	idleTimeout  = 60 * time.Second
	writeWait    = 10 * time.Second
	closeWait    = time.Second
	// how long one check of the feed's credential may take
	recheckWait = 2 * time.Second
)

// Feed serves the admin live feed: one bus subscription per connection.
type Feed struct {
	bus *Bus
	log *slog.Logger
	// the server pings every PingInterval (default 20 s) and checks the
	// caller's credential again with each ping; a peer silent for IdleTimeout
	// (default 60 s) is dropped
	PingInterval, IdleTimeout time.Duration

	mu     sync.Mutex
	closed bool
	code   int
	reason string
	done   chan struct{} // closed by Close
	wg     sync.WaitGroup
	// the open feeds whose selection holds the logs topic, the traces topic
	logPeers, tracePeers atomic.Int64

	beforeWrite    func()        // test seam
	recheckTimeout time.Duration // test seam; 0: recheckWait
	// test seams: the clock of the log-line limit and how often a peer hears
	// of the lines it missed (zero: time.Now, lineEvery)
	now       func() time.Time
	lineEvery time.Duration
}

func NewFeed(bus *Bus, log *slog.Logger) *Feed {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Feed{bus: bus, log: log, done: make(chan struct{})}
}

// Route is GET /live as a plain admin route: the admin auth is checked before
// the upgrade (a bearer, or the owner's cookie from the server's own Origin),
// and the caller needs a scope that allows a topic (ha or admin). Query
// deviceId filters; the client's subscribe frames pick topics.
func (f *Feed) Route() api.PlainRoute {
	return api.PlainRoute{
		Op: "live", Method: fiber.MethodGet, Path: "/live", Scopes: feedScopes(),
		Handler: websocket.New(f.serve, websocket.Config{RecoverHandler: func(*websocket.Conn) {
			if recover() != nil {
				f.log.Error("feed_panic")
			}
		}}),
	}
}

// LogsSubscribed tells whether any open feed subscribed to the logs topic, so
// the log store can skip publishing lines no feed would write.
func (f *Feed) LogsSubscribed() bool { return f.logPeers.Load() > 0 }

// TracesSubscribed tells whether any open feed subscribed to the traces
// topic, so the trace store can skip publishing span events no feed would write.
func (f *Feed) TracesSubscribed() bool { return f.tracePeers.Load() > 0 }

// choose makes sel the client's selection, counting the feeds that tail logs
// and traces.
func (f *Feed) choose(c *client, sel *selection) {
	old := c.sel.Swap(sel)
	count := func(t Topic, peers *atomic.Int64) {
		switch was, now := old.topics[t], sel.topics[t]; {
		case now && !was:
			peers.Add(1)
		case was && !now:
			peers.Add(-1)
		}
	}
	count(TopicLogs, &f.logPeers)
	count(TopicTraces, &f.tracePeers)
}

// Close ends every feed with code and waits for the handlers to return; links
// opened later are refused with the same code.
func (f *Feed) Close(code int, reason string) {
	f.mu.Lock()
	if !f.closed {
		f.closed, f.code, f.reason = true, code, reason
		close(f.done)
	}
	f.mu.Unlock()
	f.wg.Wait()
}

func (f *Feed) enter() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.wg.Add(1)
	return true
}

type frame struct {
	Type      string `json:"type"`
	DeviceID  string `json:"deviceId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	At        string `json:"at,omitempty"`
	Data      any    `json:"data,omitempty"`
	Count     uint64 `json:"count,omitempty"`
}

func encode(e Event) ([]byte, error) {
	fr := frame{Type: e.Type, DeviceID: e.DeviceID, SessionID: e.SessionID, TurnID: e.TurnID, Data: e.Data}
	if !e.At.IsZero() {
		fr.At = e.At.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(fr)
}
func (f *Feed) serve(ws *websocket.Conn) {
	if !f.enter() {
		f.mu.Lock()
		code, reason := f.code, f.reason
		f.mu.Unlock()
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(closeWait))
		return
	}
	defer f.wg.Done()

	ping, idle := f.PingInterval, f.IdleTimeout
	if ping <= 0 {
		ping = pingInterval
	}
	if idle <= 0 {
		idle = idleTimeout
	}
	now := f.now
	if now == nil {
		now = time.Now
	}
	c := &client{
		ws: ws, scopes: api.CallerScopes(ws), recheck: api.CallerRecheck(ws), device: ws.Query("deviceId"),
		replies: make(chan []byte, maxClientFrames), quit: make(chan struct{}), lines: &lineLimit{now: now},
		spans: &lineLimit{now: now, rate: spanRate},
	}
	c.sel.Store(defaultSelection(c.scopes, c.device))
	// runs once the reader is gone, so the selection no longer changes
	defer func() {
		if c.sel.Load().topics[TopicLogs] {
			f.logPeers.Add(-1)
		}
		if c.sel.Load().topics[TopicTraces] {
			f.tracePeers.Add(-1)
		}
	}()
	// the line limit applies as the bus delivers, after the selection, so lines
	// past it never take the buffer's room from other events
	sub := f.bus.Watch(func(e Event) bool {
		if !c.sel.Load().wants(e) {
			return false
		}
		switch topicOf[e.Type] {
		case TopicLogs:
			return c.lines.allow()
		case TopicTraces:
			return c.spans.allow()
		}
		return true
	})
	defer sub.Close()
	f.log.Info("feed_open")
	reason := "peer"
	defer func() { f.log.Info("feed_closed", "reason", reason) }()

	_ = ws.SetReadDeadline(time.Now().Add(idle))
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		f.read(c, idle)
	}()

	end := f.pump(c, sub, ping, gone)
	close(c.quit)
	if end != nil {
		reason = end.reason
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(end.code, end.text), time.Now().Add(closeWait))
		// let the peer answer the close before the socket goes
		_ = ws.SetReadDeadline(time.Now().Add(closeWait))
		<-gone
	}
	_ = ws.Close()
	<-gone
}

// ending is why the server ends a feed: its close frame, and the reason
// feed_closed logs.
type ending struct {
	code         int
	text, reason string
}

// revoked ends a feed whose credential no longer holds: deleted, its scopes
// changed, or its session ended
var revoked = &ending{code: websocket.ClosePolicyViolation, text: "unauthorized", reason: "revoked"}

// client is one feed's peer: what it may read, what it selected, and the
// answers to its frames, which the pump writes.
type client struct {
	ws     *websocket.Conn
	scopes auth.Scopes
	// asks the store again whether the credential still holds with scopes
	recheck func(context.Context) error
	device  string // the query's deviceId
	sel     atomic.Pointer[selection]
	replies chan []byte
	quit    chan struct{} // closed once the pump returned
	lines   *lineLimit    // the logs topic's lines a second
	spans   *lineLimit    // the traces topic's events a second
}

// read takes the peer's subscriptions until it goes; a frame past
// maxClientFrame bytes, or more than maxClientFrames in a second, pings and
// pongs counted, closes the feed with 1008. Any frame extends the deadline.
// The ping and pong handlers run inside NextReader, on this goroutine alone.
func (f *Feed) read(c *client, idle time.Duration) {
	var lim frameLimit
	arrived := func() bool {
		_ = c.ws.SetReadDeadline(time.Now().Add(idle))
		if lim.over(time.Now()) {
			f.violation(c.ws, "rate_limited")
			return false
		}
		return true
	}
	c.ws.SetPingHandler(func(msg string) error {
		if !arrived() {
			return errViolation
		}
		// as the default handler: a failed pong shows in the next read or write
		_ = c.ws.WriteControl(websocket.PongMessage, []byte(msg), time.Now().Add(writeWait))
		return nil
	})
	c.ws.SetPongHandler(func(string) error {
		if !arrived() {
			return errViolation
		}
		return nil
	})
	for {
		typ, r, err := c.ws.NextReader()
		if err != nil || !arrived() {
			return
		}
		b, err := io.ReadAll(io.LimitReader(r, maxClientFrame+1))
		if err != nil {
			return
		}
		if len(b) > maxClientFrame {
			f.violation(c.ws, "too_large")
			return
		}
		reply := f.answer(c, typ, b)
		select {
		case c.replies <- reply:
		case <-c.quit:
			return
		}
	}
}

// answer applies a subscription and says what the feed now writes, or why
// it refused the frame.
func (f *Feed) answer(c *client, typ int, b []byte) []byte {
	var out any = refusalFrame{Error: "invalid"}
	if typ == websocket.TextMessage {
		sel, refusal, forbidden := parseSubscribe(b, c.scopes, c.device)
		switch {
		case forbidden != "":
			f.log.Warn("api_forbidden", "op", "live", "scope", string(topicScopes[forbidden]), "topic", string(forbidden))
			out = refusal
		case refusal != nil:
			out = refusal
		default:
			f.choose(c, sel)
			out = sel.answer()
		}
	}
	reply, _ := json.Marshal(out)
	return reply
}

func (f *Feed) violation(ws *websocket.Conn, reason string) {
	f.log.Warn("feed_violation", "reason", reason)
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(closeWait))
}

// subscribed is a feed's first frame: every event published after the peer
// read it reaches the peer (the e2e waits for it); so does every event of a
// later subscription published after the peer read its answer
var subscribed = []byte(`{"type":"subscribed"}`)

// pump writes until the peer goes (nil), the feed closes, or the caller's
// credential no longer holds.
func (f *Feed) pump(c *client, sub *Subscription, ping time.Duration, gone <-chan struct{}) *ending {
	ws := c.ws
	tick := time.NewTicker(ping)
	defer tick.Stop()
	every := f.lineEvery
	if every <= 0 {
		every = lineEvery
	}
	lineTick := time.NewTicker(every)
	defer lineTick.Stop()
	var checked chan error // nil while no check runs
	// a check in flight ends with the pump, so none outlives the handler
	var checks sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		checks.Wait()
	}()
	send := func(b []byte) bool {
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		return ws.WriteMessage(websocket.TextMessage, b) == nil
	}
	write := func(b []byte) bool {
		if f.beforeWrite != nil {
			f.beforeWrite()
		}
		return send(b)
	}
	if !send(subscribed) {
		return nil
	}
	for {
		if n := sub.Dropped(); n > 0 {
			b, _ := json.Marshal(frame{Type: "dropped", Count: n})
			if !write(b) {
				return nil
			}
		}
		select {
		case <-gone:
			return nil
		case <-f.done:
			f.mu.Lock()
			end := &ending{code: f.code, text: f.reason, reason: "shutdown"}
			f.mu.Unlock()
			return end
		case <-sub.Drops():
		case b := <-c.replies:
			if !send(b) {
				return nil
			}
		case e := <-sub.C:
			// a subscription that changed since the event was buffered
			if !c.sel.Load().wants(e) {
				continue
			}
			b, err := encode(e)
			if err != nil {
				f.log.Warn("feed_encode_failed", "type", e.Type)
				continue
			}
			if !write(b) {
				return nil
			}
		case <-lineTick.C:
			if n := c.lines.takeDropped(); n > 0 {
				b, _ := json.Marshal(frame{Type: LogLineDropped, Data: map[string]uint64{"count": n}})
				if !write(b) {
					return nil
				}
			}
			if n := c.spans.takeDropped(); n > 0 {
				b, _ := json.Marshal(frame{Type: SpanDropped, Data: map[string]uint64{"count": n}})
				if !write(b) {
					return nil
				}
			}
		case <-tick.C:
			if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)) != nil {
				return nil
			}
			// one check at a time, off the pump: a slow store holds no event up
			if checked == nil && c.recheck != nil {
				out := make(chan error, 1)
				checked = out
				checks.Go(func() { out <- f.recheck(ctx, c.recheck) })
			}
		case err := <-checked:
			checked = nil
			switch {
			case errors.Is(err, auth.ErrUnauthorized):
				return revoked
			case errors.Is(err, context.DeadlineExceeded):
				f.log.Warn("feed_recheck_failed", "code", "timeout")
			case err != nil:
				// the cause can carry driver text; the next tick checks again
				f.log.Warn("feed_recheck_failed", "code", "store")
			}
		}
	}
}

// recheck runs one check of the caller's credential within its time; a check
// that ran out of it answers context.DeadlineExceeded, whatever the store said.
func (f *Feed) recheck(ctx context.Context, check func(context.Context) error) error {
	wait := f.recheckTimeout
	if wait <= 0 {
		wait = recheckWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	err := check(ctx)
	if err != nil && !errors.Is(err, auth.ErrUnauthorized) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}
