package screen_test

import (
	"context"
	"encoding/json"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// a toast's typed action reaches the screen typed; an rpc one keeps the untyped fields an older screen calls with
func TestToastActionsAreTyped(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	display := r.caps[capschema.DisplayScreenID]
	if _, err := command(t, display, "toast",
		`{"text":"Hermes ждёт","kind":"warning","icon":"task","action":{"id":"open","label":"Открыть","kind":"open","route":"task","ref":"ab12"}}`); err != nil {
		t.Fatal(err)
	}
	toast := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetToast().GetAction() != nil }).GetToast()
	a := toast.GetAction()
	if a.GetKind() != screenv1.ToastActionKind_TOAST_ACTION_KIND_OPEN || a.GetRoute() != cadencev2.ScreenRoute_SCREEN_ROUTE_TASK ||
		a.GetRef() != "ab12" || a.GetLabel() != "Открыть" || toast.GetActionId() != "" {
		t.Fatalf("toast = %v", toast)
	}
	if _, err := command(t, display, "toast", `{"text":"x","action":{"id":"retry","label":"Again","kind":"rpc"}}`); err != nil {
		t.Fatal(err)
	}
	toast = frame(t, s, func(e *screenv1.Envelope) bool { return e.GetToast().GetAction().GetId() == "retry" }).GetToast()
	if toast.GetAction().GetKind() != screenv1.ToastActionKind_TOAST_ACTION_KIND_RPC || toast.GetActionId() != "retry" ||
		toast.GetActionLabel() != "Again" {
		t.Fatalf("toast = %v", toast)
	}
	if _, err := command(t, display, "toast", `{"text":"x","action":{"id":"open","label":"Open","kind":"open"}}`); err == nil {
		t.Fatal("an open action without its route shown")
	}
}

// the server's play carries the playable's skips and the transport it serves, which the session state then lists
func TestPlayCarriesSkipsAndTheServersTransport(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	go func() {
		_, _ = command(t, r.caps[capschema.MediaVideoID], "play",
			`{"url":"http://a/v","skips":[{"kind":"opening","startMs":1000,"endMs":90000}],"actions":["next","previous"]}`)
	}()
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if len(mp.GetSkips()) != 1 || mp.GetSkips()[0].GetKind() != screenv1.SkipKind_SKIP_KIND_OPENING ||
		mp.GetSkips()[0].GetEndMs() != 90000 || len(mp.GetActions()) != 2 || mp.GetActions()[1] != "previous" {
		t.Fatalf("play = %v", mp)
	}
}

// the server's fields of a session it owns and the tasks count reach the screen, and a screen that connects later
// gets the fields again
func TestServerSessionFieldsAndTasksReachTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	st := &cadencev2.MediaSessionState{
		SessionId: "video-3", Actions: []string{"next"},
		UpNext: []*cadencev2.MediaQueueItem{{Ref: "e:6", Title: "Next"}},
	}
	if !r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: st}}) {
		t.Fatal("session fields not taken")
	}
	got := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaSessionState() != nil }).GetMediaSessionState()
	if got.GetSessionId() != "video-3" || got.GetUpNext()[0].GetRef() != "e:6" {
		t.Fatalf("state = %v", got)
	}
	if !r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_TasksSummary{TasksSummary: &cadencev2.TasksSummary{Running: 2, Waiting: 1}}}) {
		t.Fatal("tasks not taken")
	}
	ts := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetTasksSummary() != nil }).GetTasksSummary()
	if ts.GetRunning() != 2 || ts.GetWaiting() != 1 {
		t.Fatalf("tasks = %v", ts)
	}
	s2 := r.connect(t, screentest.Options{})
	frame(t, s2, func(e *screenv1.Envelope) bool { return e.GetMediaSessionState().GetSessionId() == "video-3" })
}

// the link's origin is in the screen's link status, for its image fetches
func TestLinkStatusNamesTheServer(t *testing.T) {
	r := newRig(t, screen.Options{})
	r.srv.SetServerURL("https://cadence.example:8443")
	s := r.connect(t, screentest.Options{})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetLinkStatus().GetServerUrl() == "https://cadence.example:8443"
	})
	r.srv.LinkUp((&uplink{}).send, nil)
	frame(t, s, func(e *screenv1.Envelope) bool {
		ls := e.GetLinkStatus()
		return ls.GetState() == screenv1.LinkState_LINK_STATE_ONLINE && ls.GetServerUrl() == "https://cadence.example:8443"
	})
}

// the owner's microphone pick is display.screen's mic, and a speaker pick keeps the assistant key off the local mic
func TestMicChoiceIsTheStatesMicAndTheKeys(t *testing.T) {
	r := newRig(t, screen.Options{ForwardKeys: true})
	hello := screentest.DefaultHello()
	hello.LocalMic = true
	s := r.connect(t, screentest.Options{Hello: hello})
	display := r.caps[capschema.DisplayScreenID]
	state(t, display, `"mic":{"kind":"local"}`)
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MicChoice{MicChoice: &screenv1.MicChoice{
		Kind: cadencev2.MicSourceKind_MIC_SOURCE_KIND_SPEAKER, DeviceId: "spk-1",
	}}})
	state(t, display, `"mic":{"kind":"speaker","deviceId":"spk-1"}`)
	_ = s.Key(screenv1.Key_KEY_ASSISTANT)
	noEvents(t, r.srv)
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MicChoice{MicChoice: &screenv1.MicChoice{
		Kind: cadencev2.MicSourceKind_MIC_SOURCE_KIND_LOCAL,
	}}})
	state(t, display, `"mic":{"kind":"local"}`)
	_ = s.Key(screenv1.Key_KEY_ASSISTANT)
	if got := events(t, r.srv, 2); got[0].Kind != input.Action || !got[0].Pressed || got[1].Pressed {
		t.Fatalf("events = %v", got)
	}

	// none is the state's mic as picked
	s2 := r.connect(t, screentest.Options{})
	_ = s2.Send(&screenv1.Envelope{Body: &screenv1.Envelope_MicChoice{MicChoice: &screenv1.MicChoice{
		Kind: cadencev2.MicSourceKind_MIC_SOURCE_KIND_NONE,
	}}})
	state(t, display, `"mic":{"kind":"none"}`)
}

func longKey(s *screentest.Screen, k screenv1.Key, a screenv1.KeyAction) {
	_ = s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_InputEvent{InputEvent: &screenv1.InputEvent{
		Key: k, Action: a, Source: screenv1.InputSource_INPUT_SOURCE_REMOTE,
	}}})
}

// a long assistant key with no down of its own (a long BACK) is a press on the local mic; a held G's long is not a
// second one
func TestLongAssistantKeyIsAPressOnTheLocalMic(t *testing.T) {
	r := newRig(t, screen.Options{ForwardKeys: true})
	hello := screentest.DefaultHello()
	hello.LocalMic = true
	s := r.connect(t, screentest.Options{Hello: hello})
	longKey(s, screenv1.Key_KEY_ASSISTANT, screenv1.KeyAction_KEY_ACTION_LONG)
	if got := events(t, r.srv, 2); got[0].Kind != input.Action || !got[0].Pressed || got[1].Pressed {
		t.Fatalf("events = %v", got)
	}
	longKey(s, screenv1.Key_KEY_ASSISTANT, screenv1.KeyAction_KEY_ACTION_DOWN)
	if got := events(t, r.srv, 1); !got[0].Pressed {
		t.Fatalf("events = %v", got)
	}
	longKey(s, screenv1.Key_KEY_ASSISTANT, screenv1.KeyAction_KEY_ACTION_LONG)
	longKey(s, screenv1.Key_KEY_ASSISTANT, screenv1.KeyAction_KEY_ACTION_UP)
	if got := events(t, r.srv, 1); got[0].Pressed {
		t.Fatalf("events = %v", got)
	}
	noEvents(t, r.srv)
	// a long volume key moves nothing more
	longKey(s, screenv1.Key_KEY_VOLUME_UP, screenv1.KeyAction_KEY_ACTION_LONG)
	noEvents(t, r.srv)
}

// display.screen's language, as settings.set writes it, is the screen's Ready language
func TestDisplayLanguageIsReadys(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if err := r.caps[capschema.DisplayScreenID].Apply(context.Background(), 3, json.RawMessage(`{"language":"ru","overscan":5}`)); err != nil {
		t.Fatal(err)
	}
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetReady().GetLanguage() == "ru" })
}
