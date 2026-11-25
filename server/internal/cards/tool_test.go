package cards_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

var update = flag.Bool("update", false, "rewrite the tool cards' golden examples in pkg/cards/testdata/cards")

// the tool cards' examples join pkg/cards' own, so pkg/protofixtures makes
// them binaries the screen's renderer draws (make screen-test)
const goldenDir = "../../../pkg/cards/testdata/cards"

var at = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

// toolSamples are each builder's results as its tool writes them
var toolSamples = []struct {
	name, tool, args, data string
}{
	{"weather_day", "weather", `{"day":"today"}`, `{"place":"Москва","days":[{"date":"2026-11-14","min":-2.4,"max":4.2,"precipitationMm":1.5,"code":61,"summary":"Небольшой дождь"}]}`},
	{"weather_days", "weather", `{"days":3}`, `{"place":"Москва","days":[{"date":"2026-11-14","min":-2,"max":4,"code":3,"summary":"Облачно"},{"date":"2026-11-15","min":-5,"max":0,"code":73,"summary":"Снег"},{"date":"2026-11-16","min":-6,"max":-1,"code":0,"summary":"Ясно"}]}`},
	{"timer", "timer", `{"action":"set","seconds":600}`, `{"timer":{"seconds":600,"remainingS":600}}`},
	{"timers", "timer", `{"action":"list"}`, `{"timers":[{"seconds":600,"remainingS":312},{"seconds":5400,"remainingS":5100}]}`},
	{"timer_cancelled", "timer", `{"action":"cancel","all":true}`, `{"cancelled":[{"seconds":600}]}`},
	{"alarm_set", "alarm", `{"action":"set","time":"7:30","days":["weekdays"]}`, `{"alarm":{"time":"7:30","days":["mon","tue","wed","thu","fri"],"level":40},"replaced":false}`},
	{"alarms", "alarm", `{"action":"list"}`, `{"alarms":[{"time":"7:30","days":["mon","tue","wed","thu","fri"]},{"time":"10:00","days":["sat","sun"]},{"time":"6:15","date":"2026-11-20"}]}`},
	{"alarm_cancelled", "alarm", `{"action":"cancel","time":"7:30"}`, `{"cancelled":[{"time":"7:30","days":["mon"]}]}`},
	{"dnd_on", "do_not_disturb", `{"minutes":60}`, `{"on":true,"minutes":60,"until":"13:00"}`},
	{"dnd_off", "do_not_disturb", `{"off":true}`, `{"on":false,"wasOn":true}`},
	{"volume", "device_volume", `{"level":30}`, `{"level":30}`},
	{"agent_mode", "agent_mode", `{"mode":"умный"}`, `{"mode":"smart"}`},
	{"music_search", "music_search", `{"query":"Кино Группа крови"}`, `{"results":[{"id":"m1","title":"Группа крови","artist":"Кино","album":"Группа крови","durationS":285,"provider":"YouTube Music"},{"id":"m2","kind":"album","title":"Группа крови","artist":"Кино","trackCount":11,"provider":"Spotify"},{"id":"m3","kind":"playlist","title":"Русский рок","owner":"Spotify","trackCount":50,"provider":"Spotify"}]}`},
	{"music_play", "music_play", `{"id":"m2"}`, `{"playing":true,"songs":11}`},
	{"music_queued", "music_play", `{"id":"m1","when":"next"}`, `{"queued":true}`},
	{"video_search", "video_search", `{"query":"лекция"}`, `{"results":[{"id":"v1","title":"Лекция о космосе","channel":"Наука","durationS":3725},{"id":"v2","title":"Запуск ракеты","channel":"Роскосмос","live":true}]}`},
	{"video_play", "video_play", `{"id":"v1"}`, `{"playing":true,"height":1080,"live":false}`},
	{"youtube_search", "youtube_search", `{"query":"ролики куплинова","uploaded":"week"}`, `{"items":[{"ref":"c:ch","kind":"channel","title":"Купли Барамидзе","subtitle":"@kuplinov","subscribers":3900000,"videoCount":2400},{"ref":"c:v1","kind":"video","title":"YT Probably Stolen 9","durationS":2760,"views":133000,"published":"2026-11-17","channel":{"ref":"c:ch","title":"Купли Барамидзе"}},{"ref":"c:v2","kind":"live","title":"Прямой эфир сейчас","channel":{"ref":"c:ch","title":"Купли Барамидзе"}}]}`},
	{"screen_state", "screen_state", `{"device":"tv"}`, `{"route":"title","page":{"ref":"t:1","kind":"series","title":"Тьма"},"visible":[{"ref":"e:1","title":"Секреты"},{"ref":"e:2","title":"Ложь"}],"player":{"item":{"ref":"e:1","title":"Секреты"},"positionS":640,"tracks":[{"id":"a","kind":"audio"},{"id":"s","kind":"subtitle","selected":true}]}}`},
	{"youtube_browse", "youtube_browse", `{"ref":"c:ch","tab":"videos","sort":"latest"}`, `{"item":{"ref":"c:ch","kind":"channel","title":"Купли Барамидзе","subtitle":"@kuplinov","subscribers":3900000},"children":[{"ref":"c:v1","kind":"video","title":"YT Probably Stolen 9","durationS":2760,"views":133000,"channel":{"ref":"c:ch","title":"Купли Барамидзе"}},{"ref":"c:v2","kind":"video","title":"Стрим про хоррор","durationS":5412,"views":81000,"channel":{"ref":"c:ch","title":"Купли Барамидзе"}}]}`},
	{"media_pause", "media_control", `{"action":"pause"}`, `{"ok":true}`},
	{"media_seek", "media_control", `{"action":"seek","positionS":95}`, `{"ok":true}`},
	{"catalog_item", "catalog_item", `{"ref":"c1"}`, `{"ref":"c1","provider":"torrplayer","kind":"series","title":"Тьма","subtitle":"2017 · 3 сезона","playable":false,"hasChildren":true,"children":[{"provider":"torrplayer","kind":"episode","title":"Секреты","subtitle":"Серия 1","playable":true,"hasChildren":false},{"provider":"torrplayer","kind":"episode","title":"Ложь","subtitle":"Серия 2","playable":true,"hasChildren":false}]}`},
	{"catalog_alternatives", "catalog_alternatives", `{"ref":"c1"}`, `{"items":[{"ref":"c2","source":"AniLiberty","title":"Фрирен","match":"shikimori","episodes":28,"voiceovers":["AniLibria","Субтитры"],"quality":1080},{"ref":"c1","source":"anime365","title":"Фрирен","match":"shikimori","episodes":26,"voiceovers":["AniDUB","SHIZA","Studio Band","Субтитры"],"quality":720,"current":true}]}`},
	{"media_switch", "media_switch", `{"voiceover":"vo2","quality":"1080"}`, `{"ok":true}`},
	{"media_like", "media_item_action", `{"action":"like"}`, `{"ok":true,"actions":[]}`},
	{"task_list", "task_list", `{}`, `{"tasks":[{"id":"ab12","kind":"hermes","title":"Собрать отчёт","status":"running"},{"id":"cd34","kind":"hermes","title":"","status":"waiting_input"},{"id":"ef56","kind":"hermes","title":"Обновить сервер","status":"done"}]}`},
	{"subscribe", "subscribe", `{"topic":"t3code:home:thread/abc"}`, `{"id":"s1","topic":"t3code:home:thread/abc","events":["done","question"],"delivery":"speaker","until":"next_stop"}`},
	{"unsubscribe", "unsubscribe", `{"id":"s1"}`, `{"id":"s1","topic":"t3code:home:thread/abc","events":["done"],"delivery":"speaker","until":""}`},
	{"subscriptions", "subscriptions", `{}`, `{"subscriptions":[{"id":"s1","topic":"t3code:home:thread/abc","events":["done"],"delivery":"agent","until":""}]}`},
	{"subscription_updates", "subscription_inspect", `{}`, `{"updates":[{"topic":"t3code:home:thread/abc","event":"done","title":"Рефакторинг завершён","at":"2026-11-14T11:50:00Z"}]}`},
	{"subscription_reply", "subscription_reply", `{"token":"q1"}`, `{"topic":"t3code:home:thread/abc","answered":true}`},
	{"t3_threads", "t3_threads", `{}`, `{"projects":[],"threads":[{"instance":"home","id":"th1","project":"p1","title":"Починить сборку","status":"running"},{"instance":"home","id":"th2","project":"p1","title":"Обновить зависимости","status":"idle","waiting":"approval"}],"more":3}`},
	{"t3_launch", "t3_launch", `{"project":"p1"}`, `{"instance":"home","thread":"th3","project":"p1","started":true}`},
	{"generic", "notes_add", `{"note":"пароль от wifi: hunter2"}`, `{"saved":true,"token":"sk-live-123"}`},
}

func result(name, tool, args, data string, ok bool) cards.ToolResult {
	r := cards.ToolResult{Key: "turn-1:" + name, Tool: tool, OK: ok, Args: json.RawMessage(args), At: at}
	if data != "" {
		r.Data = json.RawMessage(data)
	}
	return r
}

func show(t *testing.T, lang string, subj cards.Subject) *cardsv1.Card {
	t.Helper()
	s := cards.New(cards.Deps{Lang: func(context.Context, string) string { return lang }})
	c, err := s.Show(context.Background(), "tv-1", subj)
	if err != nil {
		t.Fatalf("%s: %v", subj.ID(), err)
	}
	return c
}

// a catalog key the catalog lacks comes back as the key: no card may show one
var keyLike = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_()]+)+$`)

func shownText(m protoreflect.Message, out *[]string) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.Name() == "id" || fd.Name() == "features" || fd.Name() == "icon" || fd.Name() == "name" && fd.ContainingMessage().Name() == "Icon":
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			for i := range v.List().Len() {
				shownText(v.List().Get(i).Message(), out)
			}
		case fd.Kind() == protoreflect.MessageKind:
			shownText(v.Message(), out)
		case fd.Kind() == protoreflect.StringKind:
			*out = append(*out, v.String())
		}
		return true
	})
}

func TestToolCardsMatchTheirExamples(t *testing.T) {
	tc := &cards.ToolCards{}
	for _, s := range toolSamples {
		t.Run(s.name, func(t *testing.T) {
			subs := tc.Subjects(result(s.name, s.tool, s.args, s.data, true))
			if len(subs) != 1 {
				t.Fatalf("%d cards, want 1", len(subs))
			}
			for _, lang := range []string{"en", "ru"} {
				c := show(t, lang, tc.Subjects(result(s.name, s.tool, s.args, s.data, true))[0])
				var text []string
				shownText(c.ProtoReflect(), &text)
				for _, x := range text {
					if keyLike.MatchString(x) {
						t.Errorf("%s: an untranslated key %q", lang, x)
					}
				}
				if lang != "ru" {
					continue
				}
				golden(t, "tool_"+s.name, c)
			}
		})
	}
}

// golden compares c with its example, protojson in pkg/cards' layout
func golden(t *testing.T, name string, c *cardsv1.Card) {
	t.Helper()
	path := filepath.Join(goldenDir, name+".json")
	raw, err := protojson.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", " "); err != nil {
		t.Fatal(err)
	}
	b.WriteByte('\n')
	if *update {
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil { //nolint:gosec // a public example
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", name, err)
	}
	var parsed cardsv1.Card
	if err := protojson.Unmarshal(want, &parsed); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(&parsed, c) {
		t.Fatalf("%s differs from its example (run with -update):\n%s", name, b.String())
	}
}

// a tool no builder knows (an MCP server's) shows its name and how it ended,
// never its arguments or its result
func TestGenericToolCard(t *testing.T) {
	tc := &cards.ToolCards{}
	for _, ok := range []bool{true, false} {
		subs := tc.Subjects(result("g", "notes_add", `{"note":"hunter2"}`, `{"token":"sk-live-123"}`, ok))
		if len(subs) != 1 {
			t.Fatalf("ok=%v: %d cards", ok, len(subs))
		}
		c := show(t, "ru", subs[0])
		b, _ := protojson.Marshal(c)
		if !strings.Contains(string(b), "notes_add") {
			t.Fatalf("no tool name: %s", b)
		}
		for _, leak := range []string{"hunter2", "sk-live", "note", "token"} {
			if strings.Contains(strings.ReplaceAll(string(b), "notes_add", ""), leak) {
				t.Fatalf("the card shows %q: %s", leak, b)
			}
		}
		if got := c.GetRoot().GetCard().GetTone(); ok != (got != cardsv1.Tone_TONE_DANGER) {
			t.Fatalf("ok=%v: tone %v", ok, got)
		}
	}
	golden(t, "tool_generic_failed", show(t, "ru", tc.Subjects(result("generic_failed", "notes_add", `{}`, "", false))[0]))
}

// a built-in tool that failed shows no card (its step says so), and one
// whose output is not its own shape shows none either
func TestBuiltinToolCardsOnlyForTheirResults(t *testing.T) {
	tc := &cards.ToolCards{}
	for _, s := range toolSamples {
		if s.name == "generic" || s.tool == "subscription_reply" || s.tool == "t3_launch" || s.tool == "media_switch" {
			continue
		}
		if got := tc.Subjects(result(s.name, s.tool, s.args, s.data, false)); len(got) != 0 {
			t.Errorf("%s: a failed call shows %d cards", s.name, len(got))
		}
		if strings.HasPrefix(s.tool, "media_") {
			continue // drawn from the call's own enumerated arguments
		}
		if got := tc.Subjects(result(s.name, s.tool, s.args, `"junk"`, true)); len(got) != 0 {
			t.Errorf("%s: junk output shows %d cards", s.name, len(got))
		}
	}
	// a screen_state read shows its own card now (what it read); the screen itself shows what navigate did
	for _, tool := range []string{"web_search", "x_search", "screen_navigate"} {
		if got := tc.Subjects(result("n", tool, `{}`, `{"ok":true}`, true)); len(got) != 0 {
			t.Errorf("%s shows %d cards: its tool or the screen shows them", tool, len(got))
		}
	}
	// an action the schema does not name draws nothing
	if got := tc.Subjects(result("m", "media_control", `{"action":"louder"}`, `{"ok":true}`, true)); len(got) != 0 {
		t.Error("an unknown media action shows a card")
	}
}

// a timer's countdown ends where the device's does, and its card expires
// with it
func TestTimerCardCountsDown(t *testing.T) {
	c := show(t, "ru", (&cards.ToolCards{}).Subjects(result("t", "timer", `{}`, `{"timer":{"seconds":600,"remainingS":590}}`, true))[0])
	cd := c.GetRoot().GetCard().GetChildren()[0].GetCountdown()
	if cd.GetEndsAtMs() != at.Add(590*time.Second).UnixMilli() {
		t.Fatalf("ends at %d", cd.GetEndsAtMs())
	}
	if c.GetTtlS() != 590+60 || !c.GetDismissable() {
		t.Fatalf("ttl %d dismissable %v", c.GetTtlS(), c.GetDismissable())
	}
}

type stubSubject struct{ id string }

func (s stubSubject) ID() string { return s.id }
func (stubSubject) Render(context.Context, *cards.Render) (*cardsv1.Card, error) {
	return nil, nil
}

// a task tool's call shows the task's own live card
func TestTaskToolsShowTheTask(t *testing.T) {
	var asked []string
	tc := &cards.ToolCards{Task: func(id string) cards.Subject { asked = append(asked, id); return stubSubject{"task-" + id} }}
	for _, tool := range []string{"task_start", "task_cancel", "task_reply", "task_inspect"} {
		subs := tc.Subjects(result("t", tool, `{}`, `{"id":"ab12","status":"running"}`, true))
		if len(subs) != 1 || subs[0].ID() != "task-ab12" {
			t.Fatalf("%s: %v", tool, subs)
		}
	}
	if len(asked) != 4 {
		t.Fatalf("asked %v", asked)
	}
	if got := (&cards.ToolCards{}).Subjects(result("t", "task_start", `{}`, `{"id":"ab12"}`, true)); len(got) != 0 {
		t.Fatal("no task cards: none shown")
	}
}

// every built-in tool has its card decided, and the table names no tool
// that does not exist: a new tool must choose a card or none
func TestEveryBuiltinToolHasACardDecision(t *testing.T) {
	var all []agent.Tool
	all = append(all,
		tools.Weather(tools.WeatherDeps{}), tools.WebSearch(tools.SearchDeps{}), tools.XSearch(tools.XSearchDeps{}),
		tools.AgentMode(tools.ModeDeps{}), tools.DeviceVolume(tools.VolumeDeps{}), tools.Alarm(tools.AlarmDeps{}),
		tools.Timer(tools.AlarmDeps{}), tools.Reminder(tools.AlarmDeps{}), tools.DoNotDisturb(tools.DNDDeps{}), tools.MediaControl(tools.MediaDeps{}),
		tools.HAControl(tools.HADeps{}), tools.HASearch(tools.HADeps{}), tools.HAInspect(tools.HADeps{}),
	)
	all = append(all, tools.Music(tools.MusicDeps{})...)
	all = append(all, tools.Video(tools.VideoDeps{})...)
	all = append(all, tools.YouTube(tools.ScreenDeps{})...)
	all = append(all, tools.Screens(tools.ScreenDeps{})...)
	all = append(all, tools.Subscriptions(tools.SubscriptionDeps{})...)
	all = append(all, tools.T3Code(tools.T3Deps{})...)
	// the task tools read the kinds when built; a manager without a store has none
	all = append(all, tools.Tasks(tools.TaskDeps{Manager: tasks.NewManager(tasks.ManagerDeps{})})...)
	var names []string
	for _, tl := range all {
		names = append(names, tl.Name)
		if !cards.Known(tl.Name) {
			t.Errorf("%s has no card decision in cards.toolBuilders", tl.Name)
		}
	}
	// the agent's seeded call of an update turn and its turn-scoped skill loader are tools too, and decided too
	names = append(names, "subscription_updates", agent.LoadSkill)
	for _, n := range cards.ToolNames() {
		if !slices.Contains(names, n) {
			t.Errorf("cards.toolBuilders names %s, which no built-in tool is", n)
		}
	}
}

// video_search's answers are wide tiles with the video tools' own results: each its still and its length, a press
// playing it on the screen that pressed, recorded as its video_play; without the seam the plain list stays
func TestVideoSearchTiles(t *testing.T) {
	data := `{"results":[{"id":"v1","title":"Лекция о космосе","channel":"Наука","durationS":3725},{"id":"v2","title":"Запуск ракеты","channel":"Роскосмос","live":true}]}`
	if got := (&cards.ToolCards{}).Subjects(result("t", "video_search", `{"query":"космос"}`, data, true)); len(got) != 1 {
		t.Fatalf("the list: %v", got)
	}
	tc := &cards.ToolCards{Images: func(u string) string {
		if u == "" {
			return ""
		}
		return "AbCdEfGhIjKlMnOpQrStUv02"
	}, Videos: fakeVideos{}}
	subs := tc.Subjects(result("t", "video_search", `{"query":"космос"}`, data, true))
	if len(subs) != 1 {
		t.Fatalf("%v", subs)
	}
	c := show(t, "ru", subs[0])
	goldenFixed(t, "video_search_tiles", c)
	list := c.GetRoot().GetCard().GetChildren()[0].GetPagedList()
	if len(list.GetItems()) != 2 {
		t.Fatalf("items %v", list.GetItems())
	}
	first := list.GetItems()[0].GetMediaTile()
	if first.GetImage().GetRef() != "img:AbCdEfGhIjKlMnOpQrStUv02" || first.GetImage().GetAspect() != cardsv1.Aspect_ASPECT_WIDE ||
		first.GetSubtitle() != "Наука · 1:02:05" || first.GetAction() == nil {
		t.Fatalf("first %v", first)
	}
	if second := list.GetItems()[1].GetMediaTile(); second.GetSubtitle() != "Роскосмос · Прямой эфир" {
		t.Fatalf("live %v", second)
	}
	// a result the seam cannot read keeps the whole card the list
	tc.Videos = fakeVideos{missing: true}
	if got := tc.Subjects(result("t", "video_search", `{"query":"космос"}`, data, true)); len(got) != 1 {
		t.Fatalf("the fallback: %v", got)
	}
}

// fakeVideos answers as the video tools' results do, with stills
type fakeVideos struct {
	missing bool
}

func (f fakeVideos) VideoResult(_, id string) (cards.VideoFound, bool) {
	if f.missing {
		return cards.VideoFound{}, false
	}
	switch id {
	case "v1":
		return cards.VideoFound{Title: "Лекция о космосе", Channel: "Наука", ArtworkURL: "https://i.ytimg.com/vi/a/hq.jpg", DurationS: 3725}, true
	case "v2":
		return cards.VideoFound{Title: "Запуск ракеты", Channel: "Роскосмос", ArtworkURL: "https://i.ytimg.com/vi/b/hq.jpg", Live: true}, true
	}
	return cards.VideoFound{}, false
}

func (f fakeVideos) VideoPlay(context.Context, string, string) error {
	return nil
}
