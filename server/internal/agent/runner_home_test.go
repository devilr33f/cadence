package agent_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const bedLine = `- light.bed (light, Bedroom) "Bed lamp": `

func bedroom() *homeFake {
	return &homeFake{view: homeView(true, []ha.Area{{ID: "bedroom", Name: "Bedroom"}}, ent("light.bed", "bedroom", "Bed lamp", "off"))}
}

// homeHarness is a harness whose device sees Home Assistant and is offered
// ha_control, with the bedroom lamp as its favorite.
func homeHarness(t *testing.T, home agent.HomeView, u agent.HomeUsage) *harness {
	h := newHarness(t, llmStep{text: "Fine."})
	h.values.HomeAssistant, h.values.HAFavorites = "home", []string{"light.bed"}
	control, _ := fakeTool(agent.HAControl, agent.Write, agent.Trusted, `{}`, nil)
	h.tools = []agent.Tool{control}
	h.home = agent.HomeNotes(home, u)
	return h
}

func wantNoHomeText(t *testing.T, h *harness) {
	t.Helper()
	for _, text := range []string{"light.bed", "Bed lamp", "Bedroom"} {
		if strings.Contains(h.logs.String(), text) {
			t.Fatalf("home text in the log: %s", h.logs.String())
		}
	}
}

// boundedRun runs a turn under a 3 s bound of its own, so a note that
// outstays its budget fails the test instead of hanging it.
func boundedRun(h *harness) (agent.Result, time.Duration) {
	h.limits = []time.Duration{10 * time.Second, 100 * time.Millisecond, 8 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	res, _ := h.runIn(ctx, wake, "hello")
	return res, time.Since(start)
}

// the note carries live state, so it comes after the recalled memories and
// before the tasks board, out of the cached prefix (Ruling M5c-R2), and it is
// context, never part of the turn
func TestHomeNotePlacement(t *testing.T) {
	h := homeHarness(t, bedroom(), &usageFake{})
	h.convs.history = []providers.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "sure"}}
	h.memories = &recallFake{res: memory.Recalled{Memories: []memory.Memory{mem("fact", "Likes tea."), mem("fact", "Has a cat.")}, Pinned: 1}}
	h.tasks = &tasksFake{list: someTasks()[:1]}
	if res, _ := h.run(wake, "hello"); res != (agent.Result{}) {
		t.Fatalf("result %+v", res)
	}
	msgs := h.requests(1)[0].Messages
	// persona + rules, pinned notes, history, recalled notes, home, tasks, volatile note, the user's words
	if len(msgs) != 9 {
		t.Fatalf("messages %+v", msgs)
	}
	if want := homeHeader + "\n" + bedLine + "off"; msgs[5].Role != "system" || msgs[5].Content != want {
		t.Fatalf("home note %q", msgs[5].Content)
	}
	if msgs[1].Content != notesHeader+"\n- [fact] Likes tea." || msgs[2].Content != "earlier" ||
		msgs[4].Content != notesHeader+"\n- [fact] Has a cat." || !strings.HasPrefix(msgs[6].Content, "Background tasks") ||
		!strings.HasPrefix(msgs[7].Content, "Local time:") || msgs[8].Content != "hello" {
		t.Fatalf("order %+v", msgs)
	}
	for _, m := range h.convs.stored[0].msgs {
		if strings.Contains(m.Content, "Home Assistant") {
			t.Fatalf("stored %+v", h.convs.stored[0].msgs)
		}
	}
	wantNoHomeText(t, h)
}

// a state change between two turns changes the note and nothing before it,
// so the provider's cached prefix holds
func TestHomeNoteLeavesThePrefixAlone(t *testing.T) {
	home := bedroom()
	h := homeHarness(t, home, &usageFake{})
	h.convs.history = []providers.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "sure"}}
	h.memories = &recallFake{res: memory.Recalled{Memories: []memory.Memory{mem("fact", "Likes tea.")}, Pinned: 1}}
	h.run(wake, "hello")
	home.setState("light.bed", "on")
	h.run(wake, "hello")
	reqs := h.requests(2)
	first, second := reqs[0].Messages, reqs[1].Messages
	if len(first) != 7 || len(second) != 7 {
		t.Fatalf("messages %+v\n%+v", first, second)
	}
	if !reflect.DeepEqual(first[:4], second[:4]) {
		t.Fatalf("the prefix changed:\n%+v\n%+v", first[:4], second[:4])
	}
	if first[4].Content != homeHeader+"\n"+bedLine+"off" || second[4].Content != homeHeader+"\n"+bedLine+"on" {
		t.Fatalf("home notes %q, %q", first[4].Content, second[4].Content)
	}
}

// the note is built only for a turn ha_control is offered in: never an
// event turn, nor one whose mode or device leaves the tool out; then neither
// the view nor the usage store is read
func TestNoHomeNote(t *testing.T) {
	noTool := func(h *harness) { h.tools[0].Available = func(settings.Values) bool { return false } }
	modeWithout := func(h *harness) {
		m := fastMode
		m.Tools = []string{"weather"}
		h.modes = modeTable{"fast": m, "smart": smartMode}
	}
	for name, c := range map[string]struct {
		home    *homeFake
		setup   func(*harness)
		trigger cadencev2.Trigger
		reads   bool   // the usage store is read
		code    string // logged; "": nothing logged
	}{
		"unconfigured": {bedroom(), func(h *harness) { h.values.HomeAssistant = "" }, wake, false, ""},
		"empty":        {bedroom(), func(h *harness) { h.values.HAFavorites = nil }, wake, true, ""},
		"unavailable":  {&homeFake{err: ha.ErrUnavailable}, func(*harness) {}, wake, false, "unavailable"},
		// even a tool offered in event turns: they never control the home
		"event turn":              {bedroom(), func(h *harness) { h.tools[0].EventTurns = true }, cadencev2.Trigger_TRIGGER_EVENT, false, ""},
		"mode without ha tools":   {bedroom(), modeWithout, wake, false, ""},
		"device without ha tools": {bedroom(), func(h *harness) { h.values.Tools = []string{"weather"} }, wake, false, ""},
		"ha_control unavailable":  {bedroom(), noTool, wake, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			u := &usageFake{}
			h := homeHarness(t, c.home, u)
			c.setup(h)
			res, outs := h.run(c.trigger, "hello")
			if res != (agent.Result{}) {
				t.Fatalf("result %+v", res)
			}
			wantSpeech(t, outs, "Fine.")
			if msgs := h.requests(1)[0].Messages; len(msgs) != 3 {
				t.Fatalf("messages %+v", msgs)
			}
			if read := u.queries() > 0; read != c.reads {
				t.Fatalf("%d usage queries", u.queries())
			}
			logged := strings.Contains(h.logs.String(), `"msg":"ha_context_failed","code":"`+c.code+`"`)
			if c.code == "" {
				logged = strings.Contains(h.logs.String(), "ha_context_failed")
			}
			if logged != (c.code != "") {
				t.Fatalf("log %s", h.logs.String())
			}
		})
	}
	// without Home Assistant set up there is no dependency at all
	h := newHarness(t, llmStep{text: "Fine."})
	h.values.HomeAssistant, h.values.HAFavorites = "home", []string{"light.bed"}
	h.run(wake, "hello")
	if msgs := h.requests(1)[0].Messages; len(msgs) != 3 {
		t.Fatalf("messages %+v", msgs)
	}
}

// the view and both usage reads share 300 ms; a slow store costs the turn no
// more than that and the note only its ranking (M5c-R7), a dependency that
// overstays drops the note
func TestHomeNoteBudget(t *testing.T) {
	degraded := homeHeader + "\n" + bedLine + "off"
	t.Run("slow", func(t *testing.T) {
		u := &usageFake{stall: true}
		h := homeHarness(t, bedroom(), u)
		if res, _ := boundedRun(h); res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		if msgs := h.requests(1)[0].Messages; len(msgs) != 4 || msgs[1].Content != degraded {
			t.Fatalf("messages %+v", msgs)
		}
		if left := u.timeLeft(); len(left) != 2 || left[0] > 300*time.Millisecond || left[0] < 200*time.Millisecond {
			t.Fatalf("the usage reads had %v left", left)
		}
		if !strings.Contains(h.logs.String(), `"msg":"ha_context_failed","code":"timeout"`) {
			t.Fatalf("log %s", h.logs.String())
		}
		wantNoHomeText(t, h)
	})
	t.Run("stuck store", func(t *testing.T) {
		u := &usageFake{stuck: make(chan struct{})}
		t.Cleanup(func() { close(u.stuck) })
		h := homeHarness(t, bedroom(), u)
		h.homeBound = 50 * time.Millisecond
		if res, d := boundedRun(h); res != (agent.Result{}) || d > time.Second {
			t.Fatalf("result %+v after %v", res, d)
		}
		if msgs := h.requests(1)[0].Messages; len(msgs) != 4 || msgs[1].Content != degraded {
			t.Fatalf("messages %+v", msgs)
		}
		if !strings.Contains(h.logs.String(), `"msg":"ha_context_failed","code":"timeout"`) {
			t.Fatalf("log %s", h.logs.String())
		}
	})
	t.Run("store error", func(t *testing.T) {
		h := homeHarness(t, bedroom(), &usageFake{err: &ha.Error{Code: "store"}})
		h.run(wake, "hello")
		if msgs := h.requests(1)[0].Messages; len(msgs) != 4 || msgs[1].Content != degraded {
			t.Fatalf("messages %+v", msgs)
		}
		if !strings.Contains(h.logs.String(), `"level":"WARN","msg":"ha_context_failed","code":"store"`) {
			t.Fatalf("log %s", h.logs.String())
		}
	})
	t.Run("stuck dependency", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		h := homeHarness(t, bedroom(), &usageFake{})
		h.home = func(context.Context, string, settings.Values) (string, error) {
			<-release
			return "late", nil
		}
		h.homeBound = 50 * time.Millisecond
		if res, d := boundedRun(h); res != (agent.Result{}) || d > time.Second {
			t.Fatalf("result %+v after %v", res, d)
		}
		if msgs := h.requests(1)[0].Messages; len(msgs) != 3 {
			t.Fatalf("messages %+v", msgs)
		}
		if !strings.Contains(h.logs.String(), `"msg":"ha_context_failed","code":"timeout"`) {
			t.Fatalf("log %s", h.logs.String())
		}
	})
	t.Run("panic", func(t *testing.T) {
		h := homeHarness(t, bedroom(), &usageFake{})
		h.home = func(context.Context, string, settings.Values) (string, error) { panic("light.bed Bed lamp") }
		if res, _ := h.run(wake, "hello"); res != (agent.Result{}) {
			t.Fatalf("result %+v", res)
		}
		if msgs := h.requests(1)[0].Messages; len(msgs) != 3 {
			t.Fatalf("messages %+v", msgs)
		}
		if !strings.Contains(h.logs.String(), `"msg":"ha_context_failed","code":"panic"`) {
			t.Fatalf("log %s", h.logs.String())
		}
		wantNoHomeText(t, h)
	})
}

// Home Assistant down is one warning, not one per turn: unavailable warns
// when the last note's outcome was another, a store's failures every time
func TestHomeFailureLogs(t *testing.T) {
	warnings := func(h *harness, code string) int {
		return strings.Count(h.logs.String(), `"level":"WARN","msg":"ha_context_failed","code":"`+code+`"`)
	}
	turns := func(h *harness, r *agent.Runner, n int) {
		for range n {
			r.Run(context.Background(), h.turn(wake, "hello"), h.out)
			h.drain()
		}
	}
	home := bedroom()
	home.setErr(ha.ErrUnavailable)
	h := homeHarness(t, home, &usageFake{})
	r := h.runner()
	turns(h, r, 2)
	if n := warnings(h, "unavailable"); n != 1 {
		t.Fatalf("%d warnings after two turns: %s", n, h.logs.String())
	}
	home.setErr(nil)
	turns(h, r, 1)
	home.setErr(ha.ErrUnavailable)
	turns(h, r, 1)
	if n := warnings(h, "unavailable"); n != 2 {
		t.Fatalf("%d warnings once it came back and went: %s", n, h.logs.String())
	}

	h = homeHarness(t, bedroom(), &usageFake{err: &ha.Error{Code: "store"}})
	r = h.runner()
	turns(h, r, 2)
	if n := warnings(h, "store"); n != 2 {
		t.Fatalf("%d store warnings after two turns: %s", n, h.logs.String())
	}
}
