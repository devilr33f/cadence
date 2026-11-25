package screen_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

const wait = 2 * time.Second

type uplink struct {
	mu   sync.Mutex
	sent []*cadencev2.Envelope
}

func (u *uplink) send(env *cadencev2.Envelope) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sent = append(u.sent, env)
	return nil
}

func (u *uplink) got(t *testing.T, match func(*cadencev2.Envelope) bool) *cadencev2.Envelope {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		for _, env := range u.sent {
			if match(env) {
				u.mu.Unlock()
				return env
			}
		}
		u.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing sent up")
	return nil
}

type rig struct {
	srv   *screen.Server
	media *media.Media
	path  string
	caps  map[string]capability.Provider
}

func newRig(t *testing.T, o screen.Options) *rig {
	t.Helper()
	// short: a unix socket path is at most 104 bytes on macos
	dir, err := os.MkdirTemp("", "scr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	o.Path = filepath.Join(dir, "s")
	if o.Media == nil {
		o.Media = media.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	r := &rig{srv: screen.New(o), media: o.Media, path: o.Path, caps: map[string]capability.Provider{}}
	for _, p := range r.srv.Capabilities() {
		r.caps[p.Descriptor().ID] = p
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.srv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r
}

func (r *rig) connect(t *testing.T, o screentest.Options) *screentest.Screen {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	s, err := screentest.Connect(ctx, r.path, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Wait(wait, func(e *screenv1.Envelope) bool { return e.GetReady() != nil }); err != nil {
		t.Fatal("no ready")
	}
	return s
}

func frame(t *testing.T, s *screentest.Screen, match func(*screenv1.Envelope) bool) *screenv1.Envelope {
	t.Helper()
	env, err := s.Wait(wait, match)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func state(t *testing.T, p capability.Provider, want string) {
	t.Helper()
	deadline := time.After(wait)
	var last json.RawMessage
	for {
		select {
		case v := <-p.State():
			last = v
			if strings.Contains(string(v), want) {
				return
			}
		case <-deadline:
			t.Fatalf("%s state %s, want %s", p.Descriptor().ID, last, want)
		}
	}
}

func command(t *testing.T, p capability.Provider, name, args string) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return p.Command(ctx, name, json.RawMessage(args))
}

func TestReadyAndDisplayStateFollowTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{DaemonVersion: "v-test", TapPath: "@tap"})
	display := r.caps[capschema.DisplayScreenID]
	state(t, display, `"screen":false`)
	s := r.connect(t, screentest.Options{})
	ready := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetReady() != nil }).GetReady()
	if ready.GetDaemonVersion() != "v-test" || ready.GetTapPath() != "@tap" || ready.GetGateOpen() {
		t.Fatalf("ready = %v", ready)
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetPlaybackGate() != nil && !e.GetPlaybackGate().GetOpen() })
	state(t, display, `"maxHeight":1080,"codecs":["h264","hevc"],"cardVersions":[1],"cardFeatures":["card","text","toggle"]`)
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_ScreenStatus{ScreenStatus: &screenv1.ScreenStatus{
		Power: screenv1.ScreenPower_SCREEN_POWER_AMBIENT, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER, DrawerOpen: true,
	}}})
	state(t, display, `"power":"ambient","route":"player","drawerOpen":true`)
	s.Close()
	state(t, display, `"screen":false`)
}

// a newer screen replaces the one before
func TestNewerScreenReplacesTheOld(t *testing.T) {
	r := newRig(t, screen.Options{})
	first := r.connect(t, screentest.Options{})
	r.connect(t, screentest.Options{})
	select {
	case <-first.Done():
	case <-time.After(wait):
		t.Fatal("the old screen stays")
	}
}

func TestRPCPassesThroughAndIsRefusedOffline(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	req := &cadencev2.RpcRequest{Id: "1", Method: "catalog.search", Json: `{"q":"x"}`}
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_RpcRequest{RpcRequest: req}})
	resp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetRpcResponse().GetId() == "1" }).GetRpcResponse()
	if resp.GetStatus() != "unavailable" {
		t.Fatalf("offline = %v", resp)
	}

	up := &uplink{}
	r.srv.LinkUp(up.send, &cadencev2.DeviceInfo{Name: "TV", RoomId: "r1", RoomName: "Гостиная"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetReady().GetRoomId() == "r1" })
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetLinkStatus().GetState() == screenv1.LinkState_LINK_STATE_ONLINE
	})
	req.Id = "2"
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_RpcRequest{RpcRequest: req}})
	up.got(t, func(e *cadencev2.Envelope) bool { return e.GetRpcRequest().GetId() == "2" })
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_RpcCancel{RpcCancel: &cadencev2.RpcCancel{Id: "2"}}})
	up.got(t, func(e *cadencev2.Envelope) bool { return e.GetRpcCancel().GetId() == "2" })
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_ScreenContext{ScreenContext: &cadencev2.ScreenContext{
		Revision: 7, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE, PageRef: "t:1",
	}}})
	up.got(t, func(e *cadencev2.Envelope) bool { return e.GetScreenContext().GetRevision() == 7 })

	// the server's side reaches the screen
	for _, env := range []*cadencev2.Envelope{
		{Body: &cadencev2.Envelope_RpcResponse{RpcResponse: &cadencev2.RpcResponse{Id: "2", Status: "ok"}}},
		{Body: &cadencev2.Envelope_RpcEvent{RpcEvent: &cadencev2.RpcEvent{Id: "2", Seq: 1}}},
		{Body: &cadencev2.Envelope_AssistantTurn{AssistantTurn: &cadencev2.AssistantTurn{TurnId: "turn"}}},
		{Body: &cadencev2.Envelope_VoiceLevel{VoiceLevel: &cadencev2.VoiceLevel{TurnId: "turn", Mic: 9}}},
		{Body: &cadencev2.Envelope_Cards{Cards: &cadencev2.Cards{ContextId: "turn"}}},
	} {
		if !r.srv.LinkMessage(env) {
			t.Fatalf("not taken: %v", env)
		}
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetRpcResponse().GetId() == "2" })
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetRpcEvent().GetSeq() == 1 })
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetAssistantTurn().GetTurnId() == "turn" })
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetVoiceLevel().GetMic() == 9 })
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetCards().GetContextId() == "turn" })
	if r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_MicOpen{MicOpen: &cadencev2.MicOpen{}}}) {
		t.Fatal("a speaker's frame taken")
	}
	r.srv.LinkDown()
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetLinkStatus().GetState() == screenv1.LinkState_LINK_STATE_OFFLINE
	})
}

// what the server knows of a play reaches the screen whole: the title, the channel, the artist and the provider it
// names, so the osd shows the video's name and its line under it
func TestPlayCarriesWhatTheScreenShows(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if _, err := command(t, r.caps[capschema.MediaVideoID], "play",
		`{"url":"https://example.test/v.m3u8","metadata":{"title":"Как устроен звук","channel":"Лекторий","artist":"A",`+
			`"provider":"youtube","providerName":"YouTube","artworkUrl":"https://cadence.test/v2/images/abc","itemRef":"c:1"}}`); err != nil {
		t.Fatal(err)
	}
	m := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay().GetMetadata()
	if m.GetTitle() != "Как устроен звук" || m.GetChannel().GetName() != "Лекторий" || m.GetArtist() != "A" ||
		m.GetProvider().GetId() != "youtube" || m.GetProvider().GetName() != "YouTube" ||
		m.GetArtworkUrl() != "https://cadence.test/v2/images/abc" || m.GetItemRef() != "c:1" ||
		m.GetKind() != cadencev2.MediaKind_MEDIA_KIND_VIDEO {
		t.Fatalf("metadata = %v", m)
	}
}

// rule 7: the player starts paused until the gate is open
func TestPlayBecomesAnExternalSessionPausedWhileGated(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	out, err := command(t, r.caps[capschema.MediaVideoID], "play",
		`{"url":"https://example.test/v.m3u8","metadata":{"title":"T","series":"S","episode":"3"},`+
			`"renditions":[{"name":"1080p","height":1080,"fps":30,"codecs":["vp9"]},{"name":"720p60","height":720,"fps":60,"selected":true}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var played struct{ SessionID string }
	_ = json.Unmarshal(out, &played)
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if mp.GetSessionId() != played.SessionID || mp.GetUrl() != "https://example.test/v.m3u8" ||
		mp.GetMetadata().GetKind() != cadencev2.MediaKind_MEDIA_KIND_EPISODE {
		t.Fatalf("play = %v", mp)
	}
	if rs := mp.GetRenditions(); len(rs) != 2 || rs[0].GetName() != "1080p" || rs[0].GetCodecs()[0] != "vp9" ||
		rs[0].GetSelected() || rs[1].GetFps() != 60 || !rs[1].GetSelected() {
		t.Fatalf("renditions = %v", rs)
	}
	if _, st := s.State(); st != cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED {
		t.Fatalf("player state = %v", st)
	}
	// the screen's session is the device's, with what it was played with
	var got *cadencev2.MediaSessionState
	deadline := time.After(wait)
	for got.GetSessionId() != played.SessionID {
		select {
		case got = <-r.media.Sessions():
		case <-deadline:
			t.Fatal("no session")
		}
	}
	if got.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_VIDEO || got.GetMetadata().GetTitle() != "T" ||
		got.GetAudioRoute() != cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT {
		t.Fatalf("session = %v", got)
	}
	state(t, r.caps[capschema.MediaVideoID], `"tracks":[{"id":"a1","kind":"audio","selected":true}]`)
	tracks, err := command(t, r.caps[capschema.MediaVideoID], "tracks", `{}`)
	if err != nil || !strings.Contains(string(tracks), `"a1"`) {
		t.Fatalf("tracks = %s, %v", tracks, err)
	}
	if _, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"file:///etc/passwd"}`); err == nil {
		t.Fatal("a non-web url played")
	}
	// media.player's transport reaches the screen's player
	if err := r.srv.ExternalCommand(context.Background(), played.SessionID, "stop", 0, ""); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.State(); id != "" {
		t.Fatal("not stopped")
	}
}

// a play that replaces the screen's video autoplays: the replaced session ends with the media plane, so audiod's
// pause for it (displaced, as the new one takes the speaker) neither is asked nor reaches the player — which would
// pause the new session, the command naming no session of its own. a pause for the session that plays still does
func TestAPlayThatReplacesTheVideoAutoplays(t *testing.T) {
	r := newRig(t, screen.Options{})
	a := &fakeAudiod{sent: make(chan *audiodv1.Envelope, 64)}
	r.media.MediaReady(a.send)
	s := r.connect(t, screentest.Options{})
	// the gate opens: the level held, so the screen plays what it is given
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_VolumeApplied{VolumeApplied: &audiodv1.VolumeApplied{Ok: true}}})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetPlaybackGate().GetOpen() })

	play := func() string {
		t.Helper()
		out, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://example.test/v.m3u8"}`)
		if err != nil {
			t.Fatal(err)
		}
		var played struct{ SessionID string }
		_ = json.Unmarshal(out, &played)
		return played.SessionID
	}
	playing := func(session string) {
		t.Helper()
		deadline := time.After(wait)
		for {
			if id, st := s.State(); id == session && st == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
				return
			}
			select {
			case <-deadline:
				id, st := s.State()
				t.Fatalf("player = %s %v, want %s playing", id, st, session)
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	first := play()
	playing(first)
	a.next(t, func(e *audiodv1.Envelope) bool { return e.GetExternalSource().GetId() == first })

	second := play()
	if second == first {
		t.Fatal("the same session")
	}
	playing(second)
	// the replaced session ended with the media plane
	if e := a.next(t, func(e *audiodv1.Envelope) bool {
		return e.GetExternalSource().GetId() == first && e.GetExternalSource().GetOp() == audiodv1.ExternalOp_EXTERNAL_OP_UNREGISTER
	}); e == nil {
		t.Fatal("the replaced session stayed")
	}
	// audiod's pause for the replaced session (in flight, or an arbitration that never saw the end) pauses nothing
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_ExternalAction{ExternalAction: &audiodv1.ExternalAction{
		Id: first, Action: audiodv1.MediaAction_MEDIA_ACTION_PAUSE, Reason: "displaced",
	}}})
	time.Sleep(200 * time.Millisecond)
	if _, st := s.State(); st != cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
		t.Fatal("the new video was paused")
	}
	for _, f := range s.Frames() {
		if mc := f.GetMediaCommand(); mc != nil && mc.GetAction() == screenv1.MediaAction_MEDIA_ACTION_PAUSE {
			t.Fatal("the player was sent a pause")
		}
	}
	// the session that plays is still commanded: media.player's pause reaches it
	if err := r.srv.ExternalCommand(context.Background(), second, "pause", 0, ""); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetMediaCommand() != nil && e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_PAUSE
	})
	deadline := time.After(wait)
	for {
		if _, st := s.State(); st == cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the playing session was not paused")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// a session's last state ends it, whatever state the screen put in it
func TestAnEndedStateEndsTheSession(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	out, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://example.test/v.m3u8"}`)
	if err != nil {
		t.Fatal(err)
	}
	var played struct{ SessionID string }
	_ = json.Unmarshal(out, &played)
	deadline := time.After(wait)
	for {
		select {
		case got := <-r.media.Sessions():
			if got.GetSessionId() != played.SessionID {
				continue
			}
		case <-deadline:
			t.Fatal("no session")
		}
		break
	}
	s.End(played.SessionID, cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING)
	for {
		select {
		case got := <-r.media.Sessions():
			if got.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
				return
			}
		case <-deadline:
			t.Fatal("an ended session stayed")
		}
	}
}

func TestCommandsUnavailableWithoutAScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	for id, cmd := range map[string][2]string{
		capschema.MediaVideoID:    {"play", `{"url":"https://a.test/x"}`},
		capschema.DisplayScreenID: {"wake", `{}`},
		capschema.InputRemoteID:   {"key", `{"key":"ok"}`},
	} {
		_, err := command(t, r.caps[id], cmd[0], cmd[1])
		var ce *capability.CommandError
		if !errors.As(err, &ce) || ce.Code != "unavailable" {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestDisplayAndRemoteCommandsReachTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if _, err := command(t, r.caps[capschema.DisplayScreenID], "show", `{"route":"title","ref":"t:1"}`); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetRoute().GetRef() == "t:1" })
	if _, err := command(t, r.caps[capschema.DisplayScreenID], "toast", `{"text":"hi","kind":"success"}`); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetToast().GetKind() == screenv1.ToastKind_TOAST_KIND_SUCCESS
	})
	if _, err := command(t, r.caps[capschema.DisplayScreenID], "sleep", `{}`); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetDisplayPower() != nil && !e.GetDisplayPower().GetOn() })
	if _, err := command(t, r.caps[capschema.DisplayScreenID], "toast", `{"text":""}`); err == nil {
		t.Fatal("an empty toast shown")
	}
	if _, err := command(t, r.caps[capschema.InputRemoteID], "key", `{"key":"volume_up"}`); err != nil {
		t.Fatal(err)
	}
	ev := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetInputEvent() != nil }).GetInputEvent()
	if ev.GetKey() != screenv1.Key_KEY_VOLUME_UP || ev.GetSource() != screenv1.InputSource_INPUT_SOURCE_API {
		t.Fatalf("key = %v", ev)
	}
	state(t, r.caps[capschema.InputRemoteID], `"lastSource":"api"`)
	if _, err := command(t, r.caps[capschema.InputRemoteID], "text", `{"value":"фрирен"}`); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetTextInput().GetText() == "фрирен" })
}

func TestConfigsCheckedKeptAndSentToEveryScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	display := r.caps[capschema.DisplayScreenID]
	if err := display.Apply(context.Background(), 1, json.RawMessage(`{"theme":"other"}`)); err == nil {
		t.Fatal("an invalid config applied")
	}
	if err := display.Apply(context.Background(), 2, json.RawMessage(`{"overscan":2.5}`)); err != nil {
		t.Fatal(err)
	}
	s := r.connect(t, screentest.Options{})
	cc := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetCapabilityConfig() != nil }).GetCapabilityConfig()
	if cc.GetCapability() != capschema.DisplayScreenID || cc.GetJson() != `{"overscan":2.5}` {
		t.Fatalf("config = %v", cc)
	}
}

func events(t *testing.T, srv *screen.Server, n int) []input.ButtonEvent {
	t.Helper()
	var out []input.ButtonEvent
	for len(out) < n {
		select {
		case e := <-srv.Events():
			out = append(out, e)
		case <-time.After(wait):
			t.Fatalf("%d events, want %d", len(out), n)
		}
	}
	return out
}

func noEvents(t *testing.T, srv *screen.Server) {
	t.Helper()
	select {
	case e := <-srv.Events():
		t.Fatalf("event %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestVolumeAndAssistantKeysAreTheBoardsPresses(t *testing.T) {
	r := newRig(t, screen.Options{ForwardKeys: true})
	s := r.connect(t, screentest.Options{})
	_ = s.Key(screenv1.Key_KEY_VOLUME_DOWN)
	got := events(t, r.srv, 2)
	if got[0].Kind != input.VolumeDown || !got[0].Pressed || got[1].Pressed {
		t.Fatalf("events = %v", got)
	}
	// no microphone of its own: the screen asks the server, nothing begins here
	_ = s.Key(screenv1.Key_KEY_ASSISTANT)
	noEvents(t, r.srv)
	// a key the api injected never moves the level (rule 7)
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_InputEvent{InputEvent: &screenv1.InputEvent{
		Key: screenv1.Key_KEY_VOLUME_UP, Action: screenv1.KeyAction_KEY_ACTION_PRESS,
		Source: screenv1.InputSource_INPUT_SOURCE_API,
	}}})
	noEvents(t, r.srv)

	hello := screentest.DefaultHello()
	hello.LocalMic = true
	s2 := r.connect(t, screentest.Options{Hello: hello})
	_ = s2.Key(screenv1.Key_KEY_ASSISTANT)
	if got := events(t, r.srv, 2); got[0].Kind != input.Action {
		t.Fatalf("events = %v", got)
	}
}

func TestKeysLeftToABoardThatReadsItsRemotes(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	_ = s.Key(screenv1.Key_KEY_VOLUME_UP)
	noEvents(t, r.srv)
	state(t, r.caps[capschema.InputRemoteID], `"lastSource":"remote"`)
}

func TestDuckReachesTheMediaAndTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if !r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_Duck{Duck: &cadencev2.Duck{
		Active: true, Level: 0.2, TurnId: "turn",
	}}}) {
		t.Fatal("duck not taken")
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetDuck().GetTurnId() == "turn" })
}

// a direct player applies its route's controls itself; a level never exceeds 100 and a duck never raises one
func TestRouteControlsReachThePlayer(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	r.srv.RouteControl(&audiodv1.RouteControl{SessionId: "v", Control: &audiodv1.RouteControl_SetLevel{
		SetLevel: &audiodv1.SetLevel{Level: 400, RampMs: 50},
	}})
	if l := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetSetLevel() != nil }).GetSetLevel(); l.GetLevel() != 100 {
		t.Fatalf("level = %v", l)
	}
	r.srv.RouteControl(&audiodv1.RouteControl{SessionId: "v", Control: &audiodv1.RouteControl_Duck{
		Duck: &audiodv1.Duck{Active: true, Level: 2},
	}})
	if d := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetDuck() != nil }).GetDuck(); d.GetLevel() != 1 {
		t.Fatalf("duck = %v", d)
	}
}

func TestMirroringShownAndInTheState(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	sc := r.caps[capschema.MediaScreencastID]
	if err := sc.Apply(context.Background(), 1, json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	state(t, sc, `"state":"starting"`)
	r.srv.MirrorState(&audiodv1.MirrorState{SessionId: "m1", Active: true, ClientName: "iPhone", Width: 1280})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMirrorStart().GetSessionId() == "m1" })
	state(t, sc, `"mirroring":true,"clientName":"iPhone"`)
	r.srv.MirrorState(&audiodv1.MirrorState{SessionId: "m1", Reason: "sender"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMirrorStop().GetReason() == "sender" })
	r.srv.AirplayPin(&audiodv1.AirplayPin{Active: true, Pin: "1234"})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetAirplayPin().GetPin() == "1234" })
}

func TestComponentsDecideTheCapabilities(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "MANIFEST")
	if err := os.WriteFile(manifest, []byte("# cadence v1\n# components cadenced cadence-screen\nabc  bin/cadenced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := screen.ReadComponents(manifest)
	if !c.Has(screen.ComponentScreen) || c.Has(screen.ComponentAirplay) {
		t.Fatalf("components = %v", c)
	}
	ids := map[string]bool{}
	for _, p := range screen.New(screen.Options{Components: c}).Capabilities() {
		ids[p.Descriptor().ID] = true
	}
	if !ids[capschema.DisplayScreenID] || !ids[capschema.MediaVideoID] || !ids[capschema.InputRemoteID] ||
		ids[capschema.MediaScreencastID] {
		t.Fatalf("capabilities = %v", ids)
	}
	// no manifest, or none listed: everything (a sim, a development build)
	if !screen.ReadComponents(filepath.Join(dir, "none")).Has(screen.ComponentAirplay) {
		t.Fatal("a missing manifest ships less")
	}
	if len(screen.New(screen.Options{Components: screen.ComponentsOf("cadenced")}).Capabilities()) != 0 {
		t.Fatal("capabilities without the screen")
	}
}

// the player's buffer reaches the session the server sees, and its figures the board's metrics
func TestBufferAndPlayerStatsReachTheDevice(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	pct := uint32(37)
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MediaState{MediaState: &screenv1.MediaState{
		SessionId: "v1", State: cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING, PositionAtMs: time.Now().UnixMilli(),
		Purpose: screenv1.MediaPurpose_MEDIA_PURPOSE_CONTENT, BufferedMs: 4200, BufferingPercent: &pct,
		AudioRoute: cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT,
	}}})
	var got *cadencev2.MediaSessionState
	deadline := time.After(wait)
	for got.GetSessionId() != "v1" {
		select {
		case got = <-r.media.Sessions():
		case <-deadline:
			t.Fatal("no session")
		}
	}
	if got.GetBufferedMs() != 4200 || got.BufferingPercent == nil || got.GetBufferingPercent() != 37 {
		t.Fatalf("session = %v", got)
	}
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_PlayerStats{PlayerStats: &screenv1.PlayerStats{
		SessionId: "v1", BufferedMs: 4200, FrameDrops: 3, Hwdec: "mediacodec",
	}}})
	for end := time.Now().Add(wait); ; {
		var buffered, drops bool
		for _, p := range r.srv.PlayerPoints() {
			buffered = buffered || p.Name == "cadence.player.buffered" && p.Value == 4.2
			drops = drops || p.Name == "cadence.player.frame_drops" && p.Count == 3
		}
		if buffered && drops {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("player points = %v", r.srv.PlayerPoints())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a screen gone from its socket ends its leases on live cards with the server; it acquires them again once back
func TestScreenDropReleasesItsLeases(t *testing.T) {
	r := newRig(t, screen.Options{})
	up := &uplink{}
	r.srv.LinkUp(up.send, &cadencev2.DeviceInfo{Name: "TV"})
	s := r.connect(t, screentest.Options{})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetLinkStatus().GetState() == screenv1.LinkState_LINK_STATE_ONLINE
	})
	s.Close()
	up.got(t, func(e *cadencev2.Envelope) bool {
		return e.GetRpcRequest().GetMethod() == rpc.CardsRelease && e.GetRpcRequest().GetJson() == `{"all":true}`
	})
}
