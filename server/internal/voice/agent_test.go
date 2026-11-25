package voice

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestVoiceUsesRunner(t *testing.T) {
	var mu sync.Mutex
	var turns []agent.Turn
	fine := says("It is test time. ", "All good.")
	smart := agent.Mode{Name: "smart", MaxDuration: 90 * time.Second}
	h := newHarness(t, func(s *setup) {
		s.values.Language = "ru"
		s.mode = func(string, settings.Values) (agent.Mode, bool) { return smart, true }
		s.agent = func(ctx context.Context, tr agent.Turn, out chan<- agent.Out) agent.Result {
			mu.Lock()
			turns = append(turns, tr)
			mu.Unlock()
			return fine(ctx, tr, out)
		}
	})
	h.startAs("s1", cadencev2.Trigger_TRIGGER_WAKE)
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	if line := h.waitTurn("s1"); line["code"] != "ok" {
		t.Fatalf("turn %v", line)
	}
	wantFrames(t, h.conn.all(), fullTurn...)
	mu.Lock()
	defer mu.Unlock()
	want := agent.Turn{ID: "s1", DeviceID: dev, SessionID: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE, Text: transcript, Language: "ru"}
	if len(turns) != 1 {
		t.Fatalf("turns %+v", turns)
	}
	// what the session read for the turn goes with it, so the agent reads none of it again
	got := turns[0]
	if got.Settings == nil || got.Settings.Language != "ru" || got.Settings.LLM != defaultValues.LLM ||
		got.Mode == nil || got.Mode.Name != smart.Name {
		t.Fatalf("turn settings %+v, mode %+v", got.Settings, got.Mode)
	}
	if got.Settings, got.Mode = nil, nil; got != want {
		t.Fatalf("turn %+v, want %+v", got, want)
	}
	if ended := h.endedSessions(); !slices.Equal(ended, []string{"s1"}) {
		t.Fatalf("sessions ended %q", ended)
	}
}

// spec §3.8, M3-R6: a turn that said nothing closes the session without a
// stream, also when all it said was dropped before tts heard it
func TestSilentTurnSendsNoStream(t *testing.T) {
	for name, run := range map[string]agentFunc{
		"silent": func(context.Context, agent.Turn, chan<- agent.Out) agent.Result {
			return agent.Result{Silent: true}
		},
		"unfinished sentence discarded": func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "Sure, setting"}
			out <- agent.Out{Discard: true}
			return agent.Result{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recTTS{}
			h := newHarness(t, func(s *setup) { s.tts, s.agent = rec, run })
			h.start("s1")
			h.mic("s1", 3, 1)
			if c := h.waitClose("s1"); c.GetCode() != "" || c.GetFollowUpAllowed() {
				t.Fatalf("close %v", c)
			}
			if line := h.waitTurn("s1"); line["code"] != "ok" {
				t.Fatalf("turn %v", line)
			}
			wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "close:")
			if said := rec.said(); len(said) != 0 {
				t.Fatalf("spoke %q", said)
			}
		})
	}
}

// speech the agent sends before its turn ends plays while the turn goes on
func TestInterimSpokenFirst(t *testing.T) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	heard := make(chan struct{})
	h := newHarness(t, func(s *setup) {
		s.tts = spy
		s.agent = func(ctx context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "Let me check. "}
			select {
			case <-heard:
			case <-ctx.Done():
				return agent.Result{Code: agent.CodeCanceled}
			}
			out <- agent.Out{Speech: "It is sunny."}
			return agent.Result{}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	// one 300 ms tone of 15 packets, played while the agent waits
	waitFor(t, "the interim sentence's audio", func() bool { return h.audioOf("s1") == 15 })
	if said := spy.said(); !slices.Equal(said, []string{"Let me check."}) {
		t.Fatalf("spoke %q before the turn went on", said)
	}
	close(heard)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	wantFrames(t, h.conn.all(), fullTurn...)
	if said, want := spy.said(), []string{"Let me check.", " It is sunny."}; !slices.Equal(said, want) {
		t.Fatalf("spoke %q, want %q", said, want)
	}
	if n := spy.streams(); n != 1 {
		t.Fatalf("%d tts streams", n)
	}
}

// M3-R9: a discard drops the unfinished sentence and every packet the device
// was not sent yet; what was sent stays, and later speech plays on a new tts
// stream in the same reply stream
func TestDiscardDropsUnplayedSpeech(t *testing.T) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	ticks := make(chan time.Time)
	burst := make(chan struct{})
	discarded := make(chan struct{})
	h := newHarness(t, func(s *setup) {
		s.tts, s.ticks = spy, ticks
		s.agent = func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "First sentence. Dropped part"}
			<-burst
			out <- agent.Out{Discard: true}
			// taken only once the discard was handled
			out <- agent.Out{Speech: "Second one."}
			close(discarded)
			return agent.Result{}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	// the first sentence is one 300 ms tone: 10 packets go out at once, 5 wait for ticks
	waitFor(t, "the first burst", func() bool { return h.audioOf("s1") == 10 })
	close(burst)
	<-discarded
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case ticks <- time.Now():
			case <-stop:
				return
			}
		}
	}()
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×25", "stream_end", "close:")
	if said, want := spy.said(), []string{"First sentence.", "Second one."}; !slices.Equal(said, want) {
		t.Fatalf("spoke %q, want %q", said, want)
	}
	if n := spy.streams(); n != 2 {
		t.Fatalf("%d tts streams, want 2", n)
	}
	seq := uint32(0)
	for _, e := range h.conn.all() {
		if a := e.GetAudioDown(); a != nil {
			if a.GetSeq() != seq {
				t.Fatalf("audio seq %d, want %d", a.GetSeq(), seq)
			}
			seq++
		}
	}
}

// M3-R11: the agent's End says the held last sentence at once, while the
// turn is still committing; nothing after it is said
func TestEndSpeaksBeforeTurnReturns(t *testing.T) {
	spy := &spyTTS{inner: fakeModule(t, "fake-tts", `{}`).(providers.TTS)}
	played := make(chan struct{})
	h := newHarness(t, func(s *setup) {
		s.tts = spy
		s.agent = func(ctx context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
			out <- agent.Out{Speech: "All good."}
			out <- agent.Out{End: true}
			select {
			case <-played:
			case <-ctx.Done():
			}
			out <- agent.Out{Speech: "Never said."}
			return agent.Result{}
		}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	waitFor(t, "the last sentence's audio", func() bool { return h.audioOf("s1") == 15 })
	close(played)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×15", "stream_end", "close:")
	if said := spy.said(); !slices.Equal(said, []string{"All good."}) {
		t.Fatalf("spoke %q", said)
	}
}

// M3-R9: the two ends of a turn whose speech was all discarded
func TestDiscardedSpeechEndsTurn(t *testing.T) {
	// audio that reached the device stays, the stream ends, and the session closes ok
	t.Run("some sent", func(t *testing.T) {
		ticks := make(chan time.Time)
		burst, discarded := make(chan struct{}), make(chan struct{})
		h := newHarness(t, func(s *setup) {
			s.ticks = ticks
			s.agent = func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
				out <- agent.Out{Speech: "First sentence. "}
				<-burst
				out <- agent.Out{Discard: true}
				// taken only once the discard was handled
				out <- agent.Out{End: true}
				close(discarded)
				return agent.Result{}
			}
		})
		h.start("s1")
		h.mic("s1", 3, 1)
		waitFor(t, "the first burst", func() bool { return h.audioOf("s1") == 10 })
		close(burst)
		<-discarded
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for {
				select {
				case ticks <- time.Now():
				case <-stop:
					return
				}
			}
		}()
		if c := h.waitClose("s1"); c.GetCode() != "" {
			t.Fatalf("closed with %q", c.GetCode())
		}
		wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×10", "stream_end", "close:")
	})
	// tts had the text but no audio reached the device: no stream at all
	t.Run("none sent", func(t *testing.T) {
		rec := &recTTS{}
		given := make(chan struct{})
		h := newHarness(t, func(s *setup) {
			s.tts = rec
			s.agent = func(_ context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
				out <- agent.Out{Speech: "First sentence. "}
				<-given
				out <- agent.Out{Discard: true}
				return agent.Result{}
			}
		})
		h.start("s1")
		h.mic("s1", 3, 1)
		waitFor(t, "tts given the sentence", func() bool { return len(rec.said()) == 1 })
		close(given)
		if c := h.waitClose("s1"); c.GetCode() != "" {
			t.Fatalf("closed with %q", c.GetCode())
		}
		wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "close:")
	})
}

// M3-R8: the turn deadline is the mode's time plus agent.MaxHold and 15 s, never under 60 s
// from the session's start; a long utterance does not eat into it, since the
// mode's time is counted again from the transcript
func TestTurnDeadlineFollowsMode(t *testing.T) {
	for name, tc := range map[string]struct {
		budget    time.Duration
		known     bool
		listen    time.Duration // between the first mic packet and the rest
		want      time.Duration
		fromReply bool // want counts from the transcript, else from the session's start
	}{
		"smart":                     {90 * time.Second, true, 0, 105*time.Second + agent.MaxHold, false},
		"smart after a long listen": {90 * time.Second, true, time.Second, 105*time.Second + agent.MaxHold, true},
		"fast after a long listen":  {20 * time.Second, true, time.Second, 35*time.Second + agent.MaxHold, true},
		"unknown":                   {0, false, 0, 60 * time.Second, false},
	} {
		t.Run(name, func(t *testing.T) {
			left := make(chan time.Duration, 1)
			var asked settings.Values
			h := newHarness(t, func(s *setup) {
				s.mode = func(id string, v settings.Values) (agent.Mode, bool) {
					if id != dev {
						t.Errorf("mode of %q", id)
					}
					asked = v
					return agent.Mode{Name: "m", MaxDuration: tc.budget}, tc.known
				}
				s.agent = func(ctx context.Context, _ agent.Turn, _ chan<- agent.Out) agent.Result {
					dl, ok := ctx.Deadline()
					if !ok {
						t.Error("agent ctx without a deadline")
					}
					left <- time.Until(dl)
					return agent.Result{Silent: true}
				}
			})
			start := time.Now()
			h.start("s1")
			h.mic("s1", 1, 1)
			time.Sleep(tc.listen)
			h.mic("s1", 2, 1)
			h.waitClose("s1")
			got := <-left
			hi, lo := tc.want-tc.listen, tc.want-time.Since(start)-300*time.Millisecond
			if tc.fromReply {
				hi, lo = tc.want, tc.want-300*time.Millisecond
			}
			if got > hi || got < lo {
				t.Fatalf("deadline in %v, want %v..%v", got, lo, hi)
			}
			if asked.LLM != defaultValues.LLM {
				t.Fatalf("budget asked with settings %+v", asked)
			}
		})
	}
}
