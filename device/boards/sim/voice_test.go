package sim_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// a command word audiod hears reaches core's policy, which leaves пауза outside a ring to the server
func TestSimVoice(t *testing.T) {
	b, _, logs := newAudiodSim(t, "--wake")
	linkUp(t, b, slog.New(slog.NewJSONHandler(logs, nil)))
	waitFor(t, "the command word", func() bool {
		return strings.Contains(logs.String(), `"msg":"command_ignored","id":2,"reason":"server","count":1`)
	})
}

// until reads frames until one matches
func (p *linkPeer) until(match func(*cadencev2.Envelope) bool) *cadencev2.Envelope {
	p.t.Helper()
	for {
		if env := p.recv(); match(env) {
			return env
		}
	}
}

func (p *linkPeer) started() *cadencev2.SessionStart {
	p.t.Helper()
	return p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionStart() != nil }).GetSessionStart()
}

// reply plays a one-frame reply in session id and closes it with the server's leave for a follow-up of window ms
func (p *linkPeer) reply(id, stream string, window uint32) {
	p.t.Helper()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamStart{StreamStart: &cadencev2.StreamStart{
		StreamId: stream, SessionId: id, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY, Codec: cadencev2.Codec_CODEC_OPUS, Channels: 1,
	}}})
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_AudioDown{AudioDown: &cadencev2.AudioDown{StreamId: stream, Opus: []byte{1}}}})
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamEnd{StreamEnd: &cadencev2.StreamEnd{StreamId: stream}}})
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
		SessionId: id, FollowUpAllowed: true, FollowUpWindowMs: window,
	}}})
}

func captures(dir string) int {
	n := 0
	for _, r := range audiodtest.Records(filepath.Join(dir, "run")) {
		if r.Kind == "capture_start" {
			n++
		}
	}
	return n
}

// on the sim the say file is the microphone (M6a-R4): a reply in its session opens a follow-up, a say file then speaks
// into that follow-up instead of beginning a session, and a follow-up no say file speaks into ends with its window
func TestSimFollowUp(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	p := linkUp(t, b, slog.New(slog.NewJSONHandler(logs, nil)))
	say(t, dir, "what-time")
	first := p.started()
	p.reply(first.GetSessionId(), "rp1", 5000)
	fu := p.started()
	if fu.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("start = %v", fu)
	}
	// its microphone waits for a say file: audiod starts no capture before one
	time.Sleep(500 * time.Millisecond)
	if n := captures(dir); n != 1 {
		t.Fatalf("%d captures before the follow-up's say file", n)
	}
	say(t, dir, "what-time")
	p.until(func(env *cadencev2.Envelope) bool {
		if s := env.GetSessionStart(); s != nil {
			t.Fatalf("the say file began a session: %v", s)
		}
		return env.GetMicAudio().GetSessionId() == fu.GetSessionId()
	})

	p.reply(fu.GetSessionId(), "rp2", 300)
	silent := p.started()
	if silent.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("start = %v", silent)
	}
	c := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionCancel() != nil }).GetSessionCancel()
	if c.GetSessionId() != silent.GetSessionId() || c.GetReason() != "follow_up_silence" {
		t.Fatalf("cancel = %v", c)
	}
	if strings.Contains(logs.String(), "follow_up_refused") {
		t.Fatalf("log: %s", logs)
	}
}

func press(t *testing.T, dir, word string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "press"), []byte(word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gone(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// the press file acts as the button it names, once, as the real one does: a volume press steps the level, and while
// an alarm or a timer rings any press stops it without a step; a word that names no button is dropped, and an empty
// file, which may be half written, waits
func TestSimPressFile(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	linkUp(t, b, slog.New(slog.NewJSONHandler(logs, nil)))
	path := filepath.Join(dir, "press")
	press(t, dir, "volume_up")
	waitFor(t, "the step", func() bool {
		return strings.Contains(logs.String(), `"msg":"volume_set","volume":55,"origin":"button"`) && gone(path)
	})

	press(t, dir, "jump")
	waitFor(t, "the word dropped", func() bool { return strings.Contains(logs.String(), `"msg":"sim_press_unknown"`) && gone(path) })
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if gone(path) {
		t.Fatal("an empty press file was taken")
	}

	r := b.(board.Alarms).Alarms()
	if _, err := r.Book().SetTimer("tea", 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ring", r.Ringing)
	press(t, dir, "volume_down")
	waitFor(t, "the press stopping the ring", func() bool {
		return strings.Contains(logs.String(), `"msg":"ring_stop","kind":"timer","id":"tea","reason":"volume"`)
	})
	if n := strings.Count(logs.String(), `"msg":"volume_set"`); n != 1 {
		t.Fatalf("%d steps: %s", n, logs)
	}
}
