package voice

import (
	"bytes"
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
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var fullTurn = []string{"SPEECH_STARTED", "THINKING", "stream_start", "audio×30", "stream_end", "close:"}

func TestFullTurnOrder(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.channels, s.bitrate = 2, 96000 })
	h.start("s1")
	h.mic("s1", 3, 1)
	h.waitClose("s1")
	h.waitTurn("s1")

	// the fake reply is two sentences of 300 ms each: 2 × 15 frames of 20 ms
	wantFrames(t, h.conn.all(), fullTurn...)
	var streamID string
	seq := uint32(0)
	for _, e := range h.conn.all() {
		if s := e.GetStreamStart(); s != nil {
			streamID = s.GetStreamId()
			if s.GetSessionId() != "s1" || s.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY ||
				s.GetCodec() != cadencev2.Codec_CODEC_OPUS || s.GetChannels() != 2 || streamID == "" {
				t.Fatalf("stream start %v", s)
			}
		}
		if a := e.GetAudioDown(); a != nil {
			if a.GetStreamId() != streamID || a.GetSeq() != seq || len(a.GetOpus()) != 1 {
				t.Fatalf("audio %v, want seq %d of %s", a, seq, streamID)
			}
			seq++
		}
		if s := e.GetStreamEnd(); s != nil && s.GetStreamId() != streamID {
			t.Fatalf("stream end %v", s)
		}
		if c := e.GetSessionClose(); c != nil && (c.GetSessionId() != "s1" || c.GetFollowUpAllowed()) {
			t.Fatalf("close %v", c)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	want := audio.EncoderOptions{SampleRate: 24000, Channels: 2, Bitrate: 96000, Complexity: 10, Application: audio.AppAudio}
	if len(h.opts) != 1 || h.opts[0] != want {
		t.Fatalf("encoder options %+v", h.opts)
	}
}

func TestSupersede(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.pauseFirst = true })
	h.start("a")
	h.mic("a", 3, 1)
	waitFor(t, "a's burst", func() bool { return h.audioOf("a") == 10 })
	h.c.mu.Lock()
	a := h.c.sessions[dev]
	h.c.mu.Unlock()

	h.start("b")
	before := len(h.conn.of("a"))
	// a frame a was about to send when b arrived must not go out either
	if a.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: "a"}}}) {
		t.Fatal("superseded session sent a frame")
	}
	h.mic("b", 3, 1)
	h.waitClose("b")
	if line := h.waitTurn("a"); line["code"] != "superseded" {
		t.Fatalf("a's turn %v", line)
	}
	if after := len(h.conn.of("a")); after != before {
		t.Fatalf("a sent %d frames after b started", after-before)
	}
	wantFrames(t, h.conn.of("a"), "SPEECH_STARTED", "THINKING", "stream_start", "audio×10")
	wantFrames(t, h.conn.of("b"), fullTurn...)
	h.waitTurn("b")
	if ended := h.endedSessions(); !slices.Contains(ended, "a") || !slices.Contains(ended, "b") {
		t.Fatalf("sessions ended %q", ended)
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.pauseFirst = true })
	h.start("a")
	h.mic("a", 3, 1)
	waitFor(t, "a's burst", func() bool { return h.audioOf("a") == 10 })

	h.cancel("a")
	before := len(h.conn.all())
	if line := h.waitTurn("a"); line["code"] != "cancelled" {
		t.Fatalf("turn %v", line)
	}
	h.mic("a", 3, 1)
	if after := len(h.conn.all()); after != before {
		t.Fatalf("%d frames after the cancel", after-before)
	}
	wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×10")
	if ended := h.endedSessions(); !slices.Equal(ended, []string{"a"}) {
		t.Fatalf("sessions ended %q", ended)
	}
}

func TestStaleSessionAudioDropped(t *testing.T) {
	rec := &recSTT{finalAfter: 3}
	h := newHarness(t, func(s *setup) { s.stt = rec })
	h.start("a")
	h.mic("a", 1, 0xA)
	waitFor(t, "a's first packet", func() bool { f := rec.fed(); return len(f) == 1 && len(f[0]) == 1 })

	h.start("b")
	h.mic("a", 5, 0xA)
	h.mic("unknown", 5, 0xC)
	h.cancel("a") // names a superseded session, so b goes on
	h.mic("b", 3, 0xB)
	if c := h.waitClose("b"); c.GetCode() != "" {
		t.Fatalf("b closed with %q", c.GetCode())
	}
	fed := rec.fed()
	if len(fed) != 2 || !bytes.Equal(fed[0], []byte{0xA}) || !bytes.Equal(fed[1], []byte{0xB, 0xB, 0xB}) {
		t.Fatalf("stt streams were fed %x", fed)
	}
	wantFrames(t, h.conn.of("b"), fullTurn...)
}

func TestMidStreamFailureEndsStream(t *testing.T) {
	t.Run("reply said", func(t *testing.T) {
		h := newHarness(t, func(s *setup) {
			s.tts = fakeModule(t, "fake-tts", `{"failAfterSpeaks":1}`).(providers.TTS)
		})
		h.start("s1")
		h.mic("s1", 3, 1)
		h.waitClose("s1")
		wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×15", "stream_end", "close:tts_failed")
	})
	// nothing more the agent says could be heard, so its turn ends with the tts
	t.Run("agent still working", func(t *testing.T) {
		stopped := make(chan error, 1)
		h := newHarness(t, func(s *setup) {
			s.tts = fakeModule(t, "fake-tts", `{"failAfterSpeaks":1}`).(providers.TTS)
			s.agent = func(ctx context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
				out <- agent.Out{Speech: "One. Two. "}
				<-ctx.Done()
				stopped <- ctx.Err()
				return agent.Result{Code: agent.CodeCanceled}
			}
		})
		h.start("s1")
		h.mic("s1", 3, 1)
		h.waitClose("s1")
		wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", "stream_start", "audio×15", "stream_end", "close:tts_failed")
		if err := <-stopped; !errors.Is(err, context.Canceled) {
			t.Fatalf("agent ctx %v", err)
		}
	})
}

func TestNotConfigured(t *testing.T) {
	for name, edit := range map[string]func(*settings.Values){
		"no stt":       func(v *settings.Values) { v.STT = "" },
		"no tts":       func(v *settings.Values) { v.TTS = "" },
		"no llm":       func(v *settings.Values) { v.LLM = "" },
		"unknown stt":  func(v *settings.Values) { v.STT = "deleted" },
		"unknown tts":  func(v *settings.Values) { v.TTS = "deleted" },
		"unknown llm":  func(v *settings.Values) { v.LLM = "deleted" },
		"all of them":  func(v *settings.Values) { *v = settings.Values{} },
		"only a voice": func(v *settings.Values) { *v = settings.Values{Voice: "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recSTT{finalAfter: 3}
			h := newHarness(t, func(s *setup) { s.stt = rec; edit(&s.values) })
			h.start("s1")
			h.mic("s1", 3, 1)
			h.waitClose("s1")
			wantFrames(t, h.conn.all(), "close:not_configured")
			if len(rec.fed()) != 0 {
				t.Fatal("stt opened")
			}
		})
	}
}

func TestFailureCodes(t *testing.T) {
	llmErr := errors.New(providers.CodeLLMFailed)
	cases := []struct {
		name string
		edit func(*setup)
		fail func(*cadencev2.Envelope) error
		want []string
	}{
		{"stt start fails", func(s *setup) { s.stt = &recSTT{startErr: errors.New(providers.CodeSTTUnavailable)} },
			nil, []string{"close:stt_unavailable"}},
		{"stt open deadline", func(s *setup) {
			s.stt = &recSTT{block: true}
			s.lim = func(l *limits) { l.sttOpen = 50 * time.Millisecond }
		}, nil, []string{"close:stt_unavailable"}},
		{"stt fails", func(s *setup) { s.stt = &recSTT{failAfter: 2} },
			nil, []string{"SPEECH_STARTED", "close:stt_failed"}},
		{"utterance limit without a partial", func(s *setup) {
			s.stt = &recSTT{}
			s.lim = func(l *limits) { l.utterance = 50 * time.Millisecond }
		}, nil, []string{"SPEECH_STARTED", "close:stt_failed"}},
		{"llm call fails", func(s *setup) { s.llm = &scriptLLM{err: llmErr} },
			nil, []string{"SPEECH_STARTED", "THINKING", "close:llm_failed"}},
		{"llm fails first", func(s *setup) { s.llm = &scriptLLM{chunks: []providers.LLMChunk{{Err: llmErr}}} },
			nil, []string{"SPEECH_STARTED", "THINKING", "close:llm_failed"}},
		{"llm fails after a sentence", func(s *setup) {
			s.llm = &scriptLLM{chunks: []providers.LLMChunk{{Text: "Hello there. "}, {Err: llmErr}}}
		}, nil, []string{"SPEECH_STARTED", "THINKING", "stream_start", "audio×15", "stream_end", "close:llm_failed"}},
		{"turn deadline", func(s *setup) {
			s.agent = func(ctx context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
				out <- agent.Out{Speech: "Hi"}
				<-ctx.Done()
				return agent.Result{Code: agent.CodeTimeout}
			}
			s.lim = func(l *limits) { l.turn = 200 * time.Millisecond }
		}, nil, []string{"SPEECH_STARTED", "THINKING", "close:timeout"}},
		{"tts open fails", func(s *setup) { s.tts = failTTS{} },
			nil, []string{"SPEECH_STARTED", "THINKING", "close:tts_failed"}},
		{"no encoder", func(s *setup) { s.encoderErr = audio.ErrUnavailable },
			nil, []string{"close:tts_failed"}},
		{"device queue full", nil, func() func(*cadencev2.Envelope) error {
			var once sync.Once
			return func(e *cadencev2.Envelope) error {
				var err error
				if e.GetAudioDown() != nil {
					once.Do(func() { err = gateway.ErrBackpressure })
				}
				return err
			}
		}(), []string{"SPEECH_STARTED", "THINKING", "stream_start", "stream_end", "close:timeout"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var edits []func(*setup)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			h := newHarness(t, edits...)
			h.conn.fail = tc.fail
			h.start("s1")
			h.mic("s1", 3, 1)
			h.waitClose("s1")
			h.waitTurn("s1")
			wantFrames(t, h.conn.all(), tc.want...)
		})
	}
}

func TestTurnLogHasNoText(t *testing.T) {
	h := newHarness(t)
	h.start("s1")
	h.mic("s1", 3, 1)
	h.waitClose("s1")
	line := h.waitTurn("s1")
	if line["device_id"] != dev || line["code"] != "ok" {
		t.Fatalf("turn %v", line)
	}
	for _, k := range []string{"endpointMs", "firstAudioMs", "totalMs"} {
		if v, ok := line[k].(float64); !ok || v < 0 {
			t.Fatalf("%s = %v", k, line[k])
		}
	}
	logs := h.logs.String()
	for _, secret := range []string{
		transcript, "test time", "All good", defaultValues.STT, defaultValues.TTS, defaultValues.LLM,
		defaultValues.Voice, defaultValues.Model,
	} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log carries %q", secret)
		}
	}
}

func TestDisconnectStopsSession(t *testing.T) {
	t.Run("link closed", func(t *testing.T) {
		h := newHarness(t, func(s *setup) { s.pauseFirst = true })
		h.start("a")
		h.mic("a", 3, 1)
		waitFor(t, "a's burst", func() bool { return h.audioOf("a") == 10 })
		h.c.Disconnected(dev)
		before := len(h.conn.all())
		if line := h.waitTurn("a"); line["code"] != "disconnected" {
			t.Fatalf("turn %v", line)
		}
		h.mic("a", 3, 1)
		if after := len(h.conn.all()); after != before {
			t.Fatalf("%d frames after the disconnect", after-before)
		}
		if ended := h.endedSessions(); !slices.Equal(ended, []string{"a"}) {
			t.Fatalf("sessions ended %q", ended)
		}
	})
	t.Run("link replaced", func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		h.ctx = ctx
		h.start("a")
		h.mic("a", 1, 1)
		waitFor(t, "speech started", func() bool { return len(h.conn.all()) == 1 })
		cancel()
		if line := h.waitTurn("a"); line["code"] != "disconnected" {
			t.Fatalf("turn %v", line)
		}
		wantFrames(t, h.conn.all(), "SPEECH_STARTED")
	})
}

func TestOutputFromState(t *testing.T) {
	for raw, want := range map[string][2]int{
		`{"level":30,"channels":2,"maxBitrate":96000}`: {2, 96000},
		`{"level":30,"channels":1,"maxBitrate":32000}`: {1, 32000},
		`{"level":30}`:                     {1, 64000},
		`{"channels":3,"maxBitrate":100}`:  {1, 64000},
		`{"channels":"2","maxBitrate":-1}`: {1, 64000},
		`not json`:                         {1, 64000},
		``:                                 {1, 64000},
	} {
		out := OutputFrom(func(_ context.Context, id, capability string) (json.RawMessage, bool) {
			if id != dev || capability != "audio.output" {
				t.Fatalf("state of %s %s", id, capability)
			}
			return json.RawMessage(raw), raw != ""
		})
		ch, br, reported := out(context.Background(), dev)
		if ch != want[0] || br != want[1] || reported != (raw != "") {
			t.Fatalf("%s: %d %d %v, want %v", raw, ch, br, reported, want)
		}
	}
}

// an empty flush may go unanswered, so a turn with nothing spoken closes tts
// instead: its real code goes out at once, not the turn deadline's timeout
func TestNothingSpokenEndsPromptly(t *testing.T) {
	for name, tc := range map[string]struct {
		llm  *scriptLLM
		want string
	}{
		"llm fails first": {&scriptLLM{chunks: []providers.LLMChunk{{Err: errors.New(providers.CodeLLMFailed)}}}, "close:llm_failed"},
		"empty reply":     {&scriptLLM{chunks: []providers.LLMChunk{{Text: " … "}}}, "close:"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recTTS{}
			h := newHarness(t, func(s *setup) {
				s.llm, s.tts = tc.llm, rec
				s.lim = func(l *limits) { l.turn = time.Second }
			})
			h.start("s1")
			h.mic("s1", 3, 1)
			h.waitClose("s1")
			wantFrames(t, h.conn.all(), "SPEECH_STARTED", "THINKING", tc.want)
			if said := rec.said(); len(said) != 0 {
				t.Fatalf("spoke %q", said)
			}
		})
	}
}

// aura joins what it is sent until a flush, so sentences need the space the chunker trimmed
func TestSentencesSpokenWithSpaces(t *testing.T) {
	rec := &recTTS{}
	h := newHarness(t, func(s *setup) {
		s.tts = rec
		s.llm = &scriptLLM{chunks: []providers.LLMChunk{{Text: "Hello there. How"}, {Text: " are you? Fine"}}}
	})
	h.start("s1")
	h.mic("s1", 3, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	if said, want := rec.said(), []string{"Hello there.", " How are you?", " Fine"}; strings.Join(said, "|") != strings.Join(want, "|") {
		t.Fatalf("spoke %q, want %q", said, want)
	}
}

// a panic ends its session, not the server; its value never reaches a log
func TestPanicClosesSessionInternal(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*setup)
		fail func(*cadencev2.Envelope) error
		// the helper it panicked on, if not the session's own goroutine
		part string
		want []string
	}{
		// tts opens on its own goroutine, beside the agent
		"provider": {func(s *setup) { s.tts = panicTTS{} }, nil, "tts_open",
			[]string{"SPEECH_STARTED", "THINKING", "close:internal"}},
		"stream open": {nil, func() func(*cadencev2.Envelope) error {
			n := 0
			return func(e *cadencev2.Envelope) error {
				if e.GetAudioDown() != nil {
					if n++; n == 2 {
						panic("secret-xyz")
					}
				}
				return nil
			}
		}(), "", []string{"SPEECH_STARTED", "THINKING", "stream_start", "audio×1", "stream_end", "close:internal"}},
	} {
		t.Run(name, func(t *testing.T) {
			var edits []func(*setup)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			h := newHarness(t, edits...)
			h.conn.fail = tc.fail
			h.start("s1")
			h.mic("s1", 3, 1)
			h.waitClose("s1")
			if line := h.waitTurn("s1"); line["code"] != "internal" {
				t.Fatalf("turn %v", line)
			}
			wantFrames(t, h.conn.all(), tc.want...)
			logs := h.logs.String()
			line := `"msg":"voice_panic","device_id":"` + dev + `","session_id":"s1"}`
			if tc.part != "" {
				line = `"msg":"voice_panic","device_id":"` + dev + `","session_id":"s1","part":"` + tc.part + `"}`
			}
			if !strings.Contains(logs, line) || strings.Contains(logs, "secret-xyz") {
				t.Fatalf("log: %s", logs)
			}
		})
	}
}

type panicOnceSTT struct {
	inner providers.STT
	n     atomic.Int32
}

func (p *panicOnceSTT) Start(ctx context.Context, o providers.STTOptions) (providers.STTStream, error) {
	if p.n.Add(1) == 1 {
		panic("secret-xyz")
	}
	return p.inner.Start(ctx, o)
}

type panicOnceTTS struct {
	inner providers.TTS
	n     atomic.Int32
}

func (p *panicOnceTTS) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	s, err := p.inner.Open(ctx, o)
	return &panicOnceStream{TTSStream: s, n: &p.n}, err
}

type panicOnceStream struct {
	providers.TTSStream
	n *atomic.Int32
}

func (s *panicOnceStream) Speak(text string) error {
	if s.n.Add(1) == 1 {
		panic("secret-xyz")
	}
	return s.TTSStream.Speak(text)
}

type panicOnceEncoder struct {
	fakeEncoder
	n *atomic.Int32
}

func (e panicOnceEncoder) Encode(pcm []int16) ([]byte, error) {
	if e.n.Add(1) == 2 {
		panic("secret-xyz")
	}
	return e.fakeEncoder.Encode(pcm)
}

// a panic in a helper goroutine ends its turn as internal, closes an open
// stream, and leaves the coordinator serving the next session
func TestHelperPanicEndsTurn(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*setup)
		part string
		want []string
	}{
		"stt start": {func(s *setup) { s.stt = &panicOnceSTT{inner: s.stt} }, "stt_start",
			[]string{"close:internal"}},
		"agent": {func(s *setup) {
			var n atomic.Int32
			fine := says("It is test time. ", "All good.")
			s.agent = func(ctx context.Context, t agent.Turn, out chan<- agent.Out) agent.Result {
				if n.Add(1) == 1 {
					panic("secret-xyz")
				}
				return fine(ctx, t, out)
			}
		}, "agent", []string{"SPEECH_STARTED", "THINKING", "close:internal"}},
		"speak": {func(s *setup) { s.tts = &panicOnceTTS{inner: s.tts} }, "speak",
			[]string{"SPEECH_STARTED", "THINKING", "close:internal"}},
		"encode with stream open": {func(s *setup) {
			var n atomic.Int32
			s.encoder = func(samples int) audio.Encoder { return panicOnceEncoder{fakeEncoder{samples}, &n} }
		}, "encode", []string{"SPEECH_STARTED", "THINKING", "stream_start", "audio×1", "stream_end", "close:internal"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tc.edit)
			h.start("s1")
			h.mic("s1", 3, 1)
			if c := h.waitClose("s1"); c.GetCode() != "internal" {
				t.Fatalf("closed with %q", c.GetCode())
			}
			if line := h.waitTurn("s1"); line["code"] != "internal" {
				t.Fatalf("turn %v", line)
			}
			wantFrames(t, h.conn.of("s1"), tc.want...)
			logs := h.logs.String()
			if !strings.Contains(logs, `"msg":"voice_panic","device_id":"`+dev+`","session_id":"s1","part":"`+tc.part+`"}`) ||
				strings.Contains(logs, "secret-xyz") {
				t.Fatalf("log: %s", logs)
			}
			h.start("s2")
			h.mic("s2", 3, 1)
			if c := h.waitClose("s2"); c.GetCode() != "" {
				t.Fatalf("next session closed with %q", c.GetCode())
			}
		})
	}
}

func TestDeviceCannotStartAnEventSession(t *testing.T) {
	h := newHarness(t)
	h.start("keep")
	for _, tr := range []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_EVENT, cadencev2.Trigger_TRIGGER_UNSPECIFIED, cadencev2.Trigger(99)} {
		h.startAs("bad", tr)
	}
	c := h.waitClose("bad")
	if c.GetCode() != "invalid_trigger" || c.GetFollowUpAllowed() {
		t.Fatalf("close %v", c)
	}
	h.c.mu.Lock()
	s := h.c.sessions[dev]
	h.c.mu.Unlock()
	if s == nil || s.id != "keep" {
		t.Fatal("a refused start superseded the running session")
	}
}

// the task reporter holds its work while a session runs and hears its end
// with the trigger that began it and whether a user turn ran in it, once it
// no longer counts as active
func TestSessionActiveUntilItsEndIsHeard(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.pauseFirst = true })
	if h.c.SessionActive(dev) {
		t.Fatal("active before any session")
	}
	h.startAs("a", cadencev2.Trigger_TRIGGER_WAKE)
	if !h.c.SessionActive(dev) || h.c.SessionActive("other") {
		t.Fatal("session not active, or active elsewhere")
	}
	h.mic("a", 3, 1)
	waitFor(t, "a's burst", func() bool { return h.audioOf("a") == 10 })
	// a superseded session's end is heard while the newer one still counts
	h.startAs("b", cadencev2.Trigger_TRIGGER_FOLLOW_UP)
	h.waitTurn("a")
	if !h.c.SessionActive(dev) {
		t.Fatal("the newer session does not count")
	}
	h.cancel("b")
	h.waitTurn("b")
	if h.c.SessionActive(dev) {
		t.Fatal("active after the last session ended")
	}
	triggers, _ := h.ends()
	if !slices.Equal(triggers, []cadencev2.Trigger{cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_FOLLOW_UP}) {
		t.Fatalf("ends heard %v", triggers)
	}
	// a's words reached the agent; b ended before anyone spoke
	if heard := h.heard(); !slices.Equal(heard, []bool{true, false}) {
		t.Fatalf("heard %v", heard)
	}
}
