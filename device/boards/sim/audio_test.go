package sim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opusframes"
)

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newAudio(t *testing.T, log io.Writer) (audio.IO, string) {
	t.Helper()
	dir := t.TempDir()
	b := sim.New(func(string) string { return "1" }, dir, slog.New(slog.NewTextHandler(log, nil)))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	aio, ok := b.AudioIO()
	if !ok {
		t.Fatal("no audio io")
	}
	return aio, dir
}

func say(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sim"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sim", "say"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSayFileTriggers(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	say(t, dir, "what-time")
	wantSayTrigger(t, aio)
	if _, err := os.Stat(filepath.Join(dir, "sim", "say")); !os.IsNotExist(err) {
		t.Fatalf("say file kept: %v", err)
	}
	select {
	case tr := <-aio.Triggers:
		t.Fatalf("second trigger %+v", tr)
	case <-time.After(500 * time.Millisecond):
	}
}

// wantSayTrigger: a say file begins a button session in which test audio speaks
func wantSayTrigger(t *testing.T, aio audio.IO) {
	t.Helper()
	select {
	case tr := <-aio.Triggers:
		if tr != (audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: "what-time", Test: true}) {
			t.Fatalf("trigger = %+v", tr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no trigger")
	}
}

// a follow-up's microphone hears nothing until a say file names its utterance, which then plays into it in place of a
// new session (M6a-R4); once it played, a say file begins a session again
func TestAFollowUpHearsTheSayFile(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := aio.Mic.Open(ctx, audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_FOLLOW_UP, Test: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-ch:
		t.Fatalf("a packet before any say file: %v", p)
	case <-time.After(500 * time.Millisecond):
	}
	say(t, dir, "what-time")
	n := 0
	for range ch {
		n++
	}
	if n < 40 {
		t.Fatalf("packets = %d", n)
	}
	if r, ok := aio.Mic.(audio.MicFailureReporter); !ok || r.MicFailure(ch) {
		t.Fatal("the utterance did not end normally")
	}
	select {
	case tr := <-aio.Triggers:
		t.Fatalf("the say file began a session: %+v", tr)
	default:
	}
	say(t, dir, "what-time")
	wantSayTrigger(t, aio)
}

// with no say file a follow-up stays silent until its session ends, and the say file after it begins a session
func TestASilentFollowUpEndsWithItsSession(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := aio.Mic.Open(ctx, audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_FOLLOW_UP})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("a packet from a silent follow-up")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the microphone outlived its session")
	}
	say(t, dir, "what-time")
	wantSayTrigger(t, aio)
}

func TestUnknownFixture(t *testing.T) {
	buf := &logBuf{}
	aio, dir := newAudio(t, buf)
	say(t, dir, "nope")
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "sim_fixture_unknown") {
		if time.Now().After(deadline) {
			t.Fatalf("no log: %s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case tr := <-aio.Triggers:
		t.Fatalf("trigger %+v", tr)
	default:
	}
	if _, err := os.Stat(filepath.Join(dir, "sim", "say")); !os.IsNotExist(err) {
		t.Fatalf("say file kept: %v", err)
	}
}

func TestSpeakerWritesSummary(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	w, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: "st-1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := w.Write(uint32(i), []byte{1, 2, byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "sim", "out", "st-1.opusframes"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ps, err := opusframes.Read(f)
	if err != nil || len(ps) != 3 {
		t.Fatalf("packets = %d, err = %v", len(ps), err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sim", "out", "st-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum map[string]any
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if sum["packets"] != float64(3) || sum["duration_ms"] != float64(60) || sum["purpose"] != "reply" || len(sum) != 3 {
		t.Fatalf("summary = %s", raw)
	}
}

// the e2e finds an announcement's stream by its summary
func TestSpeakerSummaryNamesThePurpose(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	for id, p := range map[string]cadencev2.StreamPurpose{
		"st-a": cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE,
		"st-u": cadencev2.StreamPurpose_STREAM_PURPOSE_UNSPECIFIED,
	} {
		w, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: id, Purpose: p})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.End(); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"st-a": "announce", "st-u": "other"} {
		raw, err := os.ReadFile(filepath.Join(dir, "sim", "out", id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var sum struct{ Purpose string }
		if err := json.Unmarshal(raw, &sum); err != nil || sum.Purpose != want {
			t.Fatalf("%s summary = %s, want purpose %q", id, raw, want)
		}
	}
}

func TestSpeakerRefusesUnsafeStreamID(t *testing.T) {
	aio, dir := newAudio(t, io.Discard)
	for _, id := range []string{"", "../x", "a/b", `a\b`} {
		if _, err := aio.Speaker.Begin(&cadencev2.StreamStart{StreamId: id}); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.opusframes")); err == nil {
		t.Fatal("wrote outside out dir")
	}
}

func TestFixtureEmbedded(t *testing.T) {
	aio, _ := newAudio(t, io.Discard)
	ch, err := aio.Mic.Open(context.Background(), audio.Trigger{Fixture: "what-time"})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for range ch {
		n++
	}
	if n < 40 {
		t.Fatalf("packets = %d", n)
	}
	if _, err := aio.Mic.Open(context.Background(), audio.Trigger{Fixture: "missing"}); err == nil {
		t.Fatal("unknown fixture opened")
	}
}

func TestNoAudioWithoutDataDir(t *testing.T) {
	if _, ok := sim.New(func(string) string { return "1" }, "", nil).AudioIO(); ok {
		t.Fatal("audio io without a data dir")
	}
}

// regression: nothing made <dataDir>/sim before the first reply, so a say file
// had no directory to land in on a fresh device
func TestAudioIOMakesSayDir(t *testing.T) {
	_, dir := newAudio(t, io.Discard)
	st, err := os.Stat(filepath.Join(dir, "sim"))
	if err != nil || !st.IsDir() {
		t.Fatalf("sim dir: %v", err)
	}
}
