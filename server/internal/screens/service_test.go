package screens_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

type dir struct{ rooms map[string]string }

func (d dir) Room(_ context.Context, id string) string { return d.rooms[id] }

func (d dir) Devices(context.Context) []string {
	var ids []string
	for id := range d.rooms {
		ids = append(ids, id)
	}
	return ids
}

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

// rig is a living room with a speaker, a fake screen and a playing
// soundbar, and a kitchen screen
type rig struct {
	t    *testing.T
	svc  *screens.Service
	bus  *events.Bus
	logs *syncBuf

	mu      sync.Mutex
	sent    map[string][]*cadencev2.Envelope
	playing map[string]bool
	states  map[string]string
	// the voice sessions running per device, and the stops the service asked for
	live  map[string]string
	stops []string
}

// stubCalls answers a call's cards from fn, its line from the real titles
type stubCalls func(c cards.Call) []cards.Subject

func (f stubCalls) Title(m cards.Messages, lang string, c cards.Call) string {
	return (&cards.ToolCards{}).Title(m, lang, c)
}

func (f stubCalls) CallSubjects(c cards.Call) ([]cards.Subject, string) { return f(c), "" }

func newRig(t *testing.T, edit ...func(*screens.Deps)) *rig {
	r := &rig{t: t, bus: events.NewBus(0), logs: &syncBuf{}, sent: map[string][]*cadencev2.Envelope{}, live: map[string]string{},
		playing: map[string]bool{"soundbar": true, "kitchen-tv": true},
		states: map[string]string{
			"tv":         `{"power":"on","screen":true,"mic":{"kind":"speaker","deviceId":"spk"}}`,
			"kitchen-tv": `{"power":"on","screen":true,"mic":{"kind":"none"}}`,
			"bare-tv":    `{"power":"on","screen":true}`,
		}}
	log := slog.New(slog.NewTextHandler(r.logs, nil))
	cs := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }, Log: log})
	d := screens.Deps{
		Directory: dir{rooms: map[string]string{"spk": "living", "tv": "living", "soundbar": "living", "kitchen-tv": "kitchen", "bare-tv": "attic"}},
		State: func(_ context.Context, id, capability string) (json.RawMessage, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			s, ok := r.states[id]
			return json.RawMessage(s), ok && capability == "display.screen"
		},
		Online: func(id string) bool { return id != "offline" },
		Send: func(id string, env *cadencev2.Envelope) error {
			r.mu.Lock()
			r.sent[id] = append(r.sent[id], env)
			r.mu.Unlock()
			return nil
		},
		Playing: func(id string) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.playing[id] },
		Lang:    func(context.Context, string) string { return "ru" },
		Name: func(_ context.Context, id string) string {
			return map[string]string{"spk": "Кухня"}[id]
		},
		Cards: cs, Publish: r.bus.Publish,
		Calls: stubCalls(func(c cards.Call) []cards.Subject {
			// the agent's envelope is unwrapped: the cards read the tool's own output and the call's arguments
			res := c.Result
			if res == nil || res.Tool != "ha_control" || !res.OK || string(res.Data) != `{"done":"turn_off"}` ||
				string(res.Args) != `{"name":"свет"}` || res.Key != "t1:c1" {
				return nil
			}
			return []cards.Subject{cards.Posts("ha-standin", []providers.Post{{Author: "дом", Text: "свет выключен"}})}
		}),
		StopSession: func(dev, id string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			if id == "" && r.live[dev] != "" {
				id = r.live[dev]
			}
			if r.live[dev] != id || id == "" {
				return false
			}
			delete(r.live, dev)
			r.stops = append(r.stops, dev+"/"+id)
			return true
		},
		Log: log,
	}
	for _, e := range edit {
		e(&d)
	}
	r.svc = screens.New(d)
	ctx, cancel := context.WithCancel(context.Background())
	stop := r.svc.Run(ctx, r.bus)
	t.Cleanup(func() { cancel(); stop() })
	return r
}

func (r *rig) of(id string) []*cadencev2.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*cadencev2.Envelope(nil), r.sent[id]...)
}

func (r *rig) turns(id string) []*cadencev2.AssistantTurn {
	var out []*cadencev2.AssistantTurn
	for _, e := range r.of(id) {
		if at := e.GetAssistantTurn(); at != nil {
			out = append(out, at)
		}
	}
	return out
}

func (r *rig) ducks(id string) []*cadencev2.Duck {
	var out []*cadencev2.Duck
	for _, e := range r.of(id) {
		if d := e.GetDuck(); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// await waits for the screen's turn state to satisfy ok
func (r *rig) await(id, what string, ok func(*cadencev2.AssistantTurn) bool) *cadencev2.AssistantTurn {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ts := r.turns(id); len(ts) > 0 && ok(ts[len(ts)-1]) {
			return ts[len(ts)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("%s: %s never came; got %v", id, what, r.turns(id))
	return nil
}

func (r *rig) publish(typ string, data map[string]any) {
	r.bus.Publish(events.Event{Type: typ, DeviceID: "spk", SessionID: "s1", TurnID: "t1", At: time.Now(), Data: data})
}

func TestSpeakerTurnReachesTheRoomScreen(t *testing.T) {
	r := newRig(t)
	start := &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	}}}
	if r.svc.Inbound("spk", start) {
		t.Fatal("a session start must reach the voice service too")
	}
	at := r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_LISTENING
	})
	if at.GetTurnId() != "s1" || at.GetOriginDeviceId() != "spk" || at.GetTrigger() != cadencev2.Trigger_TRIGGER_WAKE ||
		at.GetMicSource().GetKind() != cadencev2.MicSourceKind_MIC_SOURCE_KIND_SPEAKER || at.GetMicSource().GetDeviceId() != "spk" ||
		at.GetMicSource().GetDeviceName() != "Кухня" {
		t.Fatalf("first state %v", at)
	}
	ducks := r.ducks("soundbar")
	if len(ducks) != 1 || !ducks[0].GetActive() || ducks[0].GetLevel() != screens.DuckLevel ||
		ducks[0].GetReason() != cadencev2.DuckReason_DUCK_REASON_ASSISTANT || ducks[0].GetTurnId() != "s1" {
		t.Fatalf("duck %v", ducks)
	}

	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionEvent{SessionEvent: &cadencev2.SessionEvent{
		SessionId: "s1", Kind: cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING,
	}}})
	r.publish("transcript", map[string]any{"text": "выключи свет и найди новости"})
	r.publish("tool_call", map[string]any{"step": 1, "id": "c1", "name": "ha_control", "args": json.RawMessage(`{"name":"свет"}`)})
	r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "web_search", "args": json.RawMessage(`{}`)})
	r.publish("tool_result", map[string]any{"step": 1, "id": "c1", "name": "ha_control", "ok": true, "code": "",
		"result": json.RawMessage(`{"ok":true,"data":{"done":"turn_off"}}`), "ms": 5})
	r.svc.Attach("spk", "s1", "t1:c2", cards.WebResults("t1:c2", []providers.SearchResult{
		{Title: "Новость", Text: "текст", URL: "https://news.example/1", Site: "news.example"},
	}, nil))
	r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "web_search", "ok": false, "code": "search_failed"})
	r.publish("llm_text", map[string]any{"step": 2, "text": "Готово.", "spoken": true})
	at = r.await("tv", "the steps, cards and reply", func(at *cadencev2.AssistantTurn) bool {
		return at.GetReply().GetText() == "Готово."
	})
	if at.GetState() != cadencev2.AssistantState_ASSISTANT_STATE_THINKING || at.GetTranscript().GetStable() != "выключи свет и найди новости" {
		t.Fatalf("state %v", at)
	}
	st := at.GetSteps()
	if len(st) != 2 || st[0].GetLabel() != "Управляю домом" || st[0].GetStatus() != cadencev2.StepStatus_STEP_STATUS_DONE ||
		st[1].GetLabel() != "Ищу в интернете · не получилось" || st[1].GetStatus() != cadencev2.StepStatus_STEP_STATUS_FAILED ||
		st[1].GetCardId() != cards.CallCardID("t1:c2") {
		t.Fatalf("steps %v", st)
	}
	// the two cards come by two ways (the bus, the tool's attachment) in either order; the search's results stand for
	// its call's card under the call's id
	cite := uint32(0)
	for _, c := range at.GetCards() {
		if l := c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[0].GetLink(); l != nil {
			cite = l.GetCite()
		}
	}
	if len(at.GetCards()) != 2 || cite != 1 {
		t.Fatalf("cards %v", at.GetCards())
	}
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_StreamStart{StreamStart: &cadencev2.StreamStart{
		StreamId: "st1", SessionId: "s1", Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY,
	}}})
	r.await("tv", "speaking", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_SPEAKING
	})
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
		SessionId: "s1", FollowUpAllowed: true, FollowUpWindowMs: 5000,
	}}})
	at = r.await("tv", "the follow-up", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_FOLLOW_UP
	})
	if at.GetFollowUpWindowMs() != 5000 || at.GetErrorCode() != "" {
		t.Fatalf("follow-up %v", at)
	}
	ts := r.turns("tv")
	for i := 1; i < len(ts); i++ {
		if ts[i].GetRevision() <= ts[i-1].GetRevision() {
			t.Fatalf("revisions must rise: %d after %d", ts[i].GetRevision(), ts[i-1].GetRevision())
		}
	}
	waitFor(t, "the duck lifted", func() bool { d := r.ducks("soundbar"); return len(d) == 2 && !d[1].GetActive() })
	if len(r.of("kitchen-tv")) != 0 || len(r.ducks("spk")) != 0 {
		t.Fatalf("another room heard of the turn: %v / the speaker ducked itself: %v", r.of("kitchen-tv"), r.ducks("spk"))
	}
	for _, leak := range []string{"выключи", "Готово", "Новость", "news.example"} {
		if strings.Contains(r.logs.String(), leak) {
			t.Fatalf("a log line carries %q:\n%s", leak, r.logs)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestATurnWithNoScreenSendsNothing(t *testing.T) {
	r := newRig(t)
	r.mu.Lock()
	delete(r.states, "tv")
	r.mu.Unlock()
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{SessionId: "s1"}}})
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: "s1", Code: "llm_failed"}}})
	time.Sleep(50 * time.Millisecond)
	if len(r.of("tv")) != 0 || len(r.of("soundbar")) != 0 {
		t.Fatalf("sent %v %v", r.of("tv"), r.of("soundbar"))
	}
}

func TestAFailedTurnShowsItsToken(t *testing.T) {
	r := newRig(t)
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{SessionId: "s1"}}})
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{SessionId: "s1", Code: "stt_failed"}}})
	at := r.await("tv", "resting", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_RESTING
	})
	if at.GetErrorCode() != "not_heard" {
		t.Fatalf("error %q", at.GetErrorCode())
	}
}

func TestAFailedTurnForwardsItsDetail(t *testing.T) {
	r := newRig(t)
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{SessionId: "s1"}}})
	failure := &cadencev2.TurnFailure{Code: "timeout", Stage: "llm", Step: 1, Provider: "gateway-main",
		Model: "vendor/model-x", WaitedMs: 10001, LimitMs: 10000}
	r.svc.Outbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionClose{SessionClose: &cadencev2.SessionClose{
		SessionId: "s1", Code: "timeout", Failure: failure,
	}}})
	at := r.await("tv", "resting", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_RESTING
	})
	if at.GetErrorCode() != "timeout" || at.GetFailure().GetStage() != "llm" || at.GetFailure().GetProvider() != "gateway-main" ||
		at.GetFailure().GetLimitMs() != 10000 {
		t.Fatalf("assistant turn %+v", at)
	}
}

func invoke(r *rig, dev string) calls.Reply {
	return r.svc.InvokeMethod()(context.Background(), calls.Call{DeviceID: dev, ID: "i1", Method: rpc.AssistantInvoke, Args: json.RawMessage(`{}`)})
}

func TestScreenPressOpensTheRoomSpeaker(t *testing.T) {
	r := newRig(t)
	rep := invoke(r, "tv")
	ans, _ := rep.JSON.(screens.InvokeAnswer)
	if rep.Status != rpc.StatusOK || ans.MicDeviceID != "spk" || ans.TurnID == "" {
		t.Fatalf("invoke %+v", rep)
	}
	var open *cadencev2.MicOpen
	for _, e := range r.of("spk") {
		if m := e.GetMicOpen(); m != nil {
			open = m
		}
	}
	if open == nil || open.GetSessionId() != ans.TurnID || open.GetOriginDeviceId() != "tv" {
		t.Fatalf("mic open %v", open)
	}
	if at := r.turns("tv"); len(at) != 1 || at[0].GetState() != cadencev2.AssistantState_ASSISTANT_STATE_INVOKED ||
		at[0].GetMicSource().GetDeviceName() != "Кухня" {
		t.Fatalf("invoked %v", at)
	}
	r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
		SessionId: open.GetSessionId(), Trigger: cadencev2.Trigger_TRIGGER_BUTTON,
	}}})
	at := r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool {
		return at.GetState() == cadencev2.AssistantState_ASSISTANT_STATE_LISTENING
	})
	if at.GetOriginDeviceId() != "tv" || at.GetTrigger() != cadencev2.Trigger_TRIGGER_BUTTON {
		t.Fatalf("pressed turn %v", at)
	}

	// no microphone in the room, a screen of no room, and a device that is no screen
	rep = invoke(r, "kitchen-tv")
	if a, _ := rep.JSON.(screens.InvokeAnswer); rep.Status != rpc.StatusUnavailable || a.Code != "no_microphone" || a.Toast == "" {
		t.Fatalf("kitchen %+v", rep)
	}
	// the owner's none pick (design §21): no microphone serves the screen though its room has one
	r.mu.Lock()
	ov := r.states["tv"]
	r.states["tv"] = `{"power":"on","screen":true,"mic":{"kind":"none"}}`
	r.mu.Unlock()
	if a, _ := invoke(r, "tv").JSON.(screens.InvokeAnswer); a.Code != "no_microphone" {
		t.Fatalf("a none pick opened a speaker: %+v", a)
	}
	r.mu.Lock()
	r.states["tv"] = ov
	r.mu.Unlock()
	if rep = invoke(r, "bare-tv"); rep.Status != rpc.StatusUnavailable {
		t.Fatalf("bare %+v", rep)
	}
	if rep = invoke(r, "spk"); rep.Status != rpc.StatusInvalid {
		t.Fatalf("speaker %+v", rep)
	}
	r.mu.Lock()
	r.states["tv"] = `{"power":"on","screen":true,"mic":{"kind":"speaker","deviceId":"offline"}}`
	r.mu.Unlock()
	if a, _ := invoke(r, "tv").JSON.(screens.InvokeAnswer); a.Code != "mic_offline" {
		t.Fatalf("offline mic %+v", a)
	}
}

func TestScreenContextStore(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := screens.NewContexts(clock)
	refs := make([]string, 30)
	for i := range refs {
		refs[i] = "r"
	}
	if !s.Put("tv", &cadencev2.ScreenContext{Revision: 2, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE, FocusedRef: "t:1", VisibleRefs: refs}) {
		t.Fatal("first put")
	}
	if s.Put("tv", &cadencev2.ScreenContext{Revision: 1, FocusedRef: "t:old"}) {
		t.Fatal("a late context replaced a newer one")
	}
	got, ok := s.Get("tv")
	if !ok || got.GetFocusedRef() != "t:1" || len(got.GetVisibleRefs()) != 24 {
		t.Fatalf("got %v", got)
	}
	now = now.Add(screens.ContextTTL)
	if _, ok := s.Get("tv"); ok {
		t.Fatal("a context outlived its ttl")
	}
	s.Put("tv", &cadencev2.ScreenContext{Revision: 1})
	s.Forget("tv")
	if _, ok := s.Get("tv"); ok {
		t.Fatal("forgotten")
	}
}

func TestInboundKeepsTheScreenContext(t *testing.T) {
	r := newRig(t)
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_ScreenContext{ScreenContext: &cadencev2.ScreenContext{Revision: 1, Query: "фрирен"}}}
	if !r.svc.Inbound("tv", env) {
		t.Fatal("a screen context is the store's")
	}
	if got, ok := r.svc.Contexts().Get("tv"); !ok || got.GetQuery() != "фрирен" {
		t.Fatalf("got %v", got)
	}
	r.svc.Closed("tv")
	if _, ok := r.svc.Contexts().Get("tv"); ok {
		t.Fatal("a closed link keeps its context")
	}
}

// a tool's own card reaches the service beside the bus, so it can come before its call or after its result: in each
// order the call keeps one card, under the call's id, with the tool's content (the step's card id raced before)
func TestAnAttachedCardIsItsCallsInEveryOrder(t *testing.T) {
	type move struct {
		do   func(r *rig)
		seen func(at *cadencev2.AssistantTurn) bool
	}
	steps := func(at *cadencev2.AssistantTurn) int { return len(at.GetSteps()) }
	call := move{func(r *rig) {
		r.publish("tool_call", map[string]any{"step": 1, "id": "c2", "name": "web_search", "args": json.RawMessage(`{}`)})
	}, func(at *cadencev2.AssistantTurn) bool { return steps(at) == 1 }}
	result := move{func(r *rig) {
		r.publish("tool_result", map[string]any{"step": 1, "id": "c2", "name": "web_search", "ok": true, "code": "",
			"result": json.RawMessage(`{"ok":true,"data":{}}`)})
	}, func(at *cadencev2.AssistantTurn) bool {
		return steps(at) == 1 && at.GetSteps()[0].GetStatus() == cadencev2.StepStatus_STEP_STATUS_DONE
	}}
	// a partial after the attachment goes through the same queue: once it shows, the attachment was taken
	attach := move{func(r *rig) {
		r.svc.Attach("spk", "s1", "t1:c2", cards.WebResults("t1:x", []providers.SearchResult{{Title: "Новость", URL: "https://news.example/1"}}, nil))
		r.svc.Transcript("spk", "s1", "найди новости", false)
	}, func(at *cadencev2.AssistantTurn) bool {
		return at.GetTranscript().GetUnstable()+at.GetTranscript().GetStable() != ""
	}}
	orders := map[string][]move{
		"attach, call, result": {attach, call, result},
		"call, attach, result": {call, attach, result},
		"call, result, attach": {call, result, attach},
	}
	for name, moves := range orders {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.svc.Inbound("spk", &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionStart{SessionStart: &cadencev2.SessionStart{
				SessionId: "s1", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
			}}})
			r.await("tv", "listening", func(at *cadencev2.AssistantTurn) bool { return at.GetTurnId() == "s1" })
			for i, m := range moves {
				m.do(r)
				r.await("tv", fmt.Sprintf("move %d", i), m.seen)
			}
			id := cards.CallCardID("t1:c2")
			at := r.await("tv", "the step done with its results", func(at *cadencev2.AssistantTurn) bool {
				if steps(at) != 1 || at.GetSteps()[0].GetStatus() != cadencev2.StepStatus_STEP_STATUS_DONE || len(at.GetCards()) != 1 {
					return false
				}
				ch := at.GetCards()[0].GetRoot().GetCard().GetChildren()
				return len(ch) > 0 && ch[0].GetPagedList() != nil
			})
			if at.GetSteps()[0].GetCardId() != id || at.GetCards()[0].GetId() != id {
				t.Fatalf("step card %q, card %q, want %q", at.GetSteps()[0].GetCardId(), at.GetCards()[0].GetId(), id)
			}
		})
	}
}
