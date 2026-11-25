// Package calls routes a device's RpcRequest frames (D5: a JSON call to a
// server method named in pkg/rpc) to the method that serves it, and carries a
// subscribed call's later frames: RpcEvent and the Cards envelopes of its live
// revisions. A call's arguments and results are never logged (refs, queries,
// titles and entity ids are private, rule 8); its method, status and ids are.
package calls

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/pkg/tracectx"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
)

const (
	// maxOpen bounds a device's calls in flight and subscribed streams
	maxOpen = 32
	maxArgs = 64 << 10
	maxID   = 64
)

// Call is one request.
type Call struct {
	DeviceID, ID, Method string
	Args                 json.RawMessage
	Subscribe            bool
	// Event streams progress while the method still runs, before its one
	// response. Nil for an unsubscribed call. Arguments and events are never logged.
	Event func(v any) error
}

// Reply is a method's one answer. JSON nil is {}. Page and CardAction, when
// set, are checked by pkg/cards before they go out. Follow, for a subscribed call answered ok,
// runs after the answer until its ctx ends (RpcCancel, the link's end, the
// server's stop) or it returns; then the stream's last event goes out.
type Reply struct {
	Status string
	JSON   any
	Page   *cardsv1.Page
	// CardAction is card.action's typed answer: the card's next revision, a
	// page, or a refusal
	CardAction *cardsv1.CardActionResult
	Follow     func(ctx context.Context, s *Stream)
}

// Fail is a refusal with a code and no upstream text.
func Fail(status, code string) Reply {
	return Reply{Status: status, JSON: map[string]string{"code": code}}
}

// OK is a plain success.
func OK(v any) Reply { return Reply{Status: rpc.StatusOK, JSON: v} }

// Method serves one method name.
type Method func(ctx context.Context, c Call) Reply

// Router serves every device's calls.
type Router struct {
	send    func(deviceID string, env *cadencev2.Envelope) error
	log     *slog.Logger
	methods map[string]Method

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	open map[string]map[string]context.CancelFunc
}

// New sends through send (the hub's).
func New(send func(deviceID string, env *cadencev2.Envelope) error, log *slog.Logger) *Router {
	ctx, cancel := context.WithCancel(context.Background())
	return &Router{send: send, log: log, methods: map[string]Method{}, ctx: ctx, cancel: cancel,
		open: map[string]map[string]context.CancelFunc{}}
}

// Register adds a method; call it before any link opens.
func (r *Router) Register(name string, m Method) { r.methods[name] = m }

// Handle takes RpcRequest and RpcCancel frames; false for anything else.
func (r *Router) Handle(deviceID string, env *cadencev2.Envelope) bool {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_RpcRequest:
		r.request(deviceID, b.RpcRequest)
		return true
	case *cadencev2.Envelope_RpcCancel:
		r.mu.Lock()
		cancel := r.open[deviceID][b.RpcCancel.GetId()]
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return true
	}
	return false
}

// Closed ends a device's open calls.
func (r *Router) Closed(deviceID string) {
	r.mu.Lock()
	calls := r.open[deviceID]
	delete(r.open, deviceID)
	r.mu.Unlock()
	for _, cancel := range calls {
		cancel()
	}
}

// Close ends every call and waits for them.
func (r *Router) Close() {
	r.cancel()
	r.wg.Wait()
}

func (r *Router) request(dev string, q *cadencev2.RpcRequest) {
	id := q.GetId()
	if id == "" || len(id) > maxID {
		return
	}
	m, ok := r.methods[q.GetMethod()]
	switch {
	case !ok:
		r.answer(r.ctx, dev, id, Fail(rpc.StatusUnknownMethod, "unknown_method"))
		return
	case len(q.GetJson()) > maxArgs:
		r.answer(r.ctx, dev, id, Fail(rpc.StatusInvalid, "too_large"))
		return
	}
	// the caller's span the request carries parents the call's
	ctx, cancel := context.WithCancel(tracectx.Extract(r.ctx, gateway.Carrier(q.GetTrace())))
	r.mu.Lock()
	calls := r.open[dev]
	if calls == nil {
		calls = map[string]context.CancelFunc{}
		r.open[dev] = calls
	}
	_, dup := calls[id]
	full := len(calls) >= maxOpen
	if !dup && !full {
		calls[id] = cancel
	}
	r.mu.Unlock()
	if dup || full {
		cancel()
		r.answer(r.ctx, dev, id, Fail(rpc.StatusBusy, "busy"))
		return
	}
	args := json.RawMessage(q.GetJson())
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	c := Call{DeviceID: dev, ID: id, Method: q.GetMethod(), Args: args, Subscribe: q.GetSubscribe()}
	ctx, span := startSpan(ctx, c)
	// the call's frames carry its span, so the caller's later spans join it
	tc := gateway.TraceOf(ctx)
	var stream *Stream
	if c.Subscribe {
		stream = &Stream{r: r, dev: dev, id: id, trace: tc}
		c.Event = func(v any) error { span.event(); return stream.Event(v, nil) }
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.done(dev, id, cancel)
		rep := r.run(ctx, m, c)
		sent := r.answer(ctx, dev, id, rep)
		// the span is the method's run to its answer; a follow can last as long as the screen
		span.end(rep, sent)
		if !sent {
			return
		}
		if rep.Follow != nil && c.Subscribe && rep.Status == rpc.StatusOK {
			s := &Stream{r: r, dev: dev, id: id, trace: tc}
			r.follow(ctx, c, rep.Follow, s)
			s.last()
		} else if stream != nil {
			stream.last()
		}
	}()
}

// run answers internal for a method that panicked; the value can carry
// anything, so only the method and ids are logged
func (r *Router) run(ctx context.Context, m Method, c Call) (rep Reply) {
	defer func() {
		if recover() != nil {
			r.log.Error("rpc_panic", "device_id", c.DeviceID, "rpc_id", c.ID, "method", c.Method)
			rep = Fail(rpc.StatusInternal, "internal")
		}
	}()
	return m(ctx, c)
}

func (r *Router) follow(ctx context.Context, c Call, f func(context.Context, *Stream), s *Stream) {
	defer func() {
		if recover() != nil {
			r.log.Error("rpc_panic", "device_id", c.DeviceID, "rpc_id", c.ID, "method", c.Method, "part", "follow")
		}
	}()
	f(ctx, s)
}

func (r *Router) done(dev, id string, cancel context.CancelFunc) {
	cancel()
	r.mu.Lock()
	if calls := r.open[dev]; calls != nil {
		delete(calls, id)
		if len(calls) == 0 {
			delete(r.open, dev)
		}
	}
	r.mu.Unlock()
}

// answer sends the reply, with the call's span in ctx as its trace context; false when it could not go out
func (r *Router) answer(ctx context.Context, dev, id string, rep Reply) bool {
	if rep.Status == "" {
		rep.Status = rpc.StatusOK
	}
	out := &cadencev2.RpcResponse{Id: id, Status: rep.Status, Trace: gateway.TraceOf(ctx)}
	if rep.Page != nil {
		if _, err := cards.EncodePage(rep.Page); err != nil {
			r.log.Error("rpc_page_invalid", "device_id", dev, "rpc_id", id, "code", codeOf(err))
			out.Status, rep.JSON = rpc.StatusInternal, map[string]string{"code": "internal"}
		} else {
			out.Page = rep.Page
		}
	}
	if rep.CardAction != nil {
		if err := cards.PrepareActionResult(rep.CardAction); err != nil {
			r.log.Error("rpc_card_invalid", "device_id", dev, "rpc_id", id, "code", codeOf(err))
			out.Status, rep.JSON = rpc.StatusInternal, map[string]string{"code": "internal"}
		} else {
			out.CardAction = rep.CardAction
		}
	}
	out.Json = encode(rep.JSON)
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcResponse{RpcResponse: out}}
	if size := proto.Size(env); size > wire.MaxFrameBytes {
		// the link refuses a frame past its limit, and the screen would wait for nothing: it hears why instead
		r.log.Warn("rpc_reply_too_large", "device_id", dev, "rpc_id", id, "bytes", size, "limit", wire.MaxFrameBytes)
		out.Status, out.Page, out.CardAction = rpc.StatusInternal, nil, nil
		out.Json = `{"code":"too_large"}`
	}
	if err := r.send(dev, env); err != nil {
		r.log.Warn("rpc_reply_unsent", "device_id", dev, "rpc_id", id)
		return false
	}
	return true
}

func encode(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func codeOf(err error) string {
	var e *cards.Error
	if errors.As(err, &e) {
		return string(e.Code)
	}
	return "invalid"
}

// Stream is a subscribed call's way out after its answer.
type Stream struct {
	r       *Router
	dev, id string
	// the call's span, on each event
	trace *cadencev2.TraceContext
	mu    sync.Mutex
	seq   uint32
	ended bool
}

// DeviceID is the caller's.
func (s *Stream) DeviceID() string { return s.dev }

// Event sends the call's next RpcEvent; a page is checked first.
func (s *Stream) Event(v any, page *cardsv1.Page) error {
	if page != nil {
		if _, err := cards.EncodePage(page); err != nil {
			s.r.log.Error("rpc_page_invalid", "device_id", s.dev, "rpc_id", s.id, "code", codeOf(err))
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return context.Canceled
	}
	s.seq++
	return s.r.send(s.dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcEvent{RpcEvent: &cadencev2.RpcEvent{
		Id: s.id, Seq: s.seq, Json: encode(v), Page: page, Trace: s.trace,
	}}})
}

// Cards sends new revisions of cards the call shows, as a Cards envelope
// whose context id is the call's; each is checked first.
func (s *Stream) Cards(cs ...*cardsv1.Card) error {
	for _, c := range cs {
		if _, err := cards.EncodeCard(c); err != nil {
			s.r.log.Error("rpc_card_invalid", "device_id", s.dev, "rpc_id", s.id, "code", codeOf(err))
			return err
		}
	}
	return s.r.send(s.dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_Cards{Cards: &cadencev2.Cards{
		ContextId: s.id, Cards: cs,
	}}})
}

func (s *Stream) last() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	s.seq++
	_ = s.r.send(s.dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcEvent{RpcEvent: &cadencev2.RpcEvent{
		Id: s.id, Seq: s.seq, Json: "{}", Last: true, Trace: s.trace,
	}}})
}
