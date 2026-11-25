package t3codetest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
)

// peer serves one WebSocket: unary calls answered with an Exit, streams
// sent a chunk at a time, each waiting for the client's Ack as effect's
// RpcServer does without T3's window.
type peer struct {
	ctx context.Context
	s   *Server
	c   *websocket.Conn

	wmu     sync.Mutex
	mu      sync.Mutex
	streams map[string]*fakeStream
}

type fakeStream struct {
	acks   chan struct{}
	cancel context.CancelFunc
}

func newPeer(ctx context.Context, s *Server, c *websocket.Conn) *peer {
	return &peer{ctx: ctx, s: s, c: c, streams: map[string]*fakeStream{}}
}

func (p *peer) write(v obj) {
	b, _ := json.Marshal(v)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.c.Write(p.ctx, websocket.MessageText, b)
}

func (p *peer) run() {
	for {
		_, b, err := p.c.Read(p.ctx)
		if err != nil {
			return
		}
		frames, err := t3code.DecodeFrames(b)
		if err != nil {
			p.write(obj{"_tag": "Defect", "defect": "bad frame"})
			continue
		}
		for _, f := range frames {
			if !p.handle(f) {
				return
			}
		}
	}
}

func (p *peer) handle(f t3code.Frame) bool {
	switch f.Tag {
	case "Ping":
		p.s.mu.Lock()
		p.s.pings++
		p.s.mu.Unlock()
		p.write(obj{"_tag": "Pong"})
	case "Ack":
		p.s.mu.Lock()
		p.s.acks++
		p.s.mu.Unlock()
		p.mu.Lock()
		st := p.streams[string(f.RequestID)]
		p.mu.Unlock()
		if st != nil {
			select {
			case st.acks <- struct{}{}:
			default:
			}
		}
	case "Interrupt":
		p.mu.Lock()
		st := p.streams[string(f.RequestID)]
		delete(p.streams, string(f.RequestID))
		p.mu.Unlock()
		if st != nil {
			st.cancel()
		}
		p.exit(f.RequestID, obj{"_tag": "Failure", "cause": []any{obj{"_tag": "Interrupt", "fiberId": nil}}})
	case "Eof":
		return false
	case "Request":
		p.request(f)
	}
	return true
}

func (p *peer) exit(id json.RawMessage, exit obj) {
	p.write(obj{"_tag": "Exit", "requestId": id, "exit": exit})
}

func success(v any) obj { return obj{"_tag": "Success", "value": v} }

func failure(err obj) obj {
	return obj{"_tag": "Failure", "cause": []any{obj{"_tag": "Fail", "error": err}}}
}

func (p *peer) request(f t3code.Frame) {
	p.s.cfg.Log.Info("request", "method", f.Method)
	switch f.Method {
	case t3code.MethodDispatchCommand:
		p.exit(f.ID, p.s.dispatch(f.Payload))
	case t3code.MethodGetThreadProjection:
		var in struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(f.Payload, &in)
		p.s.st.mu.Lock()
		t := p.s.st.threads[in.ThreadID]
		var exit obj
		if t == nil {
			exit = failure(obj{"_tag": "OrchestrationV2GetThreadProjectionError", "threadId": in.ThreadID, "message": "thread not found"})
		} else {
			exit = success(p.s.st.projection(t))
		}
		p.s.st.mu.Unlock()
		p.exit(f.ID, exit)
	case t3code.MethodLaunchThread:
		p.exit(f.ID, p.s.launch(f.Payload))
	case t3code.MethodSubscribeShell, t3code.MethodSubscribeThread:
		p.subscribe(f)
	default:
		p.exit(f.ID, obj{"_tag": "Failure", "cause": []any{obj{"_tag": "Die", "defect": "unknown method"}}})
	}
}

func (p *peer) subscribe(f t3code.Frame) {
	var in struct {
		ThreadID      string `json:"threadId"`
		AfterSequence *int64 `json:"afterSequence"`
	}
	_ = json.Unmarshal(f.Payload, &in)
	p.s.mu.Lock()
	p.s.subscribes = append(p.s.subscribes, Subscribe{Method: f.Method, ThreadID: in.ThreadID, AfterSequence: in.AfterSequence})
	p.s.mu.Unlock()
	st := p.s.st
	if f.Method == t3code.MethodSubscribeThread {
		st.mu.Lock()
		known := st.threads[in.ThreadID] != nil
		st.mu.Unlock()
		if !known {
			p.exit(f.ID, failure(obj{"_tag": "OrchestrationV2GetThreadProjectionError", "threadId": in.ThreadID, "message": "thread not found"}))
			return
		}
	}
	ctx, cancel := context.WithCancel(p.ctx)
	fs := &fakeStream{acks: make(chan struct{}, 1), cancel: cancel}
	p.mu.Lock()
	p.streams[string(f.ID)] = fs
	p.mu.Unlock()
	go p.stream(ctx, f.ID, fs, f.Method, in.ThreadID, in.AfterSequence)
}

// stream sends the first items (a replay after the cursor, or a snapshot),
// then every new one, up to three values a chunk.
func (p *peer) stream(ctx context.Context, id json.RawMessage, fs *fakeStream, method, threadID string, after *int64) {
	st := p.s.st
	st.mu.Lock()
	var items []obj
	var cursor int64
	if after != nil && *after <= st.seq {
		cursor = *after
		items = st.since(method, threadID, cursor)
	} else if method == t3code.MethodSubscribeShell {
		items = []obj{{"kind": "snapshot", "snapshot": st.shellSnapshot()}}
	} else {
		items = []obj{{"kind": "snapshot", "snapshotSequence": st.seq, "projection": st.projection(st.threads[threadID])}}
	}
	cursor = st.seq
	changed := st.changed
	st.mu.Unlock()
	for {
		for len(items) > 0 {
			n := min(3, len(items))
			values := make([]any, n)
			for i := range n {
				values[i] = items[i]
			}
			items = items[n:]
			p.write(obj{"_tag": "Chunk", "requestId": id, "values": values})
			select {
			case <-ctx.Done():
				return
			case <-fs.acks:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
		st.mu.Lock()
		items = st.since(method, threadID, cursor)
		cursor = st.seq
		changed = st.changed
		st.mu.Unlock()
	}
}

// since is the stream's logged items after cursor; mu held.
func (s *state) since(method, threadID string, cursor int64) []obj {
	var out []obj
	if method == t3code.MethodSubscribeShell {
		for _, l := range s.shellLog {
			if l.seq > cursor {
				out = append(out, l.item)
			}
		}
		return out
	}
	for _, l := range s.events {
		if l.seq > cursor && l.thread == threadID {
			out = append(out, l.item)
		}
	}
	return out
}
