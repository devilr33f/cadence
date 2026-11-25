package audiod_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setup(t *testing.T) (audio.IO, *fakeAudiod, *syncBuf, *audiod.Client) {
	t.Helper()
	f := newFake(t)
	logs := &syncBuf{}
	c, err := audiod.Dial(context.Background(), f.path, audiod.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return audiod.NewTestIO(c), f, logs, c
}

func within[T any](t *testing.T, ch <-chan T) (T, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
		panic("unreachable")
	}
}

func micOpus(sid string, seq uint32, b byte) *audiodv1.Envelope {
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_MicOpus{MicOpus: &audiodv1.MicOpus{SessionId: sid, Seq: seq, Opus: []byte{b}}}}
}

func TestMicOverAudiod(t *testing.T) {
	io, f, logs, _ := setup(t)
	fc := f.accept(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := io.Mic.Open(ctx, audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	sid := recvEnv(t, fc).GetCaptureStart().GetSessionId()
	if sid == "" {
		t.Fatal("no session id")
	}
	sendEnv(t, fc, micOpus("other", 0, 9))
	sendEnv(t, fc, micOpus(sid, 0, 1))
	sendEnv(t, fc, micOpus(sid, 1, 2))
	for _, want := range []byte{1, 2} {
		p, ok := within(t, ch)
		if !ok || len(p) != 1 || p[0] != want {
			t.Fatalf("got %v %v", p, ok)
		}
	}
	cancel()
	if got := recvEnv(t, fc).GetCaptureStop().GetSessionId(); got != sid {
		t.Fatalf("stop for %q", got)
	}
	if _, ok := within(t, ch); ok {
		t.Fatal("channel open")
	}

	ch, err = io.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	sid2 := recvEnv(t, fc).GetCaptureStart().GetSessionId()
	failed := &audiodv1.CaptureFailed{SessionId: sid2, Reason: "device"}
	sendEnv(t, fc, &audiodv1.Envelope{Body: &audiodv1.Envelope_CaptureFailed{CaptureFailed: failed}})
	if _, ok := within(t, ch); ok {
		t.Fatal("channel open after failure")
	}
	if !strings.Contains(logs.String(), "audiod_capture_failed") {
		t.Fatalf("logs: %s", logs.String())
	}
}

func TestMicFirstPacketTimeout(t *testing.T) {
	io, f, logs, _ := setup(t)
	fc := f.accept(t)
	ch, err := io.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	sid := recvEnv(t, fc).GetCaptureStart().GetSessionId()
	if _, ok := within(t, ch); ok {
		t.Fatal("channel open")
	}
	if recvEnv(t, fc).GetCaptureStop().GetSessionId() != sid {
		t.Fatal("no stop")
	}
	if !strings.Contains(logs.String(), "audiod_mic_timeout") {
		t.Fatalf("logs: %s", logs.String())
	}
}

func TestSpeakerOverAudiod(t *testing.T) {
	io, f, _, _ := setup(t)
	fc := f.accept(t)
	w, err := io.Speaker.Begin(&cadencev2.StreamStart{StreamId: "s1", Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st := recvEnv(t, fc).GetStreamStart(); st.GetStreamId() != "s1" || st.GetChannels() != 1 {
		t.Fatalf("start %v", st)
	}
	if err := w.Write(7, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if ad := recvEnv(t, fc).GetAudioDown(); ad.GetStreamId() != "s1" || ad.GetSeq() != 7 || len(ad.GetOpus()) != 3 {
		t.Fatalf("down %v", ad)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	if recvEnv(t, fc).GetStreamEnd().GetStreamId() != "s1" {
		t.Fatal("no end")
	}
	done := &audiodv1.StreamDone{StreamId: "s1", PlayedMs: 20}
	sendEnv(t, fc, &audiodv1.Envelope{Body: &audiodv1.Envelope_StreamDone{StreamDone: done}})
}

// audiod drops the client for a field over its bounds, which the supervisor
// counts as a crash, so the sender refuses it and sends nothing
func TestSpeakerRefusesFieldsAudiodDrops(t *testing.T) {
	io, f, _, _ := setup(t)
	fc := f.accept(t)
	for _, id := range []string{"", "../x", "a b", "é", strings.Repeat("a", 65)} {
		if _, err := io.Speaker.Begin(&cadencev2.StreamStart{StreamId: id}); err == nil {
			t.Fatalf("stream id %q accepted", id)
		}
	}
	longest := strings.Repeat("a", 64)
	w, err := io.Speaker.Begin(&cadencev2.StreamStart{StreamId: longest})
	if err != nil {
		t.Fatal(err)
	}
	if got := recvEnv(t, fc).GetStreamStart().GetStreamId(); got != longest {
		t.Fatalf("first envelope starts %q", got)
	}
	if err := w.Write(0, make([]byte, 4001)); err == nil {
		t.Fatal("4001-byte opus accepted")
	}
	if err := w.Write(1, make([]byte, 4000)); err != nil {
		t.Fatal(err)
	}
	if ad := recvEnv(t, fc).GetAudioDown(); ad.GetSeq() != 1 || len(ad.GetOpus()) != 4000 {
		t.Fatalf("down seq %d, %d bytes", ad.GetSeq(), len(ad.GetOpus()))
	}
}

// End returns at once; only a goroutine waits, bounded, and logs
func TestSpeakerEndBounded(t *testing.T) {
	io, f, logs, _ := setup(t)
	fc := f.accept(t)
	w, err := io.Speaker.Begin(&cadencev2.StreamStart{StreamId: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	recvEnv(t, fc)
	start := time.Now()
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("End waited %v", d)
	}
	recvEnv(t, fc)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "audiod_stream_timeout") {
		if time.Now().After(deadline) {
			t.Fatalf("no timeout log: %s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAbortCutsWithoutWaiting(t *testing.T) {
	io, f, logs, _ := setup(t)
	fc := f.accept(t)
	w, err := io.Speaker.Begin(&cadencev2.StreamStart{StreamId: "s3"})
	if err != nil {
		t.Fatal(err)
	}
	recvEnv(t, fc)
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	recvEnv(t, fc)
	start := time.Now()
	w.Abort()
	w.Abort()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Abort waited %v", d)
	}
	if got := recvEnv(t, fc).GetStreamCancel().GetStreamId(); got != "s3" {
		t.Fatalf("cancel for %q", got)
	}
	// idempotent: the second Abort sent nothing, and the aborted wait logs no timeout
	sendEnv(t, fc, &audiodv1.Envelope{Body: &audiodv1.Envelope_Pong{Pong: &audiodv1.HealthPong{Seq: 1}}})
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(logs.String(), "audiod_stream_timeout") {
		t.Fatal("aborted stream logged a timeout")
	}
}

func TestMicFailureReported(t *testing.T) {
	io, f, _, _ := setup(t)
	fc := f.accept(t)
	rep, ok := io.Mic.(audio.MicFailureReporter)
	if !ok {
		t.Fatal("mic does not report failures")
	}
	ch, err := io.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	sid := recvEnv(t, fc).GetCaptureStart().GetSessionId()
	failed := &audiodv1.CaptureFailed{SessionId: sid, Reason: "device"}
	sendEnv(t, fc, &audiodv1.Envelope{Body: &audiodv1.Envelope_CaptureFailed{CaptureFailed: failed}})
	if _, ok := within(t, ch); ok {
		t.Fatal("channel open")
	}
	if !rep.MicFailure(ch) {
		t.Fatal("failure not reported")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch2, err := io.Mic.Open(ctx, audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	recvEnv(t, fc)
	cancel()
	if _, ok := within(t, ch2); ok {
		t.Fatal("channel open")
	}
	if rep.MicFailure(ch2) {
		t.Fatal("normal stop reported as failure")
	}
}

func TestClientRejectsGarbage(t *testing.T) {
	for name, payload := range map[string][]byte{
		"garbage":  {0xff, 0xff, 0xff},
		"oversize": make([]byte, 16385),
	} {
		t.Run(name, func(t *testing.T) {
			_, f, logs, c := setup(t)
			fc := f.accept(t)
			if _, err := fc.Write(payload); err != nil {
				t.Fatal(err)
			}
			if _, ok := within(t, c.Recv()); ok {
				t.Fatal("got an envelope")
			}
			if !strings.Contains(logs.String(), "audiod_ipc_invalid") {
				t.Fatalf("logs: %s", logs.String())
			}
			if err := c.Send(&audiodv1.Envelope{}); err == nil {
				t.Fatal("send after close succeeded")
			}
		})
	}
}

func TestClientSkipsUnknownBody(t *testing.T) {
	f := newFake(t)
	c, err := audiod.Dial(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	fc := f.accept(t)
	if _, err := fc.Write([]byte{0xf8, 0x06, 0x01}); err != nil {
		t.Fatal(err)
	}
	sendEnv(t, fc, &audiodv1.Envelope{Body: &audiodv1.Envelope_Pong{Pong: &audiodv1.HealthPong{Seq: 5}}})
	env, ok := within(t, c.Recv())
	if !ok || env.GetPong().GetSeq() != 5 {
		t.Fatalf("got %v %v", env, ok)
	}
}

// a dead audiod is a failed capture, not a normal stop
func TestMicFailsWhenAudiodDies(t *testing.T) {
	io, f, _, _ := setup(t)
	fc := f.accept(t)
	ch, err := io.Mic.Open(context.Background(), audio.Trigger{})
	if err != nil {
		t.Fatal(err)
	}
	sid := recvEnv(t, fc).GetCaptureStart().GetSessionId()
	sendEnv(t, fc, micOpus(sid, 0, 1))
	if _, ok := within(t, ch); !ok {
		t.Fatal("no first packet")
	}
	_ = fc.Close()
	if _, ok := within(t, ch); ok {
		t.Fatal("channel open after audiod died")
	}
	if !io.Mic.(audio.MicFailureReporter).MicFailure(ch) {
		t.Fatal("dead audiod not reported as a mic failure")
	}
}
