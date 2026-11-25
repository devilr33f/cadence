package voice

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// signalLLM closes asked once the agent sent its first request.
type signalLLM struct {
	inner providers.LLM
	asked chan struct{}
	once  sync.Once
}

func (l *signalLLM) Stream(ctx context.Context, r providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	l.once.Do(func() { close(l.asked) })
	return l.inner.Stream(ctx, r)
}

// gateTTS opens its streams only once open is closed.
type gateTTS struct {
	inner providers.TTS
	open  <-chan struct{}
}

func (g gateTTS) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	select {
	case <-g.open:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.inner.Open(ctx, o)
}

// the agent's first request goes out while tts is still opening, so the
// handshake overlaps the model's first step, as in M1
func TestTTSOpensBesideAgent(t *testing.T) {
	var llm *signalLLM
	h := newHarness(t, func(s *setup) {
		llm = &signalLLM{inner: s.llm, asked: make(chan struct{})}
		s.llm, s.tts = llm, gateTTS{inner: s.tts, open: llm.asked}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	wantFrames(t, h.conn.all(), fullTurn...)
}

// a turn that says nothing needs no tts: a silent volume turn succeeds while
// tts is down, and a failed open counts only once there is speech to say
func TestSilentTurnWithoutTTS(t *testing.T) {
	var ran atomic.Int32
	volume := agent.Tool{
		Name: "device_volume", Input: `{"type":"object"}`, Effects: agent.Write, Output: agent.Trusted, SilentOnSuccess: true,
		Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			ran.Add(1)
			return json.RawMessage(`{"level":20}`), nil
		},
	}
	h := newHarness(t, func(s *setup) {
		s.tts, s.tools = failTTS{}, []agent.Tool{volume}
		s.llm = &scriptLLM{chunks: []providers.LLMChunk{
			{ToolCallStart: true},
			{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "device_volume", Arguments: json.RawMessage(`{"level":20}`)}}},
		}}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	if line := h.waitTurn("s1"); line["code"] != "ok" {
		t.Fatalf("turn %v", line)
	}
	wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "close:")
	if n := ran.Load(); n != 1 {
		t.Fatalf("volume ran %d times", n)
	}
}

// the session reads the settings and the mode once a turn and hands them to
// the agent, which reads neither again
func TestTurnReadsSettingsOnce(t *testing.T) {
	h := newHarness(t)
	h.start("s1")
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	h.waitTurn("s1")
	if n, m := h.settingsReads.Load(), h.modes.gets.Load(); n != 1 || m != 1 {
		t.Fatalf("%d settings reads, %d mode reads, want 1 each", n, m)
	}
}

// a discard opens the next tts stream at once, so the next sentence does
// not wait on a handshake
func TestDiscardDialsAhead(t *testing.T) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	dialed := make(chan int, 1)
	h := newHarness(t, func(s *setup) {
		s.tts = spy
		s.agent = func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "First sentence. "}
			out <- agent.Out{Discard: true}
			// taken only once the discard was handled
			out <- agent.Out{}
			deadline := time.Now().Add(2 * time.Second)
			for spy.streams() < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			dialed <- spy.streams()
			out <- agent.Out{Speech: "Second one."}
			return agent.Result{}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	if n := <-dialed; n != 2 {
		t.Fatalf("%d tts streams before the next sentence, want 2", n)
	}
}
