package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	announcePath = "/api/v1/devices/" + dev + "/announce"
	// distinctive, so the log test can look for it
	announceText = "announce-text-qx7"
)

type fakeDevices struct{}

func (fakeDevices) Canonical(_ context.Context, id string) (string, error) {
	if strings.EqualFold(id, dev) {
		return dev, nil
	}
	return "", devices.ErrNotFound
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

type announceRig struct {
	*harness
	app    *fiber.App
	unlink context.CancelFunc // ends the link, as a close or a replacement does
}

// newAnnounceRig links the harness's device and serves the announce route.
func newAnnounceRig(t *testing.T, edit ...func(*setup)) *announceRig {
	t.Helper()
	r := unlinkedRig(t, edit...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.c.Linked(ctx, r.conn)
	r.unlink = cancel
	return r
}

func unlinkedRig(t *testing.T, edit ...func(*setup)) *announceRig {
	t.Helper()
	h := newHarness(t, edit...)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { RegisterAdmin(a, h.c, fakeDevices{}) }},
	})
	return &announceRig{harness: h, app: app, unlink: func() {}}
}

func (r *announceRig) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer x")
	resp, err := r.app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Error(err)
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// announce posts text and returns the stream id of the 202.
func (r *announceRig) announce(t *testing.T, body string) string {
	t.Helper()
	status, resp := r.post(t, announcePath, body)
	if status != fiber.StatusAccepted {
		t.Fatalf("%d %s", status, resp)
	}
	var out struct {
		StreamID string `json:"streamId"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || out.StreamID == "" {
		t.Fatalf("body %s", resp)
	}
	return out.StreamID
}

// stream returns the frames of one stream, in order.
func (r *announceRig) stream(id string) []*cadencev2.Envelope {
	var out []*cadencev2.Envelope
	for _, e := range r.conn.all() {
		if streamOf(e) == id {
			out = append(out, e)
		}
	}
	return out
}

func streamOf(e *cadencev2.Envelope) string {
	switch b := e.GetBody().(type) {
	case *cadencev2.Envelope_StreamStart:
		return b.StreamStart.GetStreamId()
	case *cadencev2.Envelope_AudioDown:
		return b.AudioDown.GetStreamId()
	case *cadencev2.Envelope_StreamEnd:
		return b.StreamEnd.GetStreamId()
	}
	return ""
}

func (r *announceRig) audioIn(id string) int {
	n := 0
	for _, e := range r.stream(id) {
		if e.GetAudioDown() != nil {
			n++
		}
	}
	return n
}

func (r *announceRig) waitEnd(t *testing.T, id string) {
	t.Helper()
	waitFor(t, "stream end of "+id, func() bool {
		for _, e := range r.stream(id) {
			if e.GetStreamEnd() != nil {
				return true
			}
		}
		return false
	})
}

// announceLines are the announce log lines, in order.
func (r *announceRig) announceLines() []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(r.logs.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == "announce" {
			out = append(out, m)
		}
	}
	return out
}

// waitLine waits for the announce line of a stream, "" for a refusal.
func (r *announceRig) waitLine(t *testing.T, streamID string) map[string]any {
	t.Helper()
	var line map[string]any
	waitFor(t, "announce line", func() bool {
		for _, m := range r.announceLines() {
			if id, _ := m["stream_id"].(string); id == streamID {
				line = m
				return true
			}
		}
		return false
	})
	return line
}

func noStreams(t *testing.T, r *announceRig) {
	t.Helper()
	for _, e := range r.conn.all() {
		if streamOf(e) != "" {
			t.Fatalf("stream frame sent: %v", e)
		}
	}
}

// watchTTS records the options each stream opened with, what it was told and its closes.
type watchTTS struct {
	inner providers.TTS

	mu     sync.Mutex
	opts   []providers.TTSOptions
	spoken []string
	closed int
}

func (w *watchTTS) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	st, err := w.inner.Open(ctx, o)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.opts = append(w.opts, o)
	w.mu.Unlock()
	return &watchStream{TTSStream: st, w: w}, nil
}

func (w *watchTTS) seen() ([]providers.TTSOptions, []string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]providers.TTSOptions(nil), w.opts...), append([]string(nil), w.spoken...), w.closed
}

type watchStream struct {
	providers.TTSStream
	w *watchTTS
}

func (s *watchStream) Speak(text string) error {
	s.w.mu.Lock()
	s.w.spoken = append(s.w.spoken, text)
	s.w.mu.Unlock()
	return s.TTSStream.Speak(text)
}

func (s *watchStream) Close() error {
	s.w.mu.Lock()
	s.w.closed++
	s.w.mu.Unlock()
	return s.TTSStream.Close()
}

// scriptTTS plays pcm once flushed, then ends with err, or with nothing until
// Close when it hangs.
type scriptTTS struct {
	pcm    [][]int16
	err    error
	hang   bool
	closed atomic.Int32
}

func (s *scriptTTS) Open(context.Context, providers.TTSOptions) (providers.TTSStream, error) {
	return &scriptStream{t: s, out: make(chan []int16), done: make(chan struct{})}, nil
}

type scriptStream struct {
	t                    *scriptTTS
	out                  chan []int16
	done                 chan struct{}
	flushOnce, closeOnce sync.Once
	err                  error
}

func (s *scriptStream) Speak(string) error { return nil }

func (s *scriptStream) Flush() error {
	s.flushOnce.Do(func() { go s.pump() })
	return nil
}

func (s *scriptStream) pump() {
	defer close(s.out)
	for _, p := range s.t.pcm {
		select {
		case s.out <- p:
		case <-s.done:
			return
		}
	}
	if s.t.hang {
		<-s.done
		return
	}
	s.err = s.t.err
}

func (s *scriptStream) Audio() <-chan []int16 { return s.out }
func (s *scriptStream) Err() error            { return s.err }

func (s *scriptStream) Close() error {
	s.closeOnce.Do(func() { s.t.closed.Add(1); close(s.done) })
	return nil
}

// firstBlocksTTS holds its first open until the announce ends; the rest open at once.
type firstBlocksTTS struct {
	inner   providers.TTS
	opening chan struct{}
	opens   atomic.Int32
}

func (f *firstBlocksTTS) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	if f.opens.Add(1) == 1 {
		close(f.opening)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.inner.Open(ctx, o)
}

func TestAnnounceStreamsToDevice(t *testing.T) {
	var tts *watchTTS
	r := newAnnounceRig(t, func(s *setup) {
		s.channels, s.bitrate = 2, 96000
		tts = &watchTTS{inner: s.tts}
		s.tts = tts
	})
	id := r.announce(t, `{"text":"  `+announceText+`  "}`)
	// answered once the first frame was out
	if got := describe(r.stream(id)); len(got) < 2 || got[0] != "stream_start" {
		t.Fatalf("at the answer %q", got)
	}
	r.waitEnd(t, id)
	// the fake says one tone of 300 ms: 15 frames of 20 ms
	wantFrames(t, r.stream(id), "stream_start", "audio×15", "stream_end")
	// nothing else went out: no session frames, no volume
	wantFrames(t, r.conn.all(), "stream_start", "audio×15", "stream_end")
	st := r.stream(id)[0].GetStreamStart()
	if st.GetSessionId() != "" || st.GetPurpose() != cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE ||
		st.GetCodec() != cadencev2.Codec_CODEC_OPUS || st.GetChannels() != 2 || !st.GetDuck() {
		t.Fatalf("stream start %v", st)
	}
	for i, e := range r.stream(id)[1:16] {
		if a := e.GetAudioDown(); a.GetSeq() != uint32(i) || len(a.GetOpus()) != 1 {
			t.Fatalf("audio %d: %v", i, a)
		}
	}
	opts, spoken, _ := tts.seen()
	want := providers.TTSOptions{Voice: defaultValues.Voice, SampleRate: 24000, Language: "en"}
	if len(opts) != 1 || opts[0] != want || strings.Join(spoken, "|") != announceText {
		t.Fatalf("tts %v %q", opts, spoken)
	}
	r.mu.Lock()
	enc := append([]audio.EncoderOptions(nil), r.opts...)
	r.mu.Unlock()
	wantEnc := audio.EncoderOptions{SampleRate: 24000, Channels: 2, Bitrate: 96000, Complexity: 10, Application: audio.AppAudio}
	if len(enc) != 1 || enc[0] != wantEnc {
		t.Fatalf("encoder %v", enc)
	}
	line := r.waitLine(t, id)
	if line["device_id"] != dev || line["code"] != "ok" || line["chars"] != float64(len(announceText)) {
		t.Fatalf("log %v", line)
	}
	if ms, ok := line["first_audio_ms"].(float64); !ok || ms < 0 {
		t.Fatalf("log %v", line)
	}
	waitFor(t, "tts closed", func() bool { _, _, c := tts.seen(); return c == 1 })

	// the body's language wins over the setting
	id2 := r.announce(t, `{"text":"`+announceText+`","language":"ru"}`)
	r.waitEnd(t, id2)
	if opts, _, _ = tts.seen(); len(opts) != 2 || opts[1].Language != "ru" {
		t.Fatalf("tts %v", opts)
	}
	if id2 == id {
		t.Fatal("stream id reused")
	}
	if strings.Contains(r.logs.String(), announceText) {
		t.Fatalf("the text reached the log:\n%s", r.logs.String())
	}
}

func TestAnnouncePaced(t *testing.T) {
	ticks := make(chan time.Time)
	r := newAnnounceRig(t, func(s *setup) { s.ticks = ticks })
	id := r.announce(t, `{"text":"hello"}`)
	waitFor(t, "the burst", func() bool { return r.audioIn(id) == burstPackets })
	time.Sleep(50 * time.Millisecond)
	if n := r.audioIn(id); n != burstPackets {
		t.Fatalf("%d frames without a tick", n)
	}
	for n := burstPackets + 1; n <= 15; n++ {
		ticks <- time.Now()
		waitFor(t, "a paced frame", func() bool { return r.audioIn(id) == n })
	}
	r.waitEnd(t, id)
	wantFrames(t, r.stream(id), "stream_start", "audio×15", "stream_end")
}

func TestAnnounceRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		rig    func(t *testing.T) *announceRig
		path   string
		status int
		body   string
	}{
		"unknown device": {
			rig:  func(t *testing.T) *announceRig { return newAnnounceRig(t) },
			path: "/api/v1/devices/22222222-2222-3333-4444-555555555555/announce", status: 404, body: `{"error":"not_found"}`,
		},
		"never linked": {
			rig:    func(t *testing.T) *announceRig { return unlinkedRig(t) },
			status: 409, body: `{"error":"device_offline"}`,
		},
		"link ended": {
			rig: func(t *testing.T) *announceRig {
				r := newAnnounceRig(t)
				r.unlink()
				return r
			},
			status: 409, body: `{"error":"device_offline"}`,
		},
		"disconnected": {
			rig: func(t *testing.T) *announceRig {
				r := newAnnounceRig(t)
				r.c.Disconnected(dev)
				return r
			},
			status: 409, body: `{"error":"device_offline"}`,
		},
		"no audio output": {
			rig:    func(t *testing.T) *announceRig { return newAnnounceRig(t, func(s *setup) { s.noOutput = true }) },
			status: 409, body: `{"error":"no_audio"}`,
		},
		"voice session active": {
			rig: func(t *testing.T) *announceRig {
				r := newAnnounceRig(t)
				r.start("s1")
				return r
			},
			status: 409, body: `{"error":"busy"}`,
		},
		"no tts": {
			rig:    func(t *testing.T) *announceRig { return newAnnounceRig(t, func(s *setup) { s.values.TTS = "" }) },
			status: 409, body: `{"error":"tts_unconfigured"}`,
		},
		"unknown tts instance": {
			rig:    func(t *testing.T) *announceRig { return newAnnounceRig(t, func(s *setup) { s.values.TTS = "gone" }) },
			status: 409, body: `{"error":"tts_unconfigured"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := tc.rig(t)
			path := tc.path
			if path == "" {
				path = announcePath
			}
			status, body := r.post(t, path, `{"text":"`+announceText+`"}`)
			if status != tc.status || body != tc.body {
				t.Fatalf("%d %s", status, body)
			}
			noStreams(t, r)
			if strings.Contains(r.logs.String(), announceText) {
				t.Fatal("the text reached the log")
			}
		})
	}
}

type brokenDevices struct{}

func (brokenDevices) Canonical(context.Context, string) (string, error) {
	return "", errors.New("db down: secret-db-text")
}

// a lookup failure can carry driver text: neither the body nor the log has it
func TestAnnounceLookupFailureIsInternal(t *testing.T) {
	r := newAnnounceRig(t)
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { RegisterAdmin(a, r.c, brokenDevices{}) }},
	})
	if status, body := r.post(t, announcePath, `{"text":"`+announceText+`"}`); status != 500 || body != `{"error":"internal"}` {
		t.Fatalf("%d %s", status, body)
	}
	noStreams(t, r)
	if logs := r.logs.String(); !strings.Contains(logs, "announce_lookup_failed") || strings.Contains(logs, "secret") ||
		strings.Contains(logs, announceText) {
		t.Fatalf("log: %s", logs)
	}
}

// the busy announce goes through once the session ended
func TestAnnounceAfterSession(t *testing.T) {
	r := newAnnounceRig(t)
	r.start("s1")
	if status, body := r.post(t, announcePath, `{"text":"hi"}`); status != 409 {
		t.Fatalf("%d %s", status, body)
	}
	if line := r.waitLine(t, ""); line["code"] != "busy" || line["chars"] != float64(2) {
		t.Fatalf("log %v", line)
	}
	r.cancel("s1")
	r.waitTurn("s1")
	r.waitEnd(t, r.announce(t, `{"text":"hi"}`))
}

func TestAnnounceInvalidBody(t *testing.T) {
	r := newAnnounceRig(t)
	for _, body := range []string{
		``, `[]`, `"hi"`, `{}`, `{"text":""}`, `{"text":"  \n "}`, `{"text":null}`, `{"text":5}`,
		`{"text":"hi","extra":1}`, `{"Text":"hi"}`, `{"text":"hi","language":"de"}`, `{"text":"hi","language":null}`,
		`{"text":"hi","language":""}`, `{"text":"hi"} {}`, `{"text":"` + strings.Repeat("a", 501) + `"}`,
	} {
		if status, resp := r.post(t, announcePath, body); status != 422 || resp != `{"error":"announce_invalid"}` {
			t.Fatalf("%.40q: %d %s", body, status, resp)
		}
	}
	noStreams(t, r)
	// 500 characters of two bytes each
	r.waitEnd(t, r.announce(t, `{"text":" `+strings.Repeat("я", 500)+` "}`))
}

func TestAnnounceTTSFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		edit   func(*setup)
		status int
		body   string
	}{
		"open fails":         {func(s *setup) { s.tts = failTTS{} }, 502, `{"error":"tts_failed"}`},
		"fails at once":      {func(s *setup) { s.tts = &scriptTTS{err: errTTS} }, 502, `{"error":"tts_failed"}`},
		"ends with no audio": {func(s *setup) { s.tts = &scriptTTS{} }, 502, `{"error":"tts_failed"}`},
		"no encoder":         {func(s *setup) { s.encoderErr = audio.ErrUnavailable }, 502, `{"error":"tts_failed"}`},
		"panics":             {func(s *setup) { s.tts = panicTTS{} }, 500, `{"error":"internal"}`},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAnnounceRig(t, tc.edit)
			status, body := r.post(t, announcePath, `{"text":"`+announceText+`"}`)
			if status != tc.status || body != tc.body {
				t.Fatalf("%d %s", status, body)
			}
			noStreams(t, r)
			if strings.Contains(r.logs.String(), announceText) || strings.Contains(r.logs.String(), "secret-xyz") {
				t.Fatal("private text reached the log")
			}
		})
	}
}

var errTTS = errors.New(providers.CodeTTSFailed)

func TestAnnounceTTSTimeout(t *testing.T) {
	tts := &scriptTTS{hang: true}
	r := newAnnounceRig(t, func(s *setup) {
		s.tts = tts
		s.lim = func(l *limits) { l.announceWait = 50 * time.Millisecond }
	})
	status, body := r.post(t, announcePath, `{"text":"hi"}`)
	if status != 504 || body != `{"error":"tts_timeout"}` {
		t.Fatalf("%d %s", status, body)
	}
	waitFor(t, "tts closed", func() bool { return tts.closed.Load() == 1 })
	waitFor(t, "announce line", func() bool { return len(r.announceLines()) == 1 })
	if line := r.announceLines()[0]; line["code"] != "tts_timeout" || line["stream_id"] == "" || line["first_audio_ms"] != nil {
		t.Fatalf("log %v", line)
	}
	noStreams(t, r)
}

// once audio went out, a failure only ends the stream early
func TestAnnounceFailsAfterFirstAudio(t *testing.T) {
	// 100 ms of audio, then the failure
	r := newAnnounceRig(t, func(s *setup) { s.tts = &scriptTTS{pcm: [][]int16{make([]int16, 2400)}, err: errTTS} })
	id := r.announce(t, `{"text":"hi"}`)
	r.waitEnd(t, id)
	wantFrames(t, r.stream(id), "stream_start", "audio×5", "stream_end")
	if line := r.waitLine(t, id); line["code"] != "tts_failed" {
		t.Fatalf("log %v", line)
	}
}

func TestSecondAnnounceEndsFirst(t *testing.T) {
	// no tick ever comes: each stalls after its burst
	r := newAnnounceRig(t, func(s *setup) { s.ticks = make(chan time.Time) })
	first := r.announce(t, `{"text":"one"}`)
	waitFor(t, "the first burst", func() bool { return r.audioIn(first) == burstPackets })
	second := r.announce(t, `{"text":"two"}`)
	endFirst, startSecond := -1, -1
	for i, e := range r.conn.all() {
		if e.GetStreamEnd().GetStreamId() == first {
			endFirst = i
		}
		if e.GetStreamStart().GetStreamId() == second {
			startSecond = i
		}
	}
	if endFirst < 0 || startSecond < endFirst {
		t.Fatalf("first ended at %d, second began at %d", endFirst, startSecond)
	}
	wantFrames(t, r.stream(first), "stream_start", "audio×10", "stream_end")
	if line := r.waitLine(t, first); line["code"] != "superseded" {
		t.Fatalf("log %v", line)
	}
}

// one still waiting for tts when the next comes gives up without a stream
func TestAnnounceSupersededBeforeAudio(t *testing.T) {
	tts := &firstBlocksTTS{opening: make(chan struct{})}
	r := newAnnounceRig(t, func(s *setup) {
		tts.inner = s.tts
		s.tts = tts
	})
	type answer struct {
		status int
		body   string
	}
	firstDone := make(chan answer, 1)
	go func() {
		status, body := r.post(t, announcePath, `{"text":"one"}`)
		firstDone <- answer{status, body}
	}()
	select {
	case <-tts.opening:
	case a := <-firstDone:
		t.Fatalf("first answered %d %s before tts opened", a.status, a.body)
	case <-time.After(5 * time.Second):
		t.Fatal("tts never opened")
	}
	second := r.announce(t, `{"text":"two"}`)
	if a := <-firstDone; a.status != 409 || a.body != `{"error":"superseded"}` {
		t.Fatalf("first %d %s", a.status, a.body)
	}
	r.waitEnd(t, second)
	wantFrames(t, r.conn.all(), "stream_start", "audio×15", "stream_end")
}

// a link that ends mid-stream gets nothing more, not even the end
func TestAnnounceLinkEnds(t *testing.T) {
	r := newAnnounceRig(t, func(s *setup) { s.ticks = make(chan time.Time) })
	id := r.announce(t, `{"text":"hi"}`)
	waitFor(t, "the burst", func() bool { return r.audioIn(id) == burstPackets })
	r.unlink()
	if line := r.waitLine(t, id); line["code"] != "disconnected" {
		t.Fatalf("log %v", line)
	}
	wantFrames(t, r.stream(id), "stream_start", "audio×10")
}

// hookConn runs before ahead of each send, outside the recording conn's lock.
type hookConn struct {
	*fakeConn
	before func(*cadencev2.Envelope)
}

func (c *hookConn) Send(env *cadencev2.Envelope) error {
	c.before(env)
	return c.fakeConn.Send(env)
}

// oneStreamAtATime checks that a stream begins only once every earlier one
// ended, and that no frame of a stream follows its end.
func oneStreamAtATime(t *testing.T, frames []*cadencev2.Envelope) {
	t.Helper()
	open := ""
	ended := map[string]bool{}
	for i, e := range frames {
		id := streamOf(e)
		switch {
		case id == "":
		case ended[id]:
			t.Fatalf("frame %d of %s after its end: %q", i, id, describe(frames))
		case e.GetStreamStart() != nil:
			if open != "" {
				t.Fatalf("%s began at %d while %s was open: %q", id, i, open, describe(frames))
			}
			open = id
		case id != open:
			t.Fatalf("frame %d of %s while %s was open: %q", i, id, open, describe(frames))
		case e.GetStreamEnd() != nil:
			ended[id], open = true, ""
		}
	}
}

// regression: the second announce stopped the first only after it let go of
// the coordinator, so a third could begin while the first was still open
func TestThreeOverlappingAnnounces(t *testing.T) {
	// no tick ever comes: each stalls after its burst
	r := unlinkedRig(t, func(s *setup) { s.ticks = make(chan time.Time) })
	ending, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	conn := &hookConn{fakeConn: r.conn, before: func(e *cadencev2.Envelope) {
		// the first stream's end stays in flight until the gate opens
		if e.GetStreamEnd() != nil {
			once.Do(func() { close(ending); <-gate })
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.c.Linked(ctx, conn)
	first := r.announce(t, `{"text":"one"}`)
	waitFor(t, "the first burst", func() bool { return r.audioIn(first) == burstPackets })
	answers := make(chan int, 2)
	post := func(text string) {
		status, _ := r.post(t, announcePath, `{"text":"`+text+`"}`)
		answers <- status
	}
	go post("two")
	select {
	case <-ending:
	case <-time.After(5 * time.Second):
		t.Fatal("the second never ended the first")
	}
	go post("three")
	// time for the third to begin, were it let through
	time.Sleep(100 * time.Millisecond)
	close(gate)
	for range 2 {
		select {
		case status := <-answers:
			// the second may be replaced before its first frame
			if status != fiber.StatusAccepted && status != fiber.StatusConflict {
				t.Fatalf("answered %d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
	}
	oneStreamAtATime(t, r.conn.all())
	if starts := strings.Count(strings.Join(describe(r.conn.all()), " "), "stream_start"); starts < 2 {
		t.Fatalf("%d streams began", starts)
	}
}

// a device that cannot take the first frame is gone for the caller, not a server error
func TestAnnounceBackpressureBeforeAudio(t *testing.T) {
	r := newAnnounceRig(t)
	r.conn.mu.Lock()
	r.conn.fail = func(e *cadencev2.Envelope) error {
		if e.GetAudioDown() != nil {
			return gateway.ErrBackpressure
		}
		return nil
	}
	r.conn.mu.Unlock()
	if status, body := r.post(t, announcePath, `{"text":"hi"}`); status != 409 || body != `{"error":"device_offline"}` {
		t.Fatalf("%d %s", status, body)
	}
	waitFor(t, "announce line", func() bool { return len(r.announceLines()) == 1 })
	if line := r.announceLines()[0]; line["code"] != "device_offline" {
		t.Fatalf("log %v", line)
	}
}

// an announcement counts as under way until its stream ended, and its end is
// heard: the task reporter never cuts one short with the next
func TestAnnouncingUntilItsEndIsHeard(t *testing.T) {
	ticks := make(chan time.Time)
	r := newAnnounceRig(t, func(s *setup) { s.ticks = ticks })
	if r.c.Announcing(dev) {
		t.Fatal("announcing before any announcement")
	}
	id := r.announce(t, `{"text":"hello"}`)
	if !r.c.Announcing(dev) || r.c.Announcing("other") {
		t.Fatal("not announcing, or announcing elsewhere")
	}
	if _, n := r.ends(); n != 0 {
		t.Fatalf("%d ends heard mid-stream", n)
	}
	waitFor(t, "the burst", func() bool { return r.audioIn(id) == burstPackets })
	for n := burstPackets + 1; n <= 15; n++ {
		ticks <- time.Now()
		waitFor(t, "a paced frame", func() bool { return r.audioIn(id) == n })
	}
	r.waitEnd(t, id)
	waitFor(t, "the end heard", func() bool { _, n := r.ends(); return n == 1 })
	if r.c.Announcing(dev) {
		t.Fatal("announcing after the stream ended")
	}
}

// an ha token speaks an announcement (M7a-D6)
func TestHATokenAnnounces(t *testing.T) {
	r := newAnnounceRig(t)
	status, body := apitest.Call(t, r.app, apitest.HABearer, http.MethodPost, announcePath, `{"text":"`+announceText+`"}`)
	if status != fiber.StatusAccepted || !strings.Contains(body, `"streamId"`) {
		t.Fatalf("%d %s", status, body)
	}
	var out struct {
		StreamID string `json:"streamId"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	r.waitEnd(t, out.StreamID)
}

// regression: an unset voice refused every announcement (409 tts_unconfigured) although turns speak without one;
// the tts module's own default voice speaks it
func TestAnnounceWithoutAVoice(t *testing.T) {
	var tts *watchTTS
	r := newAnnounceRig(t, func(s *setup) {
		s.values.Voice = ""
		tts = &watchTTS{inner: s.tts}
		s.tts = tts
	})
	id := r.announce(t, `{"text":"`+announceText+`"}`)
	r.waitEnd(t, id)
	if opts, spoken, _ := tts.seen(); len(opts) != 1 || opts[0].Voice != "" || strings.Join(spoken, "|") != announceText {
		t.Fatalf("tts %v %q", opts, spoken)
	}
}
