package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// voice already read the settings and the mode for the turn: the runner
// reads neither again, unless the mode moved on in the meantime
func TestTurnCarriesSettingsAndMode(t *testing.T) {
	t.Run("used as given", func(t *testing.T) {
		h := newHarness(t, llmStep{text: "Fine."})
		v, mode := h.values, fastMode
		// a read of either would fail the turn
		h.settingsErr, h.modes = errors.New("settings read"), modeTable{}
		tr := h.turn(wake, "hello")
		tr.Settings, tr.Mode = &v, &mode
		if res := h.runner().Run(context.Background(), tr, h.out); res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		if r := h.requests(1)[0]; r.Model != "model-fast-x" {
			t.Fatalf("request %+v", r)
		}
	})
	t.Run("mode moved on", func(t *testing.T) {
		h := newHarness(t, llmStep{text: "Fine."})
		h.state.Set(device, "s0", "smart", turnStart)
		v, mode := h.values, fastMode
		tr := h.turn(wake, "hello")
		tr.Settings, tr.Mode = &v, &mode
		if res := h.runner().Run(context.Background(), tr, h.out); res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		if r := h.requests(1)[0]; r.Reasoning != "medium" {
			t.Fatalf("request %+v", r)
		}
	})
}

// a slow store delays no answer: past its bound the turn runs without history
func TestHistoryReadBounded(t *testing.T) {
	h := newHarness(t, llmStep{text: "Fine."})
	h.convs.stall, h.history = true, 50*time.Millisecond
	// the turn's own bound only stops the test hanging without the history's
	h.limits = []time.Duration{10 * time.Second, 100 * time.Millisecond, 8 * time.Second}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, outs := h.runIn(ctx, wake, "hello")
	if res != (agent.Result{}) {
		t.Fatalf("result %+v", res)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the turn waited %v on the history", d)
	}
	wantSpeech(t, outs, "Fine.")
	if msgs := h.requests(1)[0].Messages; len(msgs) != 3 {
		t.Fatalf("messages %+v", msgs)
	}
	if !strings.Contains(h.logs.String(), `"history_failed":true`) {
		t.Fatalf("log %s", h.logs.String())
	}
}

// voice's turn deadline follows the mode the next turn runs, which a peek
// does not count as the mode's use
func TestPeekMode(t *testing.T) {
	h := newHarness(t)
	h.values.AllowedModes = []string{"fast", "smart", "deep"}
	r := h.runner()
	if m, ok := r.PeekMode(context.Background(), device, h.values); !ok || m.Name != "fast" {
		t.Fatalf("%+v %v", m, ok)
	}
	h.state.Set(device, "s0", "smart", turnStart)
	if m, ok := r.PeekMode(context.Background(), device, h.values); !ok || m.MaxDuration != smartMode.MaxDuration {
		t.Fatalf("voiced: %+v %v", m, ok)
	}
	// a voiced mode without its row falls back as the turn will
	h.state.Set(device, "s0", "deep", turnStart)
	if m, ok := r.PeekMode(context.Background(), device, h.values); !ok || m.Name != "fast" {
		t.Fatalf("missing row: %+v %v", m, ok)
	}
	delete(h.modes, "fast")
	if _, ok := r.PeekMode(context.Background(), device, h.values); ok {
		t.Fatal("a budget without any mode row")
	}
}
