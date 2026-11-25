package agent_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/agenttest"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// the household of the screen tests: this device (the harness's) is the
// living room's speaker
const (
	livingTV  = "tv-living-0000-0000-0000-000000000001"
	bedroomTV = "tv-bedroom-000-0000-0000-000000000002"
	secondTV  = "tv-living-0000-0000-0000-000000000003"
)

func speakerInfo() agent.DeviceInfo {
	return agent.DeviceInfo{
		ID: device, Name: "Колонка", Room: "living_room", Kind: agent.KindSpeaker, Online: true,
		Caps: []string{"audio.output", "media.url", "media.player", "alarms"}, RemoteTargets: "allow",
	}
}

func tvInfo(id, name, room string) agent.DeviceInfo {
	return agent.DeviceInfo{
		ID: id, Name: name, Room: room, Kind: agent.KindScreen, Online: true,
		Caps: []string{"display.screen", "media.video", "media.player", "audio.output"}, RemoteTargets: "allow",
	}
}

type sentCommand struct{ device, capability, name, args string }

// screenRig is a harness with the household, the screens' contexts, the
// catalog and the screen tools beside device_volume
type screenRig struct {
	*harness
	devices  []agent.DeviceInfo
	contexts map[string]*cadencev2.ScreenContext
	cat      *agenttest.Catalog

	mu   sync.Mutex
	sent []sentCommand
}

func newScreenRig(t *testing.T, steps ...llmStep) *screenRig {
	r := &screenRig{
		harness: newHarness(t, steps...), contexts: map[string]*cadencev2.ScreenContext{}, cat: agenttest.NewCatalog(),
		devices: []agent.DeviceInfo{speakerInfo(), tvInfo(livingTV, "Телевизор", "living_room"), tvInfo(bedroomTV, "Спальня ТВ", "bedroom")},
	}
	r.household = func(context.Context) ([]agent.DeviceInfo, error) { return slices.Clone(r.devices), nil }
	r.screens = func(id string) (*cadencev2.ScreenContext, bool) {
		sc, ok := r.contexts[id]
		return sc, ok
	}
	r.catalog = r.cat
	resolver := &agent.Resolver{Household: r.household}
	command := func(_ context.Context, id, capability, name string, args []byte) (json.RawMessage, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.sent = append(r.sent, sentCommand{id, capability, name, string(args)})
		return json.RawMessage(`{"level":30}`), nil
	}
	r.tools = append(tools.Screens(tools.ScreenDeps{
		Target: resolver.ResolveTarget, Contexts: r.screens, Catalog: r.cat, Command: command,
		Profile: func(context.Context, string) catalog.DeviceProfile {
			return catalog.DeviceProfile{Screen: true, MaxHeight: 1080}
		},
	}), tools.DeviceVolume(tools.VolumeDeps{
		State: func(context.Context, string, string) (json.RawMessage, bool) {
			return json.RawMessage(`{"level":30}`), true
		},
		Command: command, Target: resolver.ResolveTarget,
	}))
	return r
}

func (r *screenRig) commands() []sentCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

// the tool result the turn stored for call id
func (r *screenRig) stored(t *testing.T, id string) map[string]any {
	t.Helper()
	for _, a := range r.convs.stored {
		for _, m := range a.msgs {
			if m.Role == "tool" && m.ToolCallID == id {
				var out map[string]any
				if err := json.Unmarshal([]byte(m.Content), &out); err != nil {
					t.Fatal(err)
				}
				return out
			}
		}
	}
	t.Fatalf("no stored result of %s", id)
	return nil
}

func systemNotes(req providers.LLMRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "system" {
			b.WriteString(m.Content + "\n")
		}
	}
	return b.String()
}

// «включи это аниме на телевизоре» from the speaker plays the focused ref on its room's screen
func TestPlaysTheFocusedRefOnTheRoomScreen(t *testing.T) {
	r := newScreenRig(t)
	cat := r.cat
	page, focused := cat.Ref(t, catalogtest.FrierenAniLiberty), cat.Ref(t, catalogtest.FrierenEpisode5)
	r.contexts[livingTV] = &cadencev2.ScreenContext{
		Revision: 3, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE, PageRef: page, FocusedRef: focused,
		Selection: &cadencev2.ScreenSelection{Source: "aniliberty", Voiceover: "anilibria", Quality: "1080"},
	}
	// the title's voiceover was chosen before, so the play takes it without asking
	cat.Remember(t, page, catalog.Selection{catalog.GroupVoiceover: "anilibria"})
	r.llm.steps = []llmStep{{calls: []providers.ToolCall{toolCall("c1", "catalog_play", `{"ref":"`+focused+`","device":"на телевизоре"}`)}}}
	res, _ := r.run(cadencev2.Trigger_TRIGGER_WAKE, "включи это аниме на телевизоре")
	if res.Code != "" || !res.Silent {
		t.Fatalf("result %+v", res)
	}
	notes := systemNotes(r.requests(1)[0])
	for _, want := range []string{
		`- This device: "Колонка" in "living_room", speaker (music, alarms)`,
		`- "Телевизор" in "living_room", screen (video, music, screen)`,
		`Screen "Телевизор" in "living_room" shows a title page`,
		`focused: "Фрирен, серия 5" (episode, source "aniliberty", episode "5") ref ` + focused,
		`page: "Провожающая в последний путь Фрирен" (series, source "aniliberty", 28 of 28 episodes aired) ref ` + page,
		`chosen: source "aniliberty", voiceover "anilibria", quality "1080"`,
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	sent := r.commands()
	if len(sent) != 1 || sent[0].device != livingTV || sent[0].capability != "media.video" || sent[0].name != "play" {
		t.Fatalf("sent %+v", sent)
	}
	var args struct {
		URL      string `json:"url"`
		StartMS  int64  `json:"startMs"`
		Metadata struct {
			ItemRef, Episode, Title string
		} `json:"metadata"`
		Subtitles []struct{ Format string } `json:"subtitles"`
	}
	if err := json.Unmarshal([]byte(sent[0].args), &args); err != nil {
		t.Fatal(err)
	}
	if args.URL != "http://torrplayer.lan/resolve/r1" || args.StartMS != 300_000 || args.Metadata.ItemRef != focused ||
		args.Metadata.Episode != "5" || len(args.Subtitles) != 1 || args.Subtitles[0].Format != "ass" {
		t.Fatalf("play %s", sent[0].args)
	}
	// the history records the target
	if got := r.stored(t, "c1"); got["target"] != livingTV || got["ok"] != true {
		t.Fatalf("stored %+v", got)
	}
}

// «следующую серию с той же озвучкой» on the screen itself walks torrplayer's tree: the series' episodes, then the
// next one with the voiceover the player has
func TestNextEpisodeWithTheSameVoiceover(t *testing.T) {
	r := newScreenRig(t)
	cat := r.cat
	page, playing := cat.Ref(t, catalogtest.FrierenAniLiberty), cat.Ref(t, catalogtest.FrierenEpisode5)
	r.devices[0], r.devices[1] = tvInfo(device, "Телевизор", "living_room"), speakerInfo()
	r.devices[1].ID = "speaker-0000"
	r.contexts[device] = &cadencev2.ScreenContext{
		Revision: 9, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER, PageRef: page,
		Selection: &cadencev2.ScreenSelection{Voiceover: "subs", Episode: "5"},
		Player: &cadencev2.ScreenPlayer{ItemRef: playing, PositionMs: 1_200_000, Tracks: []*cadencev2.MediaTrack{
			{Id: "a1", Kind: cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, Label: "Русский", Selected: true},
		}},
	}
	r.llm.steps = []llmStep{
		{calls: []providers.ToolCall{toolCall("c1", "catalog_item", `{"ref":"`+page+`"}`)}},
		{calls: []providers.ToolCall{toolCall("c2", "catalog_play", `{"ref":"`+page+`","episode":"5.5","voiceover":"subs"}`)}},
	}
	res, _ := r.run(cadencev2.Trigger_TRIGGER_BUTTON, "следующую серию с той же озвучкой")
	if res.Code != "" || !res.Silent {
		t.Fatalf("result %+v", res)
	}
	reqs := r.requests(2)
	notes := systemNotes(reqs[0])
	if !strings.Contains(notes, `This screen shows the player`) || !strings.Contains(notes, `playing: "Фрирен, серия 5"`) ||
		!strings.Contains(notes, `at 20:00, audio "Русский"`) || !strings.Contains(notes, `voiceover "subs"`) {
		t.Fatalf("notes:\n%s", notes)
	}
	// the episodes the model read, each with its ref and label
	item := last(reqs[1].Messages).Content
	for _, label := range []string{`"label":"1"`, `"label":"5"`, `"label":"5.5"`, `"continue":{"Episode":"5"`} {
		if !strings.Contains(item, label) {
			t.Fatalf("catalog_item lacks %s: %s", label, item)
		}
	}
	resolved := cat.Resolved()
	if len(resolved) != 1 || resolved[0].Key != catalogtest.FrierenRecap || resolved[0].Selection[catalog.GroupVoiceover] != "subs" ||
		resolved[0].From != catalog.FromResume {
		t.Fatalf("resolved %+v", resolved)
	}
	if devs := cat.ResolvedFor(); len(devs) != 1 || devs[0] != device {
		t.Fatalf("resolved for %q, want %q", devs, device)
	}
	if sent := r.commands(); len(sent) != 1 || sent[0].device != device || !strings.Contains(sent[0].args, `"episode":"5.5"`) {
		t.Fatalf("sent %+v", sent)
	}
}

// «какая это серия?» is answered from the screen note alone
func TestWhichEpisodeFromTheScreenNote(t *testing.T) {
	r := newScreenRig(t, llmStep{text: "Пятая серия."})
	playing := r.cat.Ref(t, catalogtest.FrierenEpisode5)
	r.contexts[livingTV] = &cadencev2.ScreenContext{
		Revision: 1, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER,
		Player: &cadencev2.ScreenPlayer{ItemRef: playing, PositionMs: 65_000},
	}
	res, outs := r.run(cadencev2.Trigger_TRIGGER_WAKE, "какая это серия?")
	if res.Code != "" {
		t.Fatalf("result %+v", res)
	}
	wantSpeech(t, outs, "Пятая серия.")
	notes := systemNotes(r.requests(1)[0])
	if !strings.Contains(notes, `playing: "Фрирен, серия 5" (episode, source "aniliberty", episode "5") ref `+playing+" at 1:05") {
		t.Fatalf("notes:\n%s", notes)
	}
	if len(r.commands()) != 0 {
		t.Fatal("a question sent a command")
	}
}

// a room with two screens answers the candidates, and nothing plays
func TestAmbiguousRoomAnswersCandidates(t *testing.T) {
	r := newScreenRig(t)
	r.devices = append(r.devices, tvInfo(secondTV, "Проектор", "living_room"))
	ep := r.cat.Ref(t, catalogtest.FrierenEpisode5)
	r.llm.steps = []llmStep{
		{calls: []providers.ToolCall{toolCall("c1", "catalog_play", `{"ref":"`+ep+`","device":"living room"}`)}},
		{text: "На каком экране?"},
	}
	res, _ := r.run(cadencev2.Trigger_TRIGGER_WAKE, "включи в гостиной")
	if res.Code != "" {
		t.Fatalf("result %+v", res)
	}
	got := r.stored(t, "c1")
	e, _ := got["error"].(map[string]any)
	if e["code"] != "ambiguous_device" || !slices.Equal(anyStrings(e["candidates"]), []string{"Телевизор", "Проектор"}) {
		t.Fatalf("stored %+v", got)
	}
	if len(r.commands()) != 0 {
		t.Fatal("played")
	}
}

func anyStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// rule 7 per target, and the cross-device policy: no raise on another device in a follow-up nobody woke
func TestRemoteVolumeRaiseInAFollowUpIsRefused(t *testing.T) {
	r := newScreenRig(t,
		llmStep{calls: []providers.ToolCall{toolCall("c1", "device_volume", `{"change":20,"device":"Телевизор"}`)}},
		llmStep{text: "Сначала разбуди меня."})
	res, _ := r.run(cadencev2.Trigger_TRIGGER_FOLLOW_UP, "громче на телевизоре")
	if res.Code != "" {
		t.Fatalf("result %+v", res)
	}
	if e, _ := r.stored(t, "c1")["error"].(map[string]any); e["code"] != "wake_required" {
		t.Fatalf("stored %+v", r.stored(t, "c1"))
	}
	if len(r.commands()) != 0 {
		t.Fatal("sent")
	}
	// woken, the same call reaches the screen's volume
	r2 := newScreenRig(t, llmStep{calls: []providers.ToolCall{toolCall("c1", "device_volume", `{"change":20,"device":"Телевизор"}`)}})
	r2.run(cadencev2.Trigger_TRIGGER_WAKE, "громче на телевизоре")
	if sent := r2.commands(); len(sent) != 1 || sent[0].device != livingTV || sent[0].name != "change_level" {
		t.Fatalf("woken: %+v", sent)
	}
}

// remoteTargets deny keeps a device from being driven by others; the note says so
func TestRemoteTargetsDenyRefuses(t *testing.T) {
	r := newScreenRig(t)
	r.devices[1].RemoteTargets = settings.RemoteTargetsDeny
	ep := r.cat.Ref(t, catalogtest.FrierenEpisode5)
	r.llm.steps = []llmStep{
		{calls: []providers.ToolCall{toolCall("c1", "catalog_play", `{"ref":"`+ep+`","device":"Телевизор"}`)}},
		{text: "Телевизор не принимает команды отсюда."},
	}
	r.run(cadencev2.Trigger_TRIGGER_WAKE, "включи на телевизоре")
	if e, _ := r.stored(t, "c1")["error"].(map[string]any); e["code"] != "remote_refused" {
		t.Fatalf("stored %+v", r.stored(t, "c1"))
	}
	if len(r.commands()) != 0 {
		t.Fatal("played")
	}
	if notes := systemNotes(r.requests(2)[0]); !strings.Contains(notes, `"Телевизор" in "living_room", screen (video, music, screen); does not take commands from this device`) {
		t.Fatalf("notes:\n%s", notes)
	}
}

var screenTools = []string{"screen_state", "screen_navigate", "catalog_search", "catalog_item", "catalog_alternatives", "catalog_play", "media_switch", "media_item_action"}

// an event turn gets none of the screen tools, nor the devices and screen notes
func TestEventTurnsGetNoScreenTools(t *testing.T) {
	r := newScreenRig(t, llmStep{text: "Задача готова."})
	r.contexts[livingTV] = &cadencev2.ScreenContext{Revision: 1, Route: cadencev2.ScreenRoute_SCREEN_ROUTE_HOME}
	r.run(cadencev2.Trigger_TRIGGER_EVENT, "a task finished")
	req := r.requests(1)[0]
	for _, name := range toolNames(req.Tools) {
		if slices.Contains(screenTools, name) {
			t.Fatalf("event turn offered %s", name)
		}
	}
	if notes := systemNotes(req); strings.Contains(notes, "Devices of this household") || strings.Contains(notes, "shows the home page") {
		t.Fatalf("notes:\n%s", notes)
	}
}

// a speaker that reaches no screen is not offered the screen-only tools
func TestScreenToolsNeedAReachableScreen(t *testing.T) {
	r := newScreenRig(t, llmStep{text: "ok"})
	r.devices = r.devices[:1]
	r.run(cadencev2.Trigger_TRIGGER_WAKE, "что на экране?")
	got := toolNames(r.requests(1)[0].Tools)
	if !slices.Equal(got, []string{"device_volume"}) {
		t.Fatalf("offered %v", got)
	}
	r2 := newScreenRig(t, llmStep{text: "ok"})
	r2.run(cadencev2.Trigger_TRIGGER_WAKE, "что на экране?")
	if got := toolNames(r2.requests(1)[0].Tools); !slices.Equal(got, append(slices.Clone(screenTools), "device_volume")) {
		t.Fatalf("offered %v", got)
	}
}
