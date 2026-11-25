package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// saidSTT hears the next of its texts in each stream, after three packets
type saidSTT struct {
	mu    sync.Mutex
	texts []string
}

func heardSaying(texts ...string) *saidSTT { return &saidSTT{texts: texts} }

func (s *saidSTT) Start(context.Context, providers.STTOptions) (providers.STTStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.texts[0]
	if len(s.texts) > 1 {
		s.texts = s.texts[1:]
	}
	return &saidStream{text: text, events: make(chan providers.STTEvent, 2)}, nil
}

type saidStream struct {
	text   string
	events chan providers.STTEvent

	mu      sync.Mutex
	packets int
	closed  bool
}

func (s *saidStream) Write([]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("closed")
	}
	s.packets++
	switch s.packets {
	case 1:
		s.events <- providers.STTEvent{Kind: providers.STTSpeechStarted}
	case 3:
		s.events <- providers.STTEvent{Kind: providers.STTFinal, Text: s.text}
	}
	return nil
}

func (s *saidStream) Events() <-chan providers.STTEvent { return s.events }

func (s *saidStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

// countingAgent answers every turn with reply and counts the turns it ran
type countingAgent struct {
	reply string
	runs  atomic.Int32
}

func (a *countingAgent) run(ctx context.Context, tr agent.Turn, out chan<- agent.Out) agent.Result {
	a.runs.Add(1)
	return says(a.reply)(ctx, tr, out)
}

func (h *harness) logHas(msg string) map[string]any {
	sc := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == msg {
			return m
		}
	}
	return nil
}

func (h *harness) logFree(t *testing.T, texts ...string) {
	t.Helper()
	logs := strings.ToLower(h.logs.String())
	for _, s := range texts {
		for _, w := range words(s) {
			if len([]rune(w)) >= 4 && strings.Contains(logs, w) {
				t.Fatalf("log carries %q", w)
			}
		}
	}
}

// M6a-D6: stop words alone end the session silently, with the media paused first when it plays
func TestStopRequestEndsSilently(t *testing.T) {
	for name, tc := range map[string]struct {
		lang, text string
		playing    bool
		pause      func(ctx context.Context) error
		pauses     int
		paused     bool
	}{
		"ru idle":             {"ru", "Стоп!", false, nil, 0, false},
		"ru playing":          {"ru", "стоп стоп", true, nil, 1, true},
		"capitals, two words": {"ru", "ХВАТИТ, пауза", true, nil, 1, true},
		"ru остановись":       {"ru", "Остановись!", true, nil, 1, true},
		"ru приостанови":      {"ru", "приостанови", false, nil, 0, false},
		"en playing":          {"en", "Stop.", true, nil, 1, true},
		"en pause":            {"en", "Pause.", true, nil, 1, true},
		"pause fails":         {"en", "stop", true, func(context.Context) error { return errors.New("x") }, 1, false},
		"pause never ends":    {"en", "stop", true, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			a := &countingAgent{reply: "Sure."}
			h := newHarness(t, func(s *setup) {
				s.values.Language, s.values.FollowUp = tc.lang, followUpOn
				s.stt, s.agent = heardSaying(tc.text), a.run
				s.playing = func() bool { return tc.playing }
				s.pause = tc.pause
				s.lim = func(l *limits) { l.pause = 50 * time.Millisecond }
			})
			c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE)
			if c.GetCode() != "stop_requested" || !refused(c) {
				t.Fatalf("close %v", c)
			}
			if n := a.runs.Load(); n != 0 {
				t.Fatalf("agent ran %d times", n)
			}
			wantFrames(t, h.conn.all(), "SPEECH_STARTED", "close:stop_requested")
			h.mu.Lock()
			pauses := h.pauses
			h.mu.Unlock()
			if pauses != tc.pauses {
				t.Fatalf("%d pauses", pauses)
			}
			line := h.logHas("stop_requested")
			if line == nil || line["device_id"] != dev || line["session_id"] != "s1" || line["paused"] != tc.paused {
				t.Fatalf("log %v", line)
			}
			if heard := h.heard(); len(heard) != 1 || heard[0] {
				t.Fatalf("a user turn ran: %v", heard)
			}
			if tc.lang == "ru" {
				h.logFree(t, tc.text)
			}
		})
	}
	t.Run("stop the music is a question", func(t *testing.T) {
		a := &countingAgent{reply: "Sure."}
		h := newHarness(t, func(s *setup) { s.stt, s.agent = heardSaying("stop the music"), a.run })
		if c := h.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "" || a.runs.Load() != 1 {
			t.Fatalf("close %v, %d runs", c, a.runs.Load())
		}
	})
}

// pausing never raises the volume, so a follow-up stops the music too
func TestStopRequestInAFollowUp(t *testing.T) {
	a := &countingAgent{reply: "Here is a song for you."}
	h := newHarness(t, func(s *setup) {
		s.values.FollowUp = followUpOn
		s.stt, s.agent = heardSaying("play something", "stop"), a.run
		s.playing = func() bool { return true }
	})
	if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
		t.Fatalf("wake close %v", c)
	}
	if c := h.turn("f", cadencev2.Trigger_TRIGGER_FOLLOW_UP); c.GetCode() != "stop_requested" || !refused(c) {
		t.Fatalf("follow-up close %v", c)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if a.runs.Load() != 1 || h.pauses != 1 {
		t.Fatalf("%d runs, %d pauses", a.runs.Load(), h.pauses)
	}
}

// M6a-D6 decision 5: a follow-up that hears only the previous reply's own words ends silently
func TestFollowUpEcho(t *testing.T) {
	const reply = "It is sunny and warm today, enjoy the walk."
	for name, tc := range map[string]struct {
		heard   string
		trigger cadencev2.Trigger
		echo    bool
	}{
		"echo":                 {"sunny and warm today", cadencev2.Trigger_TRIGGER_FOLLOW_UP, true},
		"one misheard in four": {"sunny and worm today", cadencev2.Trigger_TRIGGER_FOLLOW_UP, true},
		"speech":               {"and what about tomorrow", cadencev2.Trigger_TRIGGER_FOLLOW_UP, false},
		"a wake is never echo": {"sunny and warm today", cadencev2.Trigger_TRIGGER_WAKE, false},
	} {
		t.Run(name, func(t *testing.T) {
			a := &countingAgent{reply: reply}
			h := newHarness(t, func(s *setup) {
				s.values.FollowUp = followUpOn
				s.stt, s.agent = heardSaying("what is the weather", tc.heard), a.run
			})
			if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
				t.Fatalf("wake close %v", c)
			}
			c := h.turn("f", tc.trigger)
			if !tc.echo {
				if c.GetCode() != "" || a.runs.Load() != 2 {
					t.Fatalf("close %v, %d runs", c, a.runs.Load())
				}
				return
			}
			if c.GetCode() != "follow_up_echo" || !refused(c) || a.runs.Load() != 1 {
				t.Fatalf("close %v, %d runs", c, a.runs.Load())
			}
			wantFrames(t, h.conn.of("f"), "SPEECH_STARTED", "close:follow_up_echo")
			if line := h.waitTurn("f"); line["code"] != "follow_up_echo" {
				t.Fatalf("turn %v", line)
			}
			h.logFree(t, reply, tc.heard)
		})
	}
}

// the echo kept for a follow-up is what tts was given, after the guard
func TestFollowUpEchoOfTheGuardedReply(t *testing.T) {
	a := &countingAgent{reply: "Сделала громче, слушай."}
	h := newHarness(t, func(s *setup) {
		s.values.Language, s.values.FollowUp = "ru", followUpOn
		s.stt, s.agent = heardSaying("сделай громче", "сделала сильнее слушай"), a.run
	})
	if c := h.turn("w", cadencev2.Trigger_TRIGGER_WAKE); !offered(c) {
		t.Fatalf("wake close %v", c)
	}
	if c := h.turn("f", cadencev2.Trigger_TRIGGER_FOLLOW_UP); c.GetCode() != "follow_up_echo" {
		t.Fatalf("close %v", c)
	}
}

// the previous reply's words are kept until the next session ends, and a link close drops them
func TestEchoDroppedWithTheLink(t *testing.T) {
	a := &countingAgent{reply: "It is sunny and warm today."}
	h := newHarness(t, func(s *setup) {
		s.values.FollowUp = followUpOn
		s.stt, s.agent = heardSaying("what is the weather", "sunny and warm today"), a.run
	})
	h.turn("w", cadencev2.Trigger_TRIGGER_WAKE)
	h.c.Disconnected(dev)
	h.c.mu.Lock()
	_, offer := h.c.offers[dev]
	_, echo := h.c.echoes[dev]
	h.c.mu.Unlock()
	if offer || echo {
		t.Fatalf("kept the offer %v, the echo %v", offer, echo)
	}
	if c := h.turn("f", cadencev2.Trigger_TRIGGER_FOLLOW_UP); c.GetCode() != "" || a.runs.Load() != 2 {
		t.Fatalf("close %v, %d runs", c, a.runs.Load())
	}
}

// a stop request also ends the announcement the device plays, which the session began beside
func TestStopRequestEndsTheAnnouncement(t *testing.T) {
	r := newAnnounceRig(t, func(s *setup) {
		s.ticks = make(chan time.Time)
		s.stt = heardSaying("stop")
	})
	id := r.announce(t, `{"text":"one"}`)
	waitFor(t, "the first burst", func() bool { return r.audioIn(id) == burstPackets })
	if c := r.turn("s1", cadencev2.Trigger_TRIGGER_WAKE); c.GetCode() != "stop_requested" {
		t.Fatalf("close %v", c)
	}
	wantFrames(t, r.stream(id), "stream_start", "audio×10", "stream_end")
	if line := r.waitLine(t, id); line["code"] != "canceled" {
		t.Fatalf("log %v", line)
	}
	if line := r.logHas("stop_requested"); line == nil || line["announcement_stopped"] != true {
		t.Fatalf("log %v", line)
	}
}

// a stop the server asks for (a screen's assistant.stop) closes the session on the device with stopped, no
// follow-up, mid-reply or while it listens; the device's next session is not stopped by it
func TestServerStop(t *testing.T) {
	t.Run("speaking", func(t *testing.T) {
		h := newHarness(t, func(s *setup) { s.pauseFirst = true; s.values.FollowUp = followUpOn })
		h.start("a")
		h.mic("a", 3, 1)
		waitFor(t, "a's burst", func() bool { return h.audioOf("a") == 10 })
		if h.c.Stop(dev, "other") {
			t.Fatal("stopped a session it did not name")
		}
		if !h.c.Stop(dev, "a") {
			t.Fatal("not stopped")
		}
		c := h.waitClose("a")
		if c.GetCode() != "stopped" || c.GetFollowUpAllowed() {
			t.Fatalf("close %v", c)
		}
		if line := h.waitTurn("a"); line["code"] != "stopped" {
			t.Fatalf("turn %v", line)
		}
		wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×10", "close:stopped")
		if h.c.Stop(dev, "a") || h.c.SessionActive(dev) {
			t.Fatal("stopped twice")
		}
		if ended := h.endedSessions(); !slices.Equal(ended, []string{"a"}) {
			t.Fatalf("ended %v", ended)
		}
	})
	t.Run("listening, any session", func(t *testing.T) {
		h := newHarness(t)
		h.start("b")
		if !h.c.Stop(dev, "") {
			t.Fatal("not stopped")
		}
		if c := h.waitClose("b"); c.GetCode() != "stopped" {
			t.Fatalf("close %v", c)
		}
		h.waitTurn("b")
		h.start("c")
		h.mic("c", 3, 1)
		if c := h.waitClose("c"); c.GetCode() != "" {
			t.Fatalf("the next session %v", c)
		}
	})
}
