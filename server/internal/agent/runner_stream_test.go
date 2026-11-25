package agent_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestFinalAnswerStreams(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, llmStep{text: "It is noon.", hold: release})
	done := make(chan agent.Result, 1)
	go func() { done <- h.runner().Run(context.Background(), h.turn(wake, "what time is it"), h.out) }()
	// the first fragment is said while the stream is still open
	select {
	case o := <-h.out:
		if o != (agent.Out{Speech: "It "}) {
			t.Fatalf("first fragment %+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no speech before the stream ended")
	}
	select {
	case res := <-done:
		t.Fatalf("turn ended before its stream: %+v", res)
	default:
	}
	close(release)
	if res := <-done; res != (agent.Result{}) {
		t.Fatalf("result %+v", res)
	}
	wantOuts(t, h.said(), agent.Out{Speech: "is noon."})
	if got := h.convs.stored[0].msgs; len(got) != 2 || got[1].Content != "It is noon." {
		t.Fatalf("committed %+v", got)
	}
}

// M3-R11: the speech ends before the turn is committed, so it never waits on the store
func TestSpeechEndsBeforeCommit(t *testing.T) {
	h := newHarness(t, llmStep{text: "All good."})
	h.convs.hold = make(chan struct{})
	done := make(chan agent.Result, 1)
	go func() { done <- h.runner().Run(context.Background(), h.turn(wake, "how are you"), h.out) }()
	var outs []agent.Out
	for ended := false; !ended; {
		select {
		case o := <-h.out:
			ended = o.End
			outs = append(outs, o)
		case <-time.After(5 * time.Second):
			t.Fatalf("speech %+v not ended while the commit waits", outs)
		}
	}
	wantOuts(t, outs, agent.Out{Speech: "All "}, agent.Out{Speech: "good."}, agent.Out{End: true})
	close(h.convs.hold)
	if res := <-done; res != (agent.Result{}) {
		t.Fatalf("result %+v", res)
	}
	if len(h.convs.stored) != 1 {
		t.Fatalf("%d commits", len(h.convs.stored))
	}
	// the end is no speech: a turn that said nothing stays silent
	setVolume := []providers.ToolCall{toolCall("c1", "device_volume", `{"level":20}`)}
	h = newHarness(t, llmStep{calls: setVolume})
	volume, _ := volumeTool()
	h.tools = []agent.Tool{volume}
	if res, outs := h.run(wake, "volume twenty"); res != (agent.Result{Silent: true}) || len(outs) != 0 {
		t.Fatalf("result %+v, said %+v", res, outs)
	}
}

func interimHarness(t *testing.T, mode, text string, noStart bool) (*harness, *atomic.Int32) {
	h := newHarness(t,
		llmStep{text: text, calls: []providers.ToolCall{toolCall("c1", "weather", `{}`)}, noStart: noStart},
		llmStep{text: "Sunny."},
	)
	h.values.Mode = mode
	var saidBefore atomic.Int32
	weather, _ := fakeTool("weather", agent.Read, agent.Trusted, `{}`, func() { saidBefore.Store(int32(len(h.out))) })
	h.tools = []agent.Tool{weather}
	return h, &saidBefore
}

// a step's spoken text ends in whitespace before its tools run, so the next
// step's text never joins its last sentence
func TestInterimSpeechSpeak(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []agent.Out
	}{
		{"Let me check.", []agent.Out{{Speech: "Let "}, {Speech: "me check."}, {Speech: "\n"}}},
		{"Let me check. ", []agent.Out{{Speech: "Let "}, {Speech: "me check. "}}},
	} {
		h, saidBefore := interimHarness(t, "smart", tc.text, false)
		_, outs := h.run(wake, "weather")
		wantOuts(t, outs, append(tc.want, agent.Out{Speech: "Sunny."})...)
		if int(saidBefore.Load()) != len(tc.want) {
			t.Fatalf("%q: %d fragments said before the tool ran, want %d", tc.text, saidBefore.Load(), len(tc.want))
		}
	}
}

func TestInterimSpeechDrop(t *testing.T) {
	// with no ToolCallStart the discard comes once the calls arrive
	for _, noStart := range []bool{false, true} {
		h, saidBefore := interimHarness(t, "fast", "Let me check.", noStart)
		_, outs := h.run(wake, "weather")
		wantOuts(t, outs, agent.Out{Speech: "Let "}, agent.Out{Speech: "me check."}, agent.Out{Discard: true}, agent.Out{Speech: "Sunny."})
		if saidBefore.Load() != 3 {
			t.Fatalf("noStart %v: the discard did not precede the tool", noStart)
		}
		texts := eventsOf(h.events(), "llm_text")
		if len(texts) != 2 || texts[0]["text"] != "Let me check." || texts[0]["spoken"] != false || texts[1]["spoken"] != true {
			t.Fatalf("llm_text events %v", texts)
		}
		// the model still sees what it wrote
		if got := h.convs.stored[0].msgs[1]; got.Content != "Let me check." || len(got.ToolCalls) != 1 {
			t.Fatalf("committed %+v", got)
		}
	}

	// a reply with no tool call is never held back, in drop mode too
	h := newHarness(t, llmStep{text: "Just text."})
	weather, _ := fakeTool("weather", agent.Read, agent.Trusted, `{}`, nil)
	h.tools = []agent.Tool{weather}
	_, outs := h.run(wake, "hi")
	wantOuts(t, outs, speak("Just text.")...)
}

func TestBudgetDeadlineCutsStep(t *testing.T) {
	quick := func(h *harness, maxDuration time.Duration, lim ...time.Duration) {
		mode := fastMode
		mode.MaxDuration = maxDuration
		h.modes["fast"] = mode
		h.limits = lim
		weather, _ := fakeTool("weather", agent.Read, agent.Trusted, `{}`, nil)
		h.tools = []agent.Tool{weather}
	}

	t.Run("forced answer", func(t *testing.T) {
		h := newHarness(t, llmStep{text: "Partial ", block: true}, llmStep{text: "Short answer."})
		quick(h, 150*time.Millisecond, 5*time.Second, 8*time.Second, 2*time.Second)
		res, outs := h.run(wake, "think hard")
		if res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		// the cut step's speech is dropped, and the forced answer said
		wantOuts(t, outs, agent.Out{Speech: "Partial "}, agent.Out{Discard: true}, agent.Out{Speech: "Short "}, agent.Out{Speech: "answer."})
		forced := h.requests(2)[1]
		if forced.ToolChoice != "none" || last(forced.Messages).Content != "Budget spent: answer now in one short sentence." {
			t.Fatalf("forced request %+v", forced)
		}
		if spent := eventsOf(h.events(), "budget_spent"); len(spent) != 1 || spent[0]["reason"] != "seconds" {
			t.Fatalf("budget events %v", spent)
		}
		want := []providers.Message{{Role: "user", Content: "think hard"}, {Role: "assistant", Content: "Short answer."}}
		if got := h.convs.stored[0].msgs; !reflect.DeepEqual(got, want) {
			t.Fatalf("committed %+v", got)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		h := newHarness(t, llmStep{block: true}, llmStep{text: "Quick."})
		reserve := 300 * time.Millisecond
		quick(h, 20*time.Second, 5*time.Second, reserve, 2*time.Second)
		deadline := time.Now().Add(time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		res, outs := h.runIn(ctx, wake, "think hard")
		if res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		wantSpeech(t, outs, "Quick.")
		h.requests(2)
		if at := h.llm.times()[1]; !at.Before(deadline.Add(-reserve / 2)) {
			t.Fatalf("forced call started %v before the caller's deadline, want about %v", deadline.Sub(at), reserve)
		}
	})

	t.Run("first chunk stall", func(t *testing.T) {
		h := newHarness(t, llmStep{block: true})
		quick(h, 20*time.Second, 50*time.Millisecond, 8*time.Second, 8*time.Second)
		if res, _ := h.run(wake, "hello"); res.Code != agent.CodeTimeout {
			t.Fatalf("result %+v", res)
		}
		h.requests(1)
	})

	t.Run("forced call bounded", func(t *testing.T) {
		// the forced call speaks, then stalls: only its own bound ends it
		h := newHarness(t, llmStep{block: true}, llmStep{text: "Thinking ", block: true})
		quick(h, 100*time.Millisecond, 5*time.Second, 8*time.Second, 100*time.Millisecond)
		if res, _ := h.run(wake, "hello"); res.Code != agent.CodeTimeout {
			t.Fatalf("result %+v", res)
		}
		h.requests(2)
	})
}

func TestNoToolLeftKeepsToolList(t *testing.T) {
	h := newHarness(t,
		llmStep{calls: []providers.ToolCall{toolCall("c1", "device_volume", `{"level":150}`)}},
		llmStep{calls: []providers.ToolCall{toolCall("c2", "device_volume", `{"level":150}`)}},
		llmStep{text: "Sorry, no."},
	)
	volume, _ := volumeTool()
	h.tools = []agent.Tool{volume}
	_, outs := h.run(wake, "volume one fifty")
	// nothing may be called now, so the text is said as it comes
	wantOuts(t, outs, speak("Sorry, no.")...)
	r := h.requests(3)[2]
	if r.ToolChoice != "none" || !slices.Equal(toolNames(r.Tools), []string{"device_volume"}) {
		t.Fatalf("tools %v choice %q", toolNames(r.Tools), r.ToolChoice)
	}
}

// a play waits for its outcome: a Holds call outlasts the step's and the
// mode's time, and its time does not count against the mode's
func TestHoldingCallOutlastsStep(t *testing.T) {
	h := newHarness(t, llmStep{calls: []providers.ToolCall{toolCall("c1", "music_play", `{}`)}}, llmStep{text: "Done."})
	mode := fastMode
	mode.MaxDuration = 150 * time.Millisecond
	h.modes["fast"] = mode
	play, ran := fakeTool("music_play", agent.Write, agent.Trusted, `{"playing":true}`, func() {
		time.Sleep(300 * time.Millisecond)
		h.clock.Advance(21 * time.Second)
	})
	play.Holds, play.Timeout = true, 2*time.Second
	h.tools = []agent.Tool{play}
	res, outs := h.run(wake, "play it")
	if res != (agent.Result{}) {
		t.Fatalf("result %+v", res)
	}
	wantSpeech(t, outs, "Done.")
	if len(ran.all()) != 1 {
		t.Fatal("the play did not run")
	}
	reqs := h.requests(2)
	if reqs[1].ToolChoice == "none" || !strings.Contains(last(reqs[1].Messages).Content, `"playing":true`) {
		t.Fatalf("after the play %+v", reqs[1])
	}
	if spent := eventsOf(h.events(), "budget_spent"); len(spent) != 0 {
		t.Fatalf("budget events %v", spent)
	}
}
