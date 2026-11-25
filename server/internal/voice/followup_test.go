package voice

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var followUpOn = settings.FollowUp{Enabled: true, WindowMs: 7000, Limit: 3}

// M6a-D5, Ruling M6a-R1: the server offers a follow-up only when every fact holds
func TestFollowUpDecision(t *testing.T) {
	allowed := followUpFacts{
		settings: followUpOn, trigger: cadencev2.Trigger_TRIGGER_WAKE, spoke: true, chain: 2,
	}
	if ok, window := followUp(allowed); !ok || window != 7000 {
		t.Fatalf("all facts hold: %v %d", ok, window)
	}
	for _, trig := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_BUTTON, cadencev2.Trigger_TRIGGER_FOLLOW_UP} {
		f := allowed
		f.trigger = trig
		if ok, _ := followUp(f); !ok {
			t.Errorf("%v refused", trig)
		}
	}
	for name, edit := range map[string]func(*followUpFacts){
		"disabled":          func(f *followUpFacts) { f.settings.Enabled = false },
		"event trigger":     func(f *followUpFacts) { f.trigger = cadencev2.Trigger_TRIGGER_EVENT },
		"no trigger":        func(f *followUpFacts) { f.trigger = cadencev2.Trigger_TRIGGER_UNSPECIFIED },
		"error code":        func(f *followUpFacts) { f.code = providers.CodeTTSFailed },
		"silent reply":      func(f *followUpFacts) { f.spoke = false },
		"stop request":      func(f *followUpFacts) { f.stop = true },
		"echo":              func(f *followUpFacts) { f.echo = true },
		"chain at limit":    func(f *followUpFacts) { f.chain = 3 },
		"chain past limit":  func(f *followUpFacts) { f.chain = 4 },
		"limit of one used": func(f *followUpFacts) { f.settings.Limit, f.chain = 1, 1 },
	} {
		f := allowed
		edit(&f)
		if ok, window := followUp(f); ok || window != 0 {
			t.Errorf("%s: %v %d", name, ok, window)
		}
	}
}

// turn runs one session to its end and returns its close
func (h *harness) turn(id string, trigger cadencev2.Trigger) *cadencev2.SessionClose {
	h.t.Helper()
	h.startAs(id, trigger)
	h.mic(id, 3, 1)
	c := h.waitClose(id)
	h.waitTurn(id)
	return c
}

func offered(c *cadencev2.SessionClose) bool {
	return c.GetCode() == "" && c.GetFollowUpAllowed() && c.GetFollowUpWindowMs() == 7000
}

func refused(c *cadencev2.SessionClose) bool {
	return !c.GetFollowUpAllowed() && c.GetFollowUpWindowMs() == 0
}

// M6a-D5 decision 3: a wake or press starts the chain, a follow-up that takes the offer counts on, and the limit's
// last follow-up offers none
func TestFollowUpChain(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.values.FollowUp = followUpOn })
	fu := cadencev2.Trigger_TRIGGER_FOLLOW_UP
	if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
		t.Fatalf("wake close %v", c)
	}
	for _, id := range []string{"f1", "f2"} {
		if c := h.turn(id, fu); !offered(c) {
			t.Fatalf("%s close %v", id, c)
		}
	}
	if c := h.turn("f3", fu); c.GetCode() != "" || !refused(c) {
		t.Fatalf("the limit's last follow-up %v", c)
	}
	// a follow-up nobody offered (an old or odd device) is answered and offers none
	if c := h.turn("f4", fu); c.GetCode() != "" || !refused(c) {
		t.Fatalf("unsolicited follow-up %v", c)
	}
	// a press starts the chain over
	if c := h.turn("b", cadencev2.Trigger_TRIGGER_BUTTON); !offered(c) {
		t.Fatalf("press close %v", c)
	}
	if c := h.turn("g1", fu); !offered(c) {
		t.Fatalf("g1 close %v", c)
	}
	// the offer goes with the next start, whatever its trigger: a wake takes it and starts over
	if c := h.turn("w2", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
		t.Fatalf("w2 close %v", c)
	}
	for _, id := range []string{"h1", "h2"} {
		if c := h.turn(id, fu); !offered(c) {
			t.Fatalf("%s close %v", id, c)
		}
	}
	if c := h.turn("h3", fu); !refused(c) {
		t.Fatalf("h3 close %v", c)
	}
	// each turn's line names its trigger, so a chain reads off the log
	for id, want := range map[string]string{"w": "TRIGGER_WAKE", "f1": "TRIGGER_FOLLOW_UP", "b": "TRIGGER_BUTTON"} {
		if got := h.turnLine(id)["trigger"]; got != want {
			t.Fatalf("%s turn trigger %v, want %s", id, got, want)
		}
	}
}

// a link close clears the device's offer, and with it the chain
func TestLinkCloseClearsTheOffer(t *testing.T) {
	for name, unlink := range map[string]func(h *harness){
		"closed":   func(h *harness) { h.c.Disconnected(dev) },
		"replaced": func(h *harness) { h.c.Linked(context.Background(), h.conn) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) { s.values.FollowUp = followUpOn })
			if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
				t.Fatalf("wake close %v", c)
			}
			unlink(h)
			if c := h.turn("f", cadencev2.Trigger_TRIGGER_FOLLOW_UP); !refused(c) {
				t.Fatalf("follow-up after the link closed %v", c)
			}
		})
	}
}

// no offer without a spoken reply, after an error, or with the setting off
func TestFollowUpNeedsASpokenReply(t *testing.T) {
	for name, edit := range map[string]func(*setup){
		"silent reply": func(s *setup) {
			s.agent = func(context.Context, agent.Turn, chan<- agent.Out) agent.Result { return agent.Result{Silent: true} }
		},
		"tts failed": func(s *setup) { s.tts = failTTS{} },
		"disabled":   func(s *setup) { s.values.FollowUp.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) { s.values.FollowUp = followUpOn }, edit)
			if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !refused(c) {
				t.Fatalf("close %v", c)
			}
		})
	}
}

// stepLLM answers each request with the next step's chunks
type stepLLM struct {
	steps [][]providers.LLMChunk
	n     atomic.Int32
}

func (l *stepLLM) Stream(context.Context, providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	step := l.steps[min(int(l.n.Add(1)), len(l.steps))-1]
	out := make(chan providers.LLMChunk, len(step))
	for _, c := range step {
		out <- c
	}
	close(out)
	return out, nil
}

// a follow-up hears whoever talks after a reply, so its turn keeps no woken rights (rule 7, agent.Woken)
func TestFollowUpTurnIsNotWoken(t *testing.T) {
	var ran atomic.Int32
	raise := agent.Tool{
		Name: "raise", Input: `{"type":"object"}`, Effects: agent.Write, Output: agent.Trusted, RequiresWokenSession: true,
		Run: func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) {
			ran.Add(1)
			return json.RawMessage(`{"level":80}`), nil
		},
	}
	call := []providers.LLMChunk{
		{ToolCallStart: true},
		{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "raise", Arguments: json.RawMessage(`{}`)}}},
	}
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	h := newHarness(t, func(s *setup) {
		s.values.FollowUp = followUpOn
		s.tts, s.tools = spy, []agent.Tool{raise}
		s.llm = &stepLLM{steps: [][]providers.LLMChunk{call, {{Text: "Done."}}, call, {{Text: "Wake me first."}}}}
	})
	if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
		t.Fatalf("wake close %v", c)
	}
	h.turn("f", cadencev2.Trigger_TRIGGER_FOLLOW_UP)
	if n := ran.Load(); n != 1 {
		t.Fatalf("the woken tool ran %d times, want only in the wake turn", n)
	}
	if said := strings.Join(spy.said(), "|"); said != "Done.|Wake me first." {
		t.Fatalf("spoke %q", said)
	}
}
