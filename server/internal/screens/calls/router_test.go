package calls_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

type sink struct {
	mu  sync.Mutex
	got []*cadencev2.Envelope
}

func (s *sink) send(_ string, env *cadencev2.Envelope) error {
	s.mu.Lock()
	s.got = append(s.got, env)
	s.mu.Unlock()
	return nil
}

func (s *sink) all() []*cadencev2.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*cadencev2.Envelope(nil), s.got...)
}

func (s *sink) wait(t *testing.T, n int) []*cadencev2.Envelope {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.all()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d frames, want %d: %v", len(s.all()), n, s.all())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.all()
}

func req(id, method string, subscribe bool) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: id, Method: method, Json: `{"q":1}`, Subscribe: subscribe,
	}}}
}

func newRouter(t *testing.T) (*calls.Router, *sink) {
	s := &sink{}
	r := calls.New(s.send, slog.New(slog.DiscardHandler))
	t.Cleanup(r.Close)
	return r, s
}

func TestRouterAnswersAndRefuses(t *testing.T) {
	r, s := newRouter(t)
	r.Register("echo", func(_ context.Context, c calls.Call) calls.Reply {
		var a map[string]int
		_ = json.Unmarshal(c.Args, &a)
		return calls.OK(map[string]any{"q": a["q"], "dev": c.DeviceID})
	})
	r.Register("boom", func(context.Context, calls.Call) calls.Reply { panic("secret words") })
	r.Register("badpage", func(context.Context, calls.Call) calls.Reply {
		return calls.Reply{Page: &cardsv1.Page{Version: 9}}
	})
	r.Register("badresult", func(context.Context, calls.Call) calls.Reply {
		return calls.Reply{CardAction: &cardsv1.CardActionResult{}}
	})
	r.Register("refused", func(context.Context, calls.Call) calls.Reply {
		return calls.Reply{Status: rpc.StatusRefused, CardAction: &cardsv1.CardActionResult{Result: &cardsv1.CardActionResult_Refusal{
			Refusal: &cardsv1.ActionRefusal{Reason: "denied"},
		}}}
	})
	if !r.Handle("d1", req("1", "echo", false)) || r.Handle("d1", &cadencev2.Envelope{}) {
		t.Fatal("handle")
	}
	got := s.wait(t, 1)[0].GetRpcResponse()
	if got.GetId() != "1" || got.GetStatus() != rpc.StatusOK || got.GetJson() != `{"dev":"d1","q":1}` {
		t.Fatalf("answer %v", got)
	}
	r.Handle("d1", req("2", "nope", false))
	r.Handle("d1", req("3", "boom", false))
	r.Handle("d1", req("4", "badpage", false))
	r.Handle("d1", req("5", "badresult", false))
	r.Handle("d1", req("6", "refused", false))
	byID := map[string]string{}
	for _, e := range s.wait(t, 6) {
		byID[e.GetRpcResponse().GetId()] = e.GetRpcResponse().GetStatus()
		if e.GetRpcResponse().GetId() == "6" && e.GetRpcResponse().GetCardAction().GetRefusal().GetReason() != "denied" {
			t.Fatalf("typed answer %v", e.GetRpcResponse())
		}
		if e.GetRpcResponse().GetId() == "5" && e.GetRpcResponse().GetCardAction() != nil {
			t.Fatalf("an invalid answer went out: %v", e.GetRpcResponse())
		}
	}
	if byID["2"] != rpc.StatusUnknownMethod || byID["3"] != rpc.StatusInternal || byID["4"] != rpc.StatusInternal ||
		byID["5"] != rpc.StatusInternal || byID["6"] != rpc.StatusRefused {
		t.Fatalf("statuses %v", byID)
	}
}

func TestRouterStreamsProgressBeforeTheResponse(t *testing.T) {
	r, s := newRouter(t)
	r.Register("play", func(_ context.Context, c calls.Call) calls.Reply {
		if err := c.Event(map[string]any{"stage": "extract", "elapsedMs": 12}); err != nil {
			t.Error(err)
		}
		time.Sleep(10 * time.Millisecond)
		return calls.OK(map[string]string{"sessionId": "s1"})
	})
	r.Handle("d1", req("p", "play", true))
	frames := s.wait(t, 3)
	if ev := frames[0].GetRpcEvent(); ev.GetSeq() != 1 || ev.GetJson() != `{"elapsedMs":12,"stage":"extract"}` {
		t.Fatalf("progress %v", frames[0])
	}
	if got := frames[1].GetRpcResponse(); got.GetStatus() != rpc.StatusOK || got.GetJson() != `{"sessionId":"s1"}` {
		t.Fatalf("response %v", got)
	}
	if last := frames[2].GetRpcEvent(); !last.GetLast() || last.GetSeq() != 2 {
		t.Fatalf("last %v", last)
	}
}

func TestRouterStreamsUntilCancelled(t *testing.T) {
	r, s := newRouter(t)
	ended := make(chan struct{})
	r.Register("watch", func(context.Context, calls.Call) calls.Reply {
		return calls.Reply{Status: rpc.StatusOK, Follow: func(ctx context.Context, st *calls.Stream) {
			_ = st.Event(map[string]int{"n": 1}, nil)
			<-ctx.Done()
			close(ended)
		}}
	})
	r.Handle("d1", req("w", "watch", true))
	frames := s.wait(t, 2)
	if frames[0].GetRpcResponse().GetStatus() != rpc.StatusOK || frames[1].GetRpcEvent().GetSeq() != 1 {
		t.Fatalf("frames %v", frames)
	}
	// the same id again is busy while it streams
	r.Handle("d1", req("w", "watch", true))
	if f := s.wait(t, 3)[2].GetRpcResponse(); f.GetStatus() != rpc.StatusBusy {
		t.Fatalf("duplicate %v", f)
	}
	r.Handle("d1", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcCancel{RpcCancel: &cadencev2.RpcCancel{Id: "w"}}})
	<-ended
	last := s.wait(t, 4)[3].GetRpcEvent()
	if !last.GetLast() || last.GetSeq() != 2 {
		t.Fatalf("last %v", last)
	}
	// a link's end ends its streams
	r.Handle("d1", req("w2", "watch", true))
	s.wait(t, 6)
	ended = make(chan struct{})
	r.Closed("d1")
	<-ended
}

// a reply past the link's frame limit is refused there, and the screen would wait for nothing: it hears too_large
func TestRouterAnswersTooLargeInsteadOfDroppingAReply(t *testing.T) {
	r, s := newRouter(t)
	big := strings.Repeat("x", wire.MaxFrameBytes)
	r.Register("big", func(context.Context, calls.Call) calls.Reply { return calls.OK(map[string]string{"blob": big}) })
	r.Handle("d1", req("1", "big", false))
	got := s.wait(t, 1)[0].GetRpcResponse()
	if got.GetStatus() != rpc.StatusInternal || got.GetJson() != `{"code":"too_large"}` {
		t.Fatalf("answer %v", got)
	}
	if size := proto.Size(s.all()[0]); size > wire.MaxFrameBytes {
		t.Fatalf("the refusal is %d bytes", size)
	}
}
