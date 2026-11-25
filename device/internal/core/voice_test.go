package core_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/session"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// voiceSpeaker takes every reply frame at once
type voiceSpeaker struct{ frames atomic.Int32 }

func (s *voiceSpeaker) Begin(*cadencev2.StreamStart) (audio.StreamWriter, error) {
	return voiceWriter{s}, nil
}

type voiceWriter struct{ s *voiceSpeaker }

func (w voiceWriter) Write(uint32, []byte) error { w.s.frames.Add(1); return nil }
func (voiceWriter) End() error                   { return nil }
func (voiceWriter) Abort()                       {}

// levels are what the volume put on the hardware
type levels struct {
	mu  sync.Mutex
	all []int
}

func (l *levels) apply(level int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, level)
}

func (l *levels) seen() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.all)
}

// voiceBoard is a speaker as pickle is: an input, a level it keeps, alarms, media and how its sessions began
type voiceBoard struct {
	fakeBoard
	in      *input.Input
	vol     *volume.Setter
	ringer  *alarms.Ringer
	recent  *session.Recent
	say     chan audio.Trigger
	speaker *voiceSpeaker
	playing *atomic.Bool
	pauses  *atomic.Int32
	nexts   *atomic.Int32
	prevs   *atomic.Int32
	resumes *atomic.Int32
	// what the input and the volume log
	log *syncBuf
}

func (b voiceBoard) Input() *input.Input       { return b.in }
func (b voiceBoard) Volume() *volume.Setter    { return b.vol }
func (b voiceBoard) Alarms() *alarms.Ringer    { return b.ringer }
func (b voiceBoard) Sessions() *session.Recent { return b.recent }
func (b voiceBoard) MediaPlaying() bool        { return b.playing.Load() }
func (b voiceBoard) MediaPause(context.Context) error {
	b.pauses.Add(1)
	return nil
}
func (b voiceBoard) MediaNext(context.Context) error {
	b.nexts.Add(1)
	return nil
}
func (b voiceBoard) MediaPrevious(context.Context) error {
	b.prevs.Add(1)
	return nil
}
func (b voiceBoard) MediaResume(context.Context) error {
	b.resumes.Add(1)
	return nil
}
func (b voiceBoard) AudioIO() (audio.IO, bool) {
	return audio.IO{Triggers: b.say, Mic: chanMic{}, Speaker: b.speaker}, true
}
func (b voiceBoard) Capabilities() []capability.Provider {
	return []capability.Provider{b.in.Capability()}
}

var (
	_ board.Volume        = voiceBoard{}
	_ board.MediaPlayback = voiceBoard{}
)

// newVoiceBoard keeps the level 50 under dir, with buttons for the switch
func newVoiceBoard(t *testing.T, dir string) (voiceBoard, fakeButtons, *levels) {
	t.Helper()
	logs := &syncBuf{}
	vol := volume.NewSetter(volume.NewStore(dir), 30, newLog(logs))
	if _, err := vol.Set(50, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	applied := &levels{}
	vol.Follow(func(int) func(int) { return applied.apply })
	buttons := make(fakeButtons)
	b := voiceBoard{
		fakeBoard: fakeBoard{id: "speaker"}, in: input.New(input.Options{Buttons: buttons, Volume: vol, Log: newLog(logs)}), vol: vol, log: logs,
		recent: &session.Recent{}, say: make(chan audio.Trigger, 1), speaker: &voiceSpeaker{}, playing: &atomic.Bool{},
		pauses: &atomic.Int32{}, nexts: &atomic.Int32{}, prevs: &atomic.Int32{}, resumes: &atomic.Int32{},
	}
	return b, buttons, applied
}

func word(in *input.Input, id uint32) {
	in.InputEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandWord{CommandWord: &audiodv1.CommandWord{Id: id, Score: 1}}})
}

func (p *peer) reply(session, stream string, frames int) {
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamStart{StreamStart: &cadencev2.StreamStart{
		StreamId: stream, SessionId: session, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY, Codec: cadencev2.Codec_CODEC_OPUS, Channels: 1,
	}}})
	for i := range frames {
		p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_AudioDown{AudioDown: &cadencev2.AudioDown{StreamId: stream, Seq: uint32(i), Opus: []byte{1}}}})
	}
}

func (p *peer) started() *cadencev2.SessionStart {
	env, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionStart() != nil })
	return env.GetSessionStart()
}

// тише acts only within 2 s of a reply frame the player took: it lowers the level a step, kept like a press, and ends
// the session its microphone streamed to; every other word outside a ring is the server's, and nothing acts while
// the switch mutes the microphone. The level in force goes back on the hardware before each reply. No phrase is logged
func TestCommandWordsOverTheLink(t *testing.T) {
	dir := t.TempDir()
	b, buttons, applied := newVoiceBoard(t, dir)
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")

	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	id := p.started().GetSessionId()
	word(b.in, 3)
	if !strings.Contains(b.log.String(), `"msg":"command_ignored","id":3,"reason":"gate","count":1`) {
		t.Fatalf("log: %s", b.log.String())
	}
	p.reply(id, "rp1", 2)
	eventually(t, "the reply's frames", func() bool { return b.speaker.frames.Load() == 2 })
	if got := applied.seen(); !slices.Equal(got, []int{50}) {
		t.Fatalf("applied before the reply %v", got)
	}

	word(b.in, 4)
	word(b.in, 0)
	word(b.in, 3)
	c, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetSessionCancel() != nil })
	if c.GetSessionCancel().GetSessionId() != id || c.GetSessionCancel().GetReason() != "command_volume" {
		t.Fatalf("cancel = %v", c)
	}
	if level, _ := volume.NewStore(dir).Load(); level != 50 || b.vol.Level() != 50 {
		t.Fatalf("level %d, stored %d", b.vol.Level(), level)
	}
	if got := applied.seen(); !slices.Equal(got, []int{50, 55, 50}) {
		t.Fatalf("applied %v", got)
	}

	// a home phrase with no session is handed to the server, which alone can act on it
	word(b.in, 18)
	wh, _ := p.until(func(env *cadencev2.Envelope) bool { return env.GetWordHeard() != nil })
	if wh.GetWordHeard().GetWord() != "выключи свет" {
		t.Fatalf("word heard %v", wh.GetWordHeard())
	}
	if !strings.Contains(b.log.String(), `"msg":"command","id":18,"action":"report_word"`) {
		t.Fatalf("log: %s", b.log.String())
	}

	buttons <- input.ButtonEvent{Kind: input.MuteOn}
	eventually(t, "the switch on", b.in.Muted)
	word(b.in, 3)
	out := b.log.String() + buf.String()
	for _, want := range []string{
		`"msg":"command","id":4,"action":"volume_up","count":1`,
		`"msg":"command_ignored","id":0,"reason":"server","count":2`,
		`"msg":"command","id":3,"action":"volume_down","count":2`,
		`"msg":"volume_set","volume":55,"origin":"word"`,
		`"msg":"volume_set","volume":50,"origin":"word"`,
		`"msg":"command_ignored","id":3,"reason":"muted","count":3`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %s in log: %s", want, out)
		}
	}
	for _, phrase := range input.CommandWords() {
		if strings.Contains(out, phrase) {
			t.Fatalf("a phrase in the log: %s", out)
		}
	}
}

// while a ring sounds стоп, хватит and пауза stop it, offline too, and every other word is ignored; the level in force
// goes back on the hardware each interval while media plays, never during the ring
func TestStopWordsStopARing(t *testing.T) {
	dir := t.TempDir()
	b, _, applied := newVoiceBoard(t, dir)
	discard := slog.New(slog.DiscardHandler)
	book, err := alarms.NewBook(alarms.StorePath(dir), alarms.SystemClock{}, discard)
	if err != nil {
		t.Fatal(err)
	}
	b.ringer = alarms.NewRinger(alarms.RingerOptions{Book: book, Volume: b.vol, Sessions: b.recent, Log: discard})
	speaker := &tones{r: b.ringer}
	b.ringer.UseSpeaker(speaker)
	startSync(t, b, openStore(t), func(s *core.Sync) { core.SetRecheckEvery(s, 10*time.Millisecond) })

	time.Sleep(50 * time.Millisecond)
	if n := len(applied.seen()); n != 0 {
		t.Fatalf("%d applied with nothing playing", n)
	}
	b.playing.Store(true)
	eventually(t, "the level put back while media plays", func() bool { return len(applied.seen()) >= 2 })

	if _, err := book.SetTimer("tea", 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the timer ringing", b.ringer.Ringing)
	time.Sleep(30 * time.Millisecond)
	during := len(applied.seen())
	word(b.in, 3)
	word(b.in, 4)
	time.Sleep(50 * time.Millisecond)
	if n := len(applied.seen()); n != during || !b.ringer.Ringing() || b.vol.Level() != 50 {
		t.Fatalf("during the ring: %d -> %d applied, ringing %v, level %d", during, n, b.ringer.Ringing(), b.vol.Level())
	}
	word(b.in, 1)
	eventually(t, "the word stopping the ring", func() bool { return speaker.has("tone_stop ring-1") && !b.ringer.Ringing() })
	eventually(t, "the level put back after the ring", func() bool { return len(applied.seen()) > during })

	if _, err := book.SetTimer("eggs", 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second timer ringing", func() bool { return speaker.has("tone_start ring-2") })
	word(b.in, 2)
	eventually(t, "the pause word stopping it", func() bool { return speaker.has("tone_stop ring-2") })
	out := b.log.String()
	for _, want := range []string{
		`"msg":"command_ignored","id":3,"reason":"ringing"`,
		`"msg":"command_ignored","id":4,"reason":"ringing"`,
		`"msg":"command","id":1,"action":"stop_ring","count":1`,
		`"msg":"command","id":2,"action":"stop_ring","count":2`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %s in log: %s", want, out)
		}
	}
}

// while media plays and no session streams — the link down, the server hearing nothing — the stop words pause the
// media on the speaker itself, the media words skip in it and тише steps the level down; громче stays the server's
// alone (rule 7), продолжи continues what stands, and the home's phrases wait for the server. No phrase is logged
func TestOfflineWordsPauseMedia(t *testing.T) {
	b, _, _ := newVoiceBoard(t, t.TempDir())
	startSync(t, b, openStore(t))
	time.Sleep(50 * time.Millisecond)
	b.playing.Store(true)

	word(b.in, 0)
	word(b.in, 2)
	eventually(t, "the stop words pausing the media", func() bool { return b.pauses.Load() == 2 })
	word(b.in, 3)
	eventually(t, "тише stepping the level", func() bool { return b.vol.Level() == 45 })
	word(b.in, 4)
	eventually(t, "громче stepping the level up", func() bool { return b.vol.Level() == 50 })
	time.Sleep(50 * time.Millisecond)
	if b.pauses.Load() != 2 {
		t.Fatalf("pauses %d", b.pauses.Load())
	}
	// дальше and следующий skip forward, назад back, while it plays
	word(b.in, 5)
	word(b.in, 6)
	word(b.in, 7)
	eventually(t, "the media words skipping", func() bool { return b.nexts.Load() == 2 && b.prevs.Load() == 1 })
	// продолжи with something playing is the server's
	word(b.in, 8)
	time.Sleep(50 * time.Millisecond)
	if b.resumes.Load() != 0 {
		t.Fatalf("resumes %d", b.resumes.Load())
	}

	b.playing.Store(false)
	word(b.in, 1)
	// with nothing playing продолжи continues what stands, a skip is the server's, the home's phrase is
	// reported to it, and тише still steps the level down: nothing else carries it anywhere
	word(b.in, 8)
	word(b.in, 5)
	word(b.in, 17)
	word(b.in, 3)
	eventually(t, "the resume", func() bool { return b.resumes.Load() == 1 })
	eventually(t, "тише with nothing playing", func() bool { return b.vol.Level() == 45 })
	time.Sleep(50 * time.Millisecond)
	out := b.log.String()
	for _, want := range []string{
		`"msg":"command","id":0,"action":"pause_media","count":1`,
		`"msg":"command","id":2,"action":"pause_media","count":2`,
		`"msg":"command","id":3,"action":"volume_down","count":3`,
		`"msg":"volume_set","volume":45,"origin":"word"`,
		`"msg":"command","id":4,"action":"volume_up","count":4`,
		`"msg":"volume_set","volume":50,"origin":"word"`,
		`"msg":"command","id":5,"action":"media_next","count":5`,
		`"msg":"command","id":6,"action":"media_next","count":6`,
		`"msg":"command","id":7,"action":"media_previous","count":7`,
		`"msg":"command","id":8,"action":"media_resume","count":8`,
		`"msg":"command_ignored","id":8,"reason":"server","count":1`,
		`"msg":"command_ignored","id":1,"reason":"server","count":2`,
		`"msg":"command_ignored","id":5,"reason":"server","count":3`,
		`"msg":"command","id":17,"action":"report_word","count":9`,
		`"msg":"command","id":3,"action":"volume_down","count":10`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %s in log: %s", want, out)
		}
	}
	for _, phrase := range input.CommandWords() {
		if strings.Contains(out, phrase) {
			t.Fatalf("a phrase in the log: %s", out)
		}
	}
}

// a reply heard whole opens the follow-up the server allows; the switch refuses one
func TestFollowUpOverTheLink(t *testing.T) {
	b, buttons, _ := newVoiceBoard(t, t.TempDir())
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	closeWithFollowUp := func(id string) {
		p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamEnd{StreamEnd: &cadencev2.StreamEnd{StreamId: "rp-" + id}}})
		p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
			SessionId: id, FollowUpAllowed: true, FollowUpWindowMs: 5000,
		}}})
	}

	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	id := p.started().GetSessionId()
	p.reply(id, "rp-"+id, 2)
	closeWithFollowUp(id)
	follow := p.started()
	if follow.GetTrigger() != cadencev2.Trigger_TRIGGER_FOLLOW_UP || follow.GetSessionId() == id {
		t.Fatalf("start = %v", follow)
	}
	if tr, ok := b.recent.Trigger(follow.GetSessionId()); !ok || tr != cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		t.Fatalf("recorded %v, %v", tr, ok)
	}
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: follow.GetSessionId()}}})

	buttons <- input.ButtonEvent{Kind: input.MuteOn}
	eventually(t, "the switch on", b.in.Muted)
	b.say <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle}
	id = p.started().GetSessionId()
	p.reply(id, "rp-"+id, 2)
	closeWithFollowUp(id)
	waitLog(t, buf, "follow_up_refused")
	if !strings.Contains(buf.String(), `"msg":"follow_up_refused","session_id":"`+id+`","reason":"muted"`) {
		t.Fatalf("log: %s", buf.String())
	}
}
