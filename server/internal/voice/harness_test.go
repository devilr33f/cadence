package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/fake"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const dev = "11111111-2222-3333-4444-555555555555"

// distinctive, so the log test can look for them
var defaultValues = settings.Values{
	STT: "stt-instance-x", TTS: "tts-instance-x", Voice: "voice-name-x",
	LLM: "llm-instance-x", Model: "model-name-x", Language: "en",
}

const transcript = "please tell me the time"

type fakeConn struct {
	mu   sync.Mutex
	sent []*cadencev2.Envelope
	fail func(*cadencev2.Envelope) error
}

func (f *fakeConn) DeviceID() string { return dev }

func (f *fakeConn) Send(env *cadencev2.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		if err := f.fail(env); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, env)
	return nil
}

func (f *fakeConn) all() []*cadencev2.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*cadencev2.Envelope(nil), f.sent...)
}

// of returns the frames of one session; stream frames are matched through their StreamStart.
func (f *fakeConn) of(sessionID string) []*cadencev2.Envelope {
	streams := map[string]bool{}
	var out []*cadencev2.Envelope
	for _, e := range f.all() {
		switch b := e.GetBody().(type) {
		case *cadencev2.Envelope_SessionEvent:
			if b.SessionEvent.GetSessionId() == sessionID {
				out = append(out, e)
			}
		case *cadencev2.Envelope_SessionClose:
			if b.SessionClose.GetSessionId() == sessionID {
				out = append(out, e)
			}
		case *cadencev2.Envelope_StreamStart:
			if b.StreamStart.GetSessionId() == sessionID {
				streams[b.StreamStart.GetStreamId()] = true
				out = append(out, e)
			}
		case *cadencev2.Envelope_AudioDown:
			if streams[b.AudioDown.GetStreamId()] {
				out = append(out, e)
			}
		case *cadencev2.Envelope_StreamEnd:
			if streams[b.StreamEnd.GetStreamId()] {
				out = append(out, e)
			}
		}
	}
	return out
}

// describe names each frame and folds runs of audio into one entry.
func describe(frames []*cadencev2.Envelope) []string {
	var out []string
	audioRun := 0
	flush := func() {
		if audioRun > 0 {
			out = append(out, fmt.Sprintf("audio×%d", audioRun))
			audioRun = 0
		}
	}
	for _, e := range frames {
		if e.GetAudioDown() != nil {
			audioRun++
			continue
		}
		flush()
		switch b := e.GetBody().(type) {
		case *cadencev2.Envelope_SessionEvent:
			out = append(out, strings.TrimPrefix(b.SessionEvent.GetKind().String(), "SESSION_EVENT_KIND_"))
		case *cadencev2.Envelope_StreamStart:
			out = append(out, "stream_start")
		case *cadencev2.Envelope_StreamEnd:
			out = append(out, "stream_end")
		case *cadencev2.Envelope_SessionClose:
			out = append(out, "close:"+b.SessionClose.GetCode())
		default:
			out = append(out, fmt.Sprintf("%T", b))
		}
	}
	flush()
	return out
}

type fakeProviders struct {
	stt providers.STT
	llm providers.LLM
	tts providers.TTS
}

func (p *fakeProviders) STT(_ context.Context, name string) (providers.STT, error) {
	if name != defaultValues.STT {
		return nil, providers.ErrNotFound
	}
	return p.stt, nil
}

func (p *fakeProviders) LLM(_ context.Context, name string) (providers.LLM, error) {
	if name != defaultValues.LLM {
		return nil, providers.ErrNotFound
	}
	return p.llm, nil
}

func (p *fakeProviders) TTS(_ context.Context, name string) (providers.TTS, error) {
	if name != defaultValues.TTS {
		return nil, providers.ErrNotFound
	}
	return p.tts, nil
}

func fakeModule(t *testing.T, id, cfg string) any {
	t.Helper()
	for _, m := range fake.Modules() {
		if m.ID == id {
			v, err := m.New(json.RawMessage(cfg), nil)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no module %s", id)
	return nil
}

type fakeEncoder struct{ samples int }

func (e fakeEncoder) FrameSamples() int { return e.samples }

func (e fakeEncoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != e.samples {
		return nil, audio.ErrFrameSize
	}
	return []byte{1}, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type setup struct {
	values            settings.Values
	stt               providers.STT
	llm               providers.LLM
	tts               providers.TTS
	channels, bitrate int
	encoderErr        error
	encoder           func(samples int) audio.Encoder
	lim               func(*limits)
	// the first reply's ticker never ticks, so it stalls after its burst
	pauseFirst bool
	// ticks, when set, is every reply's ticker
	ticks chan time.Time
	// agent answers instead of the real runner over llm
	agent agentFunc
	// tools are the real runner's
	tools []agent.Tool
	// mode is the device's current mode; unset, the real runner's, else unknown
	mode func(deviceID string, v settings.Values) (agent.Mode, bool)
	// noOutput: the device never reported audio.output
	noOutput bool
	// playing says whether the device's media plays; pause answers its pause command
	playing func() bool
	pause   func(ctx context.Context) error
	// command answers a capability command a direct command sends; nil refuses all
	command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// homeControl answers a direct home command; nil never settles one
	homeControl func(ctx context.Context, call HomeCall) (string, bool)
	// storeCommand records a stored direct command turn
	storeCommand func(ctx context.Context, deviceID, turnID, text, reply, trigger string) error
	// transcript hears each hypothesis the session hands the screens
	transcript func(deviceID, sessionID, text string, final bool)
	// reset records a deterministic conversation reset
	reset func(ctx context.Context, deviceID, sessionID, text, reply string) error
	// recordFailure captures the voice turn's stored outcome
	recordFailure func(ctx context.Context, deviceID, turnID string, meta conversation.TurnMeta, failure conversation.TurnFailure) error
}

type harness struct {
	t    *testing.T
	c    *Coordinator
	conn *fakeConn
	ctx  context.Context
	logs *syncBuffer
	// what the turns read of the settings and the modes
	settingsReads atomic.Int32
	modes         *harnessModes

	mu    sync.Mutex
	opts  []audio.EncoderOptions
	ended []string
	// what the end hooks heard: each ended session's trigger and whether a
	// user turn ran in it, and how many announcements ended
	endedTriggers []cadencev2.Trigger
	endedHeard    []bool
	announceEnds  int
	// how many media pauses the sessions sent
	pauses int
	// what the direct commands sent and were told
	commands []commandCall
	homes    []HomeCall
	stored   []storedTurn
}

// commandCall is one capability command a direct command sent.
type commandCall struct {
	capability, name string
	args             json.RawMessage
}

type storedTurn struct {
	deviceID, turnID, text, reply, trigger string
}

func newHarness(t *testing.T, edit ...func(*setup)) *harness {
	t.Helper()
	s := setup{
		values:   defaultValues,
		stt:      fakeModule(t, "fake-stt", `{"finalAfterPackets":3,"text":"`+transcript+`"}`).(providers.STT),
		llm:      fakeModule(t, "fake-llm", `{}`).(providers.LLM),
		tts:      fakeModule(t, "fake-tts", `{}`).(providers.TTS),
		channels: 1, bitrate: 64000,
	}
	for _, e := range edit {
		e(&s)
	}
	h := &harness{t: t, conn: &fakeConn{}, ctx: context.Background(), logs: &syncBuffer{}, modes: &harnessModes{}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	values := func(_ context.Context, id string) (settings.Values, error) {
		if id != dev {
			t.Errorf("settings for %q", id)
		}
		h.settingsReads.Add(1)
		return s.values, nil
	}
	provs := &fakeProviders{stt: s.stt, llm: s.llm, tts: s.tts}
	var runner interface {
		Run(ctx context.Context, t agent.Turn, out chan<- agent.Out) agent.Result
	} = s.agent
	mode := func(context.Context, string, settings.Values) (agent.Mode, bool) { return agent.Mode{}, false }
	if s.agent == nil {
		r := agent.NewRunner(agent.RunnerDeps{
			Providers: provs, Settings: values, Modes: h.modes, ModeState: agent.NewModeState(),
			Tools: agent.NewRegistry(s.tools...), Conversations: nopConversations{}, Now: time.Now, Log: log,
		})
		runner, mode = r, r.PeekMode
	}
	if s.mode != nil {
		mode = func(_ context.Context, id string, v settings.Values) (agent.Mode, bool) { return s.mode(id, v) }
	}
	h.c = New(Deps{
		Settings:      values,
		Providers:     provs,
		Agent:         runner,
		RecordFailure: s.recordFailure,
		Mode:          mode,
		EndSession: func(id, sessionID string) {
			if id != dev {
				t.Errorf("session end for %q", id)
			}
			h.mu.Lock()
			h.ended = append(h.ended, sessionID)
			h.mu.Unlock()
		},
		SessionEnded: func(id string, trigger cadencev2.Trigger, heard bool) {
			if id != dev {
				t.Errorf("session ended for %q", id)
			}
			h.mu.Lock()
			h.endedTriggers = append(h.endedTriggers, trigger)
			h.endedHeard = append(h.endedHeard, heard)
			h.mu.Unlock()
		},
		AnnounceEnded: func(id string) {
			if id != dev {
				t.Errorf("announcement ended for %q", id)
			}
			h.mu.Lock()
			h.announceEnds++
			h.mu.Unlock()
		},
		NewEncoder: func(o audio.EncoderOptions) (audio.Encoder, error) {
			h.mu.Lock()
			h.opts = append(h.opts, o)
			h.mu.Unlock()
			if s.encoderErr != nil {
				return nil, s.encoderErr
			}
			if s.encoder != nil {
				return s.encoder(o.SampleRate / 50 * o.Channels), nil
			}
			return fakeEncoder{samples: o.SampleRate / 50 * o.Channels}, nil
		},
		Output: func(context.Context, string) (int, int, bool) { return s.channels, s.bitrate, !s.noOutput },
		MediaPlaying: func(id string) bool {
			if id != dev {
				t.Errorf("media of %q", id)
			}
			return s.playing != nil && s.playing()
		},
		PauseMedia: func(ctx context.Context, id string) error {
			if id != dev {
				t.Errorf("pause of %q", id)
			}
			h.mu.Lock()
			h.pauses++
			h.mu.Unlock()
			if s.pause == nil {
				return nil
			}
			return s.pause(ctx)
		},
		Transcript:        s.transcript,
		ResetConversation: s.reset,
		Command: func(ctx context.Context, id, capability, name string, args []byte) (json.RawMessage, error) {
			if id != dev {
				t.Errorf("command of %q", id)
			}
			h.mu.Lock()
			h.commands = append(h.commands, commandCall{capability: capability, name: name, args: append(json.RawMessage(nil), args...)})
			h.mu.Unlock()
			if s.command == nil {
				return nil, errors.New("no command")
			}
			return s.command(ctx, id, capability, name, args)
		},
		HomeControl: func(ctx context.Context, call HomeCall) (string, bool) {
			h.mu.Lock()
			h.homes = append(h.homes, call)
			h.mu.Unlock()
			if s.homeControl == nil {
				return "", false
			}
			return s.homeControl(ctx, call)
		},
		StoreCommand: func(ctx context.Context, id, turnID, text, reply, trigger string) error {
			h.mu.Lock()
			h.stored = append(h.stored, storedTurn{deviceID: id, turnID: turnID, text: text, reply: reply, trigger: trigger})
			h.mu.Unlock()
			if s.storeCommand != nil {
				return s.storeCommand(ctx, id, turnID, text, reply, trigger)
			}
			return nil
		},
		Now: time.Now,
		Log: log,
	})
	if s.lim != nil {
		s.lim(&h.c.lim)
	}
	var calls atomic.Int32
	h.c.tick = func() (<-chan time.Time, func()) {
		if s.ticks != nil {
			return s.ticks, func() {}
		}
		if s.pauseFirst && calls.Add(1) == 1 {
			return make(chan time.Time), func() {}
		}
		tk := time.NewTicker(time.Millisecond)
		return tk.C, tk.Stop
	}
	return h
}

func (h *harness) handle(env *cadencev2.Envelope) {
	h.t.Helper()
	if !h.c.Handle(h.ctx, h.conn, env) {
		h.t.Fatalf("not handled: %T", env.GetBody())
	}
}

func (h *harness) start(id string) { h.startAs(id, cadencev2.Trigger_TRIGGER_BUTTON) }

func (h *harness) startAs(id string, trigger cadencev2.Trigger) {
	h.handle(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: id, Trigger: trigger,
	}}})
}

// endedSessions are the sessions the coordinator told the mode state about, in order.
func (h *harness) endedSessions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ended...)
}

// ends are the triggers of the sessions the end hook heard of, and the count of ended announcements.
func (h *harness) ends() ([]cadencev2.Trigger, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]cadencev2.Trigger(nil), h.endedTriggers...), h.announceEnds
}

// sentCommands are the capability commands the direct commands sent.
func (h *harness) sentCommands() []commandCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]commandCall(nil), h.commands...)
}

// homeCalls are the direct home commands, and storedTurns what was stored.
func (h *harness) homeCalls() []HomeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HomeCall(nil), h.homes...)
}

func (h *harness) storedTurns() []storedTurn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]storedTurn(nil), h.stored...)
}

// heard says, for each ended session, whether a user turn ran in it.
func (h *harness) heard() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.endedHeard...)
}

func (h *harness) mic(id string, n int, b byte) {
	for i := range n {
		h.handle(&cadencev2.Envelope{Body: &cadencev2.Envelope_MicAudio{MicAudio: &cadencev2.MicAudio{
			SessionId: id, Seq: uint32(i), Opus: []byte{b},
		}}})
	}
}

func (h *harness) cancel(id string) {
	h.handle(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionCancel{SessionCancel: &cadencev2.SessionCancel{
		SessionId: id, Reason: "no_speech",
	}}})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) waitClose(id string) *cadencev2.SessionClose {
	h.t.Helper()
	var cl *cadencev2.SessionClose
	waitFor(h.t, "session close of "+id, func() bool {
		for _, e := range h.conn.of(id) {
			if c := e.GetSessionClose(); c != nil {
				cl = c
				return true
			}
		}
		return false
	})
	return cl
}

func (h *harness) audioOf(id string) int {
	n := 0
	for _, e := range h.conn.of(id) {
		if e.GetAudioDown() != nil {
			n++
		}
	}
	return n
}

func (h *harness) turnLine(id string) map[string]any {
	sc := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == "turn" && m["session_id"] == id {
			return m
		}
	}
	return nil
}

// waitTurn waits for the session's goroutine to log its turn, the last thing it does.
func (h *harness) waitTurn(id string) map[string]any {
	h.t.Helper()
	var line map[string]any
	waitFor(h.t, "turn line of "+id, func() bool { line = h.turnLine(id); return line != nil })
	return line
}

func wantFrames(t *testing.T, got []*cadencev2.Envelope, want ...string) {
	t.Helper()
	if d := describe(got); strings.Join(d, " ") != strings.Join(want, " ") {
		t.Fatalf("frames %q, want %q", d, want)
	}
}

// recSTT records what each of its streams was fed.
type recSTT struct {
	finalAfter, failAfter int
	startErr              error
	block                 bool

	mu      sync.Mutex
	streams []*recStream
}

func (r *recSTT) Start(ctx context.Context, _ providers.STTOptions) (providers.STTStream, error) {
	if r.block {
		<-ctx.Done()
		return nil, errors.New(providers.CodeSTTUnavailable)
	}
	if r.startErr != nil {
		return nil, r.startErr
	}
	s := &recStream{r: r, events: make(chan providers.STTEvent, 4)}
	r.mu.Lock()
	r.streams = append(r.streams, s)
	r.mu.Unlock()
	return s, nil
}

func (r *recSTT) fed() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.streams))
	for i, s := range r.streams {
		out[i] = append([]byte(nil), s.packets...)
	}
	return out
}

type recStream struct {
	r       *recSTT
	events  chan providers.STTEvent
	packets []byte
	closed  bool
}

func (s *recStream) Write(p []byte) error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.closed {
		return errors.New("closed")
	}
	s.packets = append(s.packets, p...)
	n := len(s.packets)
	if n == 1 {
		s.events <- providers.STTEvent{Kind: providers.STTSpeechStarted}
	}
	if n == s.r.finalAfter {
		s.events <- providers.STTEvent{Kind: providers.STTFinal, Text: transcript}
	}
	if n == s.r.failAfter {
		s.events <- providers.STTEvent{Kind: providers.STTFailed, Code: providers.CodeSTTFailed}
	}
	return nil
}

func (s *recStream) Events() <-chan providers.STTEvent { return s.events }

func (s *recStream) Close() error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

type scriptLLM struct {
	err    error
	chunks []providers.LLMChunk
}

func (l *scriptLLM) Stream(context.Context, providers.LLMRequest) (<-chan providers.LLMChunk, error) {
	if l.err != nil {
		return nil, l.err
	}
	out := make(chan providers.LLMChunk, len(l.chunks))
	for _, c := range l.chunks {
		out <- c
	}
	close(out)
	return out, nil
}

type failTTS struct{}

func (failTTS) Open(context.Context, providers.TTSOptions) (providers.TTSStream, error) {
	return nil, errors.New(providers.CodeTTSFailed)
}

// recTTS records what it is told to speak. Like aura, it may never answer a
// flush with nothing spoken: its audio then ends only on Close.
type recTTS struct {
	mu     sync.Mutex
	spoken []string
}

func (r *recTTS) Open(context.Context, providers.TTSOptions) (providers.TTSStream, error) {
	return &recTTSStream{r: r, out: make(chan []int16)}, nil
}

func (r *recTTS) said() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.spoken...)
}

type recTTSStream struct {
	r    *recTTS
	out  chan []int16
	once sync.Once
	mu   sync.Mutex
	n    int
}

func (s *recTTSStream) Speak(text string) error {
	s.r.mu.Lock()
	s.r.spoken = append(s.r.spoken, text)
	s.r.mu.Unlock()
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return nil
}

func (s *recTTSStream) Flush() error {
	s.mu.Lock()
	n := s.n
	s.mu.Unlock()
	if n > 0 {
		s.end()
	}
	return nil
}

func (s *recTTSStream) end()                  { s.once.Do(func() { close(s.out) }) }
func (s *recTTSStream) Audio() <-chan []int16 { return s.out }
func (s *recTTSStream) Err() error            { return nil }
func (s *recTTSStream) Close() error          { s.end(); return nil }

type panicTTS struct{}

func (panicTTS) Open(context.Context, providers.TTSOptions) (providers.TTSStream, error) {
	panic("secret-xyz")
}

// agentFunc stands in for the runner.
type agentFunc func(ctx context.Context, t agent.Turn, out chan<- agent.Out) agent.Result

func (f agentFunc) Run(ctx context.Context, t agent.Turn, out chan<- agent.Out) agent.Result {
	return f(ctx, t, out)
}

// says is an agent that speaks each fragment in turn and ends ok.
func says(fragments ...string) agentFunc {
	return func(ctx context.Context, _ agent.Turn, out chan<- agent.Out) agent.Result {
		for _, f := range fragments {
			select {
			case out <- agent.Out{Speech: f}:
			case <-ctx.Done():
				return agent.Result{Code: agent.CodeCanceled}
			}
		}
		return agent.Result{}
	}
}

// harnessModes has the one mode a device without mode settings runs.
type harnessModes struct{ gets atomic.Int32 }

func (m *harnessModes) Get(_ context.Context, name string) (agent.Mode, error) {
	m.gets.Add(1)
	if name != "fast" {
		return agent.Mode{}, agent.ErrModeNotFound
	}
	return agent.Mode{Name: "fast", Reasoning: "none", MaxSteps: 8, MaxDuration: 20 * time.Second, Interim: agent.InterimDrop}, nil
}

type nopConversations struct{}

func (nopConversations) History(context.Context, string, int64, int) ([]providers.Message, error) {
	return nil, nil
}

func (nopConversations) Append(context.Context, string, string, conversation.TurnMeta, []providers.Message) error {
	return nil
}

// spyTTS records what inner is told to speak and how many streams it opened.
type spyTTS struct {
	inner providers.TTS

	mu     sync.Mutex
	spoken []string
	opened int
}

func (s *spyTTS) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	st, err := s.inner.Open(ctx, o)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.opened++
	s.mu.Unlock()
	return &spyStream{TTSStream: st, s: s}, nil
}

func (s *spyTTS) said() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.spoken...)
}

func (s *spyTTS) streams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

type spyStream struct {
	providers.TTSStream
	s *spyTTS
}

func (st *spyStream) Speak(text string) error {
	st.s.mu.Lock()
	st.s.spoken = append(st.s.spoken, text)
	st.s.mu.Unlock()
	return st.TTSStream.Speak(text)
}
