package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/agenttest"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type screenFake struct {
	cat      *agenttest.Catalog
	contexts map[string]*cadencev2.ScreenContext

	mu   sync.Mutex
	sent []string
}

func newScreenFake() *screenFake {
	return &screenFake{cat: agenttest.NewCatalog(), contexts: map[string]*cadencev2.ScreenContext{}}
}

func (f *screenFake) deps() tools.ScreenDeps {
	return tools.ScreenDeps{
		Catalog: f.cat,
		Contexts: func(id string) (*cadencev2.ScreenContext, bool) {
			sc, ok := f.contexts[id]
			return sc, ok
		},
		Command: func(_ context.Context, id, capability, name string, args []byte) (json.RawMessage, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sent = append(f.sent, id+" "+capability+"/"+name+" "+string(args))
			return nil, nil
		},
	}
}

func (f *screenFake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func screenTool(d tools.ScreenDeps, name string) agent.Tool {
	for _, t := range tools.Screens(d) {
		if t.Name == name {
			return t
		}
	}
	panic(name)
}

// every screen and catalog tool: no event turn, a screen it reaches, exempt from the taint only where it writes
func TestScreenToolsPolicy(t *testing.T) {
	for _, tl := range tools.Screens(newScreenFake().deps()) {
		if !tl.NoEventTurns || tl.Reach == nil {
			t.Errorf("%s: event turns or reach", tl.Name)
		}
		write := tl.Effects == agent.Write
		if write != tl.TaintExempt || write == tl.Taints() {
			t.Errorf("%s: write %v exempt %v taints %v", tl.Name, write, tl.TaintExempt, tl.Taints())
		}
	}
	if tl := screenTool(tools.ScreenDeps{}, "catalog_play"); tl.Available(settings.Defaults()) {
		t.Fatal("catalog tools without a catalog")
	}
}

// navigation opens what was asked, and an item only for a ref this server issued
func TestScreenNavigate(t *testing.T) {
	f := newScreenFake()
	nav := screenTool(f.deps(), "screen_navigate")
	ref := f.cat.Ref(t, catalogtest.FrierenAniLiberty)
	for _, args := range []string{`{"to":"search","query":"Фрирен"}`, `{"to":"episodes","ref":"` + ref + `","episode":"5"}`, `{"to":"item","ref":"` + ref + `"}`} {
		if _, e := call(t, nav, "tv", args); e != nil {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	want := []string{
		`tv display.screen/show {"query":"Фрирен","route":"search"}`,
		`tv display.screen/show {"episode":"5","ref":"` + ref + `","route":"episodes"}`,
		`tv display.screen/show {"ref":"` + ref + `","route":"title"}`,
	}
	if got := f.commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent %q", got)
	}
	for args, code := range map[string]string{
		`{"to":"item","ref":"c:made-up"}`: "unknown_ref",
		`{"to":"item"}`:                   "invalid_arguments",
		`{"to":"search"}`:                 "invalid_arguments",
	} {
		if _, e := call(t, nav, "tv", args); e == nil || e.Code != code {
			t.Fatalf("%s: %+v", args, e)
		}
	}
	if len(f.commands()) != 3 {
		t.Fatal("a refused call sent")
	}
}

func TestScreenStateAndSwitch(t *testing.T) {
	f := newScreenFake()
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	f.contexts["tv"] = &cadencev2.ScreenContext{
		Route: cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER,
		Player: &cadencev2.ScreenPlayer{ItemRef: ep, PositionMs: 61_500, Tracks: []*cadencev2.MediaTrack{
			{Id: "a1", Kind: cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, Label: "Русский", Selected: true},
			{Id: "s2", Kind: cadencev2.MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, Label: "English"},
		}},
	}
	out, e := call(t, screenTool(f.deps(), "screen_state"), "tv", `{}`)
	if e != nil || !strings.Contains(string(out), `"route":"player"`) || !strings.Contains(string(out), `"positionS":61`) ||
		!strings.Contains(string(out), `"title":"Фрирен, серия 5"`) || !strings.Contains(string(out), `{"id":"s2","kind":"subtitle","label":"English"}`) {
		t.Fatalf("state %s %+v", out, e)
	}
	if strings.Contains(string(out), "torrplayer.lan") {
		t.Fatalf("a url reached the model: %s", out)
	}
	sw := screenTool(f.deps(), "media_switch")
	if _, e := call(t, sw, "tv", `{"subtitles":"s2"}`); e != nil {
		t.Fatal(e)
	}
	if _, e := call(t, sw, "tv", `{"audio":"s2"}`); e == nil || e.Code != "unknown_track" {
		t.Fatalf("wrong kind: %+v", e)
	}
	if _, e := call(t, sw, "tv", `{"voiceover":"anilibria"}`); e != nil {
		t.Fatal(e)
	}
	sent := f.commands()
	if len(sent) != 2 || sent[0] != `tv media.video/select_track {"id":"s2","kind":"subtitle"}` ||
		!strings.Contains(sent[1], `"startMs":61500`) {
		t.Fatalf("sent %q", sent)
	}
	res := f.cat.Resolved()
	if len(res) != 1 || res[0].Key != catalogtest.FrierenEpisode5 || res[0].Selection[catalog.GroupVoiceover] != "anilibria" {
		t.Fatalf("resolved %+v", res)
	}
	// the stream is resolved for the screen it plays on, which a relaying provider registers it with
	if devs := f.cat.ResolvedFor(); len(devs) != 1 || devs[0] != "tv" {
		t.Fatalf("resolved for %q", devs)
	}
	if _, e := call(t, sw, "elsewhere", `{"quality":"720"}`); e == nil || e.Code != "nothing_playing" {
		t.Fatalf("nothing: %+v", e)
	}
}

// a voiceover without seeders answers unplayable, so the model tries another; nothing is sent
func TestCatalogPlayUnplayable(t *testing.T) {
	f := newScreenFake()
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	if _, e := call(t, screenTool(f.deps(), "catalog_play"), "tv", `{"ref":"`+ep+`","voiceover":"dead"}`); e == nil || e.Code != "unplayable" {
		t.Fatalf("%+v", e)
	}
	if _, e := call(t, screenTool(f.deps(), "catalog_play"), "tv", `{"ref":"c:nope"}`); e == nil || e.Code != "unknown_ref" {
		t.Fatalf("%+v", e)
	}
	if len(f.commands()) != 0 {
		t.Fatal("sent")
	}
}

// "this" is the focused item off the player; my_list takes its value
func TestMediaItemAction(t *testing.T) {
	f := newScreenFake()
	series := f.cat.Ref(t, catalogtest.FrierenAniLiberty)
	f.contexts["tv"] = &cadencev2.ScreenContext{Route: cadencev2.ScreenRoute_SCREEN_ROUTE_TITLE, PageRef: series}
	act := screenTool(f.deps(), "media_item_action")
	out, e := call(t, act, "tv", `{"action":"my_list","value":"watching"}`)
	if e != nil || !strings.Contains(string(out), `"kind":"my_list","label":"Мой список","toggled":true,"enabled":true,"value":"watching"`) {
		t.Fatalf("%s %+v", out, e)
	}
	if _, e := call(t, act, "tv", `{"action":"like"}`); e == nil || e.Code != "unsupported" {
		t.Fatalf("like: %+v", e)
	}
	if _, e := call(t, act, "tv", `{"action":"my_list"}`); e == nil || e.Code != "invalid_arguments" {
		t.Fatalf("no value: %+v", e)
	}
	// a follow-up may (only rule 7 needs a woken session); an event turn is never offered it
	if _, e := run(t, act, cadencev2.Trigger_TRIGGER_FOLLOW_UP, `{"action":"subscribe","ref":"`+series+`"}`); e != nil && e.Code == "wake_required" {
		t.Fatalf("follow-up: %+v", e)
	}
	if got := agent.NewRegistry(act).Offer(nil, false, cadencev2.Trigger_TRIGGER_EVENT); len(got) != 0 {
		t.Fatalf("an event turn is offered %d", len(got))
	}
}

// a search answers refs the item and play tools take; nothing found says how to try again
func TestCatalogSearchGivesRefs(t *testing.T) {
	f := newScreenFake()
	search := screenTool(f.deps(), "catalog_search")
	out, e := call(t, search, "tv", `{"query":"фрирен"}`)
	if e != nil {
		t.Fatalf("search: %+v", e)
	}
	var got struct {
		Items []struct {
			Ref, Title string
		}
		Hint string
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Items) == 0 || got.Hint != "" {
		t.Fatalf("search answered %s", out)
	}
	item := screenTool(f.deps(), "catalog_item")
	if _, e := call(t, item, "tv", `{"ref":"`+got.Items[0].Ref+`"}`); e != nil {
		t.Fatalf("item of a found ref: %+v", e)
	}
	if out, e = call(t, search, "tv", `{"query":"nothing like it"}`); e != nil || !strings.Contains(string(out), `"items":[]`) ||
		!strings.Contains(string(out), "another spelling") {
		t.Fatalf("empty search: %s %+v", out, e)
	}
	// a made-up ref is pointed back at the search
	if _, e := call(t, item, "tv", `{"ref":"c:made-up"}`); e == nil || e.Code != "unknown_ref" || !strings.Contains(e.Hint, "catalog_search") {
		t.Fatalf("made-up ref: %+v", e)
	}
}

// a follower's play (multiroom P4) carries its group and no position, and is no play the device keeps
func TestFollowPlaysWithTheGroup(t *testing.T) {
	f := newScreenFake()
	d := f.deps()
	var played []tools.PlayedVideo
	d.Played = func(_ string, p tools.PlayedVideo) { played = append(played, p) }
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	if _, e := d.Follow(context.Background(), "tv2", catalog.Ref(ep), "", nil, "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"); e != nil {
		t.Fatalf("%+v", e)
	}
	sent := f.commands()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "tv2 media.video/play ") {
		t.Fatalf("sent %v", sent)
	}
	var args struct {
		StartMS *int64 `json:"startMs"`
		Follow  struct {
			GroupID string `json:"groupId"`
		} `json:"follow"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(sent[0], "tv2 media.video/play ")), &args); err != nil ||
		args.StartMS != nil || args.Follow.GroupID != "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60" {
		t.Fatalf("args %+v %v", args, err)
	}
	if len(played) != 0 {
		t.Fatalf("a follower's play was kept: %+v", played)
	}
}
