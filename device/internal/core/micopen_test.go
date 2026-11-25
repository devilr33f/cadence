package core_test

import (
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/input"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the room's screen asked (rpc assistant.invoke): the speaker opens a session begun by a press, under the server's id
func TestMicOpenBeginsAPressedSessionUnderTheServersID(t *testing.T) {
	b, buttons, _ := newVoiceBoard(t, t.TempDir())
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")

	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_MicOpen{MicOpen: &cadencev2.MicOpen{
		SessionId: "srv-1", OriginDeviceId: "tv",
	}}})
	st := p.started()
	if st.GetSessionId() != "srv-1" || st.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("start = %v", st)
	}
	if tr, ok := b.recent.Trigger("srv-1"); !ok || tr != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("recent = %v %v", tr, ok)
	}

	// the switch mutes it as it would a press
	buttons <- input.ButtonEvent{Kind: input.MuteOn}
	eventually(t, "the switch on", b.in.Muted)
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_MicOpen{MicOpen: &cadencev2.MicOpen{SessionId: "srv-2"}}})
	eventually(t, "the refusal", func() bool {
		return strings.Contains(buf.String(), `"msg":"activation_refused","reason":"muted","source":"mic_open"`)
	})
}

type duckBoard struct {
	voiceBoard
	mu    *sync.Mutex
	ducks *[]*cadencev2.Duck
}

func (b duckBoard) ServerDuck(d *cadencev2.Duck) {
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.ducks = append(*b.ducks, d)
}

func TestDuckReachesTheBoard(t *testing.T) {
	vb, _, _ := newVoiceBoard(t, t.TempDir())
	b := duckBoard{voiceBoard: vb, mu: &sync.Mutex{}, ducks: &[]*cadencev2.Duck{}}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_Duck{Duck: &cadencev2.Duck{Active: true, Level: 0.2}}})
	eventually(t, "the duck", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(*b.ducks) == 1 && (*b.ducks)[0].GetLevel() == float32(0.2)
	})
}

type peerBoard struct {
	voiceBoard
	mu     *sync.Mutex
	up     *int
	down   *int
	frames *[]*cadencev2.Envelope
	info   **cadencev2.DeviceInfo
}

func (b peerBoard) LinkUp(_ func(*cadencev2.Envelope) error, info *cadencev2.DeviceInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.up++
	*b.info = info
}

func (b peerBoard) LinkDown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.down++
}

func (b peerBoard) LinkMessage(env *cadencev2.Envelope) bool {
	if env.GetAssistantTurn() == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.frames = append(*b.frames, env)
	return true
}

func TestLinkPeerTakesTheScreensFrames(t *testing.T) {
	vb, _, _ := newVoiceBoard(t, t.TempDir())
	var info *cadencev2.DeviceInfo
	b := peerBoard{voiceBoard: vb, mu: &sync.Mutex{}, up: new(int), down: new(int), frames: &[]*cadencev2.Envelope{}, info: &info}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_AssistantTurn{AssistantTurn: &cadencev2.AssistantTurn{TurnId: "t"}}})
	eventually(t, "the turn", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return *b.up == 1 && len(*b.frames) == 1
	})
	_ = p.c.CloseNow()
	eventually(t, "link down", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return *b.down == 1
	})
}
