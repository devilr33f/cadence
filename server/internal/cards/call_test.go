package cards_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
)

// fakeMusic is the music tools' results and queue as the cards read them
type fakeMusic struct {
	mu      sync.Mutex
	results map[string]cards.MusicItem
	now     map[string]cards.MusicItem
	played  []string
	fail    error
}

func (f *fakeMusic) MusicResult(dev, id string) (cards.MusicItem, bool) {
	m, ok := f.results[dev+"/"+id]
	return m, ok
}

func (f *fakeMusic) MusicNow(dev string) (cards.MusicItem, bool) {
	m, ok := f.now[dev]
	return m, ok
}

func (f *fakeMusic) MusicPlay(_ context.Context, dev, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.played = append(f.played, dev+"/"+id)
	return f.fail
}

// the system of a down search of the owner's screenshot: an artist, a song, an album and a playlist
func soadMusic() *fakeMusic {
	art := func(n string) string { return "https://lh3.googleusercontent.com/" + n }
	return &fakeMusic{
		results: map[string]cards.MusicItem{
			"spk/r1": {Kind: "artist", Title: "System of a Down", ArtworkURL: art("soad")},
			"spk/r2": {Title: "Toxicity", Artist: "System of a Down", Album: "Toxicity", DurationS: 219, ArtworkURL: art("tox")},
			"spk/r3": {Kind: "album", Title: "Mezmerize", Artist: "System of a Down", TrackCount: 11, ArtworkURL: art("mez")},
			"spk/r4": {Kind: "playlist", Title: "System of a Down: лучшее", Owner: "YouTube Music", TrackCount: 49, ArtworkURL: art("best")},
		},
		now: map[string]cards.MusicItem{"spk": {Title: "Chop Suey!", Artist: "System of a Down", ArtworkURL: art("chop")}},
	}
}

const soadSearch = `{"results":[` +
	`{"id":"r1","kind":"artist","title":"System of a Down","provider":"YouTube Music"},` +
	`{"id":"r2","title":"Toxicity","artist":"System of a Down","album":"Toxicity","durationS":219,"provider":"YouTube Music"},` +
	`{"id":"r3","kind":"album","title":"Mezmerize","artist":"System of a Down","trackCount":11,"provider":"YouTube Music"},` +
	`{"id":"r4","kind":"playlist","title":"System of a Down: лучшее","owner":"YouTube Music","trackCount":49,"provider":"YouTube Music"}]}`

func musicCards(m *fakeMusic) *cards.ToolCards {
	return &cards.ToolCards{Music: m, Images: imageID}
}

// imageID is the image proxy's id of a fake cover, 24 characters as the screen takes them: the sim's painted fakes
// at each cover's real shape ("fakeart<seed>-<w>-<h>"), an artist photo as wide as the owner's tv showed it, a 4:3
// one, a tall one and square albums, so the music sim script shows each one square
func imageID(u string) string {
	shapes := map[string]string{"soad": "1-640-272", "tox": "2-400-400", "mez": "3-400-400", "best": "4-300-450", "chop": "5-480-360"}
	id := "fakeart" + shapes[u[strings.LastIndex(u, "/")+1:]]
	return id + strings.Repeat("_", 24-len(id))
}

func call(key, tool, args string) cards.Call {
	return cards.Call{Key: "turn-1:" + key, Tool: tool, DeviceID: "spk", Args: json.RawMessage(args)}
}

func ended(c cards.Call, ok bool, code, data string) cards.Call {
	r := cards.ToolResult{Key: c.Key, Tool: c.Tool, OK: ok, Code: code, Args: c.Args, At: at}
	if data != "" {
		r.Data = json.RawMessage(data)
	}
	c.Result = &r
	return c
}

// one card per call: none while a search runs (its line spins), then the results under the call's id; a play's
// media card from the moment it is called, replaced in place by the same id as it plays, its title the step's line
func TestMusicCallLifecycle(t *testing.T) {
	tc := musicCards(soadMusic())
	s := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
	ctx := context.Background()
	title := func(c cards.Call) string { return tc.Title(nil, "ru", c) }

	search := call("c1", "music_search", `{"query":"System of a Down","kind":"artist"}`)
	if subs, id := tc.CallSubjects(search); len(subs) != 0 || id != "" {
		t.Fatalf("a running search shows %d cards", len(subs))
	}
	if got := title(search); got != "Ищу музыку «System of a Down»" {
		t.Fatalf("running search %q", got)
	}
	search = ended(search, true, "", soadSearch)
	if got := title(search); got != "Ищу музыку · 4 результата" {
		t.Fatalf("done search %q", got)
	}
	subs, id := tc.CallSubjects(search)
	if len(subs) != 1 || id != cards.CallCardID(search.Key) || subs[0].ID() != id {
		t.Fatalf("search cards %d, id %q", len(subs), id)
	}
	found, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	goldenFixed(t, "call_music_search_done", proto.Clone(found).(*cardsv1.Card))
	tiles := found.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()
	want := []string{"Исполнитель", "System of a Down · 3:39", "System of a Down · 11 треков", "YouTube Music · 49 треков"}
	kinds := []string{"", "Трек", "Альбом", "Плейлист"}
	if len(tiles) != len(want) {
		t.Fatalf("%d tiles", len(tiles))
	}
	for i, n := range tiles {
		mt := n.GetMediaTile()
		kind := ""
		if len(mt.GetBadges()) > 0 {
			kind = mt.GetBadges()[0].GetText()
		}
		if mt.GetSubtitle() != want[i] || kind != kinds[i] || !strings.HasPrefix(mt.GetImage().GetRef(), "img:fakeart") || mt.GetAction().GetId() == "" {
			t.Fatalf("tile %d: %v", i, mt)
		}
	}

	play := call("c2", "music_play", `{"id":"r2"}`)
	subs, id = tc.CallSubjects(play)
	if len(subs) != 1 || id != cards.CallCardID(play.Key) {
		t.Fatalf("pending play: %d cards, id %q", len(subs), id)
	}
	pending, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_pending", pending)
	b := pending.GetRoot().GetCard()
	if b.GetTitle() != "Загружаю «Toxicity · System of a Down»" || b.GetTitle() != title(play) {
		t.Fatalf("pending title %q, line %q", b.GetTitle(), title(play))
	}
	if b.GetChildren()[1].GetProgress() == nil || b.GetChildren()[0].GetRow().GetChildren()[0].GetImage().GetRef() != "img:"+imageID("/tox") {
		t.Fatalf("pending card %v", b)
	}

	play = ended(play, true, "", `{"playing":true}`)
	subs, id = tc.CallSubjects(play)
	playing, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_done", playing)
	if playing.GetId() != pending.GetId() || playing.GetRevision() != pending.GetRevision()+1 || id != pending.GetId() {
		t.Fatalf("the play's card is not replaced in place: %s r%d, then %s r%d", pending.GetId(), pending.GetRevision(), playing.GetId(), playing.GetRevision())
	}
	b = playing.GetRoot().GetCard()
	if b.GetTitle() != "Играет «Toxicity · System of a Down»" || b.GetTitle() != title(play) {
		t.Fatalf("done title %q", b.GetTitle())
	}
	for _, n := range b.GetChildren() {
		if n.GetProgress() != nil {
			t.Fatal("the done card still spins")
		}
	}

	failed := ended(call("c2", "music_play", `{"id":"r2"}`), false, "unplayable", "")
	subs, _ = tc.CallSubjects(failed)
	bad, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_failed", bad)
	b = bad.GetRoot().GetCard()
	if b.GetTitle() != "Не включилось «Toxicity · System of a Down» · не воспроизводится" ||
		b.GetChildren()[len(b.GetChildren())-1].GetState().GetLabel() != "не воспроизводится" {
		t.Fatalf("failed card %v", b)
	}
}

// a playlist plays as its name, its owner and track count, the song it plays now and that song's cover: what the
// owner's «Музыка · Играет · Треков: 49» never said
func TestMusicPlaylistSaysWhatPlays(t *testing.T) {
	tc := musicCards(soadMusic())
	c := ended(call("c4", "music_play", `{"id":"r4"}`), true, "", `{"playing":true,"songs":49}`)
	subs, _ := tc.CallSubjects(c)
	card := show(t, "ru", subs[0])
	golden(t, "call_music_playlist_done", card)
	b := card.GetRoot().GetCard()
	if b.GetTitle() != "Играет «System of a Down: лучшее · YouTube Music»" {
		t.Fatalf("title %q", b.GetTitle())
	}
	row := b.GetChildren()[0].GetRow()
	if row.GetChildren()[0].GetImage().GetRef() != "img:"+imageID("/chop") {
		t.Fatalf("cover %v", row.GetChildren()[0])
	}
	var lines []string
	for _, n := range row.GetChildren()[1].GetColumn().GetChildren() {
		lines = append(lines, n.GetText().GetText())
	}
	if strings.Join(lines, "|") != "System of a Down: лучшее|YouTube Music|Плейлист · 49 треков|Сейчас: Chop Suey! — System of a Down" {
		t.Fatalf("lines %q", lines)
	}
}

// a tile's press plays its result on the turn's device, without the model, and is kept as a music_play
func TestMusicTilePlays(t *testing.T) {
	m := soadMusic()
	tc := musicCards(m)
	s := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
	subs, _ := tc.CallSubjects(ended(call("c1", "music_search", `{"query":"soad"}`), true, "", soadSearch))
	c, err := s.Show(context.Background(), "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	tile := c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[3].GetMediaTile()
	if rep := act(t, s, "tv", c, tile.GetAction().GetId()); rep.Status != rpc.StatusOK {
		t.Fatalf("press: %v", rep)
	}
	m.fail = errors.New("unplayable")
	// the press drew the card again: its next revision's action
	c, _ = s.Card("tv", c.GetId())
	tile = c.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[3].GetMediaTile()
	if rep := act(t, s, "tv", c, tile.GetAction().GetId()); rep.Status == rpc.StatusOK {
		t.Fatalf("a failed play answered ok: %v", rep)
	}
	if strings.Join(m.played, ",") != "spk/r4,spk/r4" {
		t.Fatalf("played %v", m.played)
	}
}

// without covers or the music tools, the results stay a list, each line what it is and whose, never the provider
func TestMusicSearchWithoutCovers(t *testing.T) {
	c := ended(call("c1", "music_search", `{"query":"soad"}`), true, "", soadSearch)
	subs, id := (&cards.ToolCards{}).CallSubjects(c)
	if len(subs) != 1 || id != cards.CallCardID(c.Key) {
		t.Fatalf("%d cards, id %q", len(subs), id)
	}
	b, _ := json.Marshal(show(t, "ru", subs[0]))
	if strings.Contains(string(b), "YouTube Music\"") && !strings.Contains(string(b), "YouTube Music · Плейлист · 49 треков") {
		t.Fatalf("the provider on a line: %s", b)
	}
}

// every built-in tool has a line in each phase, in ru and en, and none is a catalog key; a tool with a pending card
// replaces it with its reason, one without shows none for a failure (its line says it)
func TestEveryToolHasItsLines(t *testing.T) {
	tc := &cards.ToolCards{}
	for _, tool := range append(cards.ToolNames(), "catalog_search", "notes_add") {
		for _, lang := range []string{"ru", "en"} {
			for _, c := range []cards.Call{
				call("x", tool, `{}`), ended(call("x", tool, `{}`), true, "", `{}`), ended(call("x", tool, `{}`), false, "timeout", ""),
			} {
				got := tc.Title(nil, lang, c)
				if got == "" || keyLike.MatchString(got) || strings.Contains(got, "{") {
					t.Errorf("%s %s phase %d: %q", tool, lang, c.Phase(), got)
				}
			}
		}
	}
	for _, tool := range []string{"video_play", "catalog_play", "music_play"} {
		c := call("p", tool, `{}`)
		pend, pid := tc.CallSubjects(c)
		fail, fid := tc.CallSubjects(ended(c, false, "device_offline", ""))
		if len(pend) != 1 || len(fail) != 1 || pid != fid || pid != cards.CallCardID(c.Key) {
			t.Fatalf("%s: pending %d (%s), failed %d (%s)", tool, len(pend), pid, len(fail), fid)
		}
		st := show(t, "ru", fail[0]).GetRoot().GetCard()
		if st.GetChildren()[len(st.GetChildren())-1].GetState().GetKind() != cardsv1.StateKind_STATE_KIND_UNAVAILABLE {
			t.Fatalf("%s failure %v", tool, st)
		}
	}
	// the chat's cards stream too: a pending card from the call on, its reason in its place once it failed
	for _, tool := range []string{"weather", "timer", "alarm", "device_volume", "do_not_disturb", "agent_mode", "media_control", "ha_control"} {
		c := call("w", tool, `{}`)
		pend, pid := tc.CallSubjects(c)
		fail, fid := tc.CallSubjects(ended(c, false, "timeout", ""))
		if len(pend) != 1 || len(fail) != 1 || pid != fid || pid != cards.CallCardID(c.Key) {
			t.Fatalf("%s: pending %d, failed %d", tool, len(pend), len(fail))
		}
		if show(t, "ru", pend[0]).GetRoot().GetCard().GetChildren()[0].GetProgress() == nil {
			t.Fatalf("%s: the pending card does not spin", tool)
		}
	}
	// a search that finds nothing has no card of its own: its line says so
	if subs, _ := tc.CallSubjects(ended(call("w", "web_search", `{}`), false, "timeout", "")); len(subs) != 0 {
		t.Fatal("a failed search shows a card")
	}
	// the generic card of an MCP tool keeps its own title, under the call's id
	if subs, id := tc.CallSubjects(ended(call("g", "notes_add", `{}`), false, "timeout", "")); len(subs) != 1 || id != cards.CallCardID("turn-1:g") ||
		show(t, "ru", subs[0]).GetRoot().GetCard().GetTitle() != "notes_add" {
		t.Fatalf("generic %d %q", len(subs), id)
	}
	// a timer's card takes the call's line as its title
	c := ended(call("t", "timer", `{"action":"set","seconds":600}`), true, "", `{"timer":{"seconds":600,"remainingS":600}}`)
	subs, id := tc.CallSubjects(c)
	if len(subs) != 1 || id != cards.CallCardID(c.Key) || show(t, "ru", subs[0]).GetRoot().GetCard().GetTitle() != tc.Title(nil, "ru", c) {
		t.Fatalf("timer %d %q", len(subs), id)
	}
	if got := tc.Title(nil, "ru", call("t", "timer", `{"action":"set","seconds":600}`)); got != "Ставлю таймер на 10 мин" {
		t.Fatalf("timer line %q", got)
	}
}

// liveMusic is fakeMusic with the queue's session: how it plays and how far it buffered, and a wake on each change
type liveMusic struct {
	*fakeMusic
	pb   cards.MusicPlayback
	has  bool
	wake chan struct{}
}

func (l *liveMusic) MusicPlayback(dev string) (cards.MusicPlayback, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pb, l.has && dev == "spk"
}

func (l *liveMusic) MusicChanged(ctx context.Context) bool {
	l.mu.Lock()
	w := l.wake
	l.mu.Unlock()
	select {
	case <-w:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *liveMusic) set(pb cards.MusicPlayback, now *cards.MusicItem) {
	l.mu.Lock()
	l.pb, l.has = pb, true
	if now != nil {
		l.now["spk"] = *now
	}
	close(l.wake)
	l.wake = make(chan struct{})
	l.mu.Unlock()
}

// a play's card loads with the player's buffering (how full, how far ahead), then plays; once the song ended its
// radio's next one takes the same card, in place: its title, artist, cover and length
func TestMusicPlayCardFollowsBufferingAndTheNextSong(t *testing.T) {
	m := &liveMusic{fakeMusic: soadMusic(), wake: make(chan struct{})}
	m.now["spk"] = m.results["spk/r2"]
	tc := &cards.ToolCards{Music: m, Images: imageID}
	s := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
	ctx := context.Background()
	running := call("c5", "music_play", `{"id":"r2"}`)
	subs, _ := tc.CallSubjects(running)
	pending, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_loading", pending)
	m.set(cards.MusicPlayback{State: "buffering", BufferingPercent: 40, BufferedMs: 12_000}, nil)
	play := ended(running, true, "", `{"playing":true}`)
	subs, id := tc.CallSubjects(play)
	live, ok := subs[0].(cards.Live)
	if !ok || id != cards.CallCardID(play.Key) || id != pending.GetId() {
		t.Fatalf("the play's card is not live: %T %s", subs[0], id)
	}
	loading, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_buffering", loading)
	p := loading.GetRoot().GetCard().GetChildren()[1].GetProgress()
	if p.GetFraction() != 0.4 || p.GetLabel() != "Загрузка 40 % · в буфере 12 с" {
		t.Fatalf("buffering %v", p)
	}
	next := cards.MusicItem{Title: "Chop Suey!", Artist: "System of a Down", Album: "Toxicity", DurationS: 210,
		ArtworkURL: "https://lh3.googleusercontent.com/chop"}
	go m.set(cards.MusicPlayback{State: "playing", BufferingPercent: -1, BufferedMs: 90_000}, &next)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !live.Changed(cctx) {
		t.Fatal("no change for the next song")
	}
	playing, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_music_play_next", playing)
	b := playing.GetRoot().GetCard()
	row := b.GetChildren()[0].GetRow()
	if playing.GetId() != loading.GetId() || playing.GetRevision() != loading.GetRevision()+1 ||
		b.GetTitle() != "Играет «Chop Suey! · System of a Down»" || row.GetChildren()[0].GetImage().GetRef() != "img:"+imageID("/chop") ||
		row.GetChildren()[1].GetColumn().GetChildren()[0].GetText().GetText() != "Chop Suey!" ||
		b.GetChildren()[1].GetState().GetLabel() != "Играет" {
		t.Fatalf("next song: %s r%d %v", playing.GetId(), playing.GetRevision(), b)
	}
	// a skip by voice is the same: its card is the song it plays
	skip := ended(call("c3", "media_control", `{"action":"next"}`), true, "", `{"ok":true}`)
	if got := tc.Title(nil, "ru", skip); got != "Играет «Chop Suey! · System of a Down»" {
		t.Fatalf("skip line %q", got)
	}
	if subs, _ := tc.CallSubjects(skip); len(subs) != 1 {
		t.Fatal("no card for the skip")
	} else if _, ok := subs[0].(cards.Live); !ok {
		t.Fatalf("the skip's card is not the live song: %T", subs[0])
	}
	if got := tc.Title(nil, "ru", call("c3", "media_control", `{"action":"next"}`)); got != "Включаю следующий трек" {
		t.Fatalf("skip running %q", got)
	}
}

// a chat card streams as the showcase's do: the volume's pending card from the call on, then its result in place
func TestChatCardsStreamInPlace(t *testing.T) {
	tc := &cards.ToolCards{}
	s := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
	ctx := context.Background()
	c := call("v1", "device_volume", `{"level":60}`)
	subs, id := tc.CallSubjects(c)
	pending, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_volume_pending", pending)
	subs, _ = tc.CallSubjects(ended(c, true, "", `{"level":60}`))
	set, err := s.Show(ctx, "tv", subs[0])
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "call_volume_done", set)
	if pending.GetId() != id || set.GetId() != id || set.GetRevision() != pending.GetRevision()+1 ||
		set.GetRoot().GetCard().GetTitle() != "Громкость 60 %" || set.GetRoot().GetCard().GetChildren()[0].GetMeter() == nil {
		t.Fatalf("volume: %v then %v", pending, set)
	}
	// a live card of its own (a task's, an entity's) takes the call's id too, and stays live
	live := &cards.ToolCards{Task: func(string) cards.Subject { return liveStub{} }}
	c = call("t1", "task_start", `{}`)
	subs, id = live.CallSubjects(ended(c, true, "", `{"id":"ab12"}`))
	if len(subs) != 1 || id != cards.CallCardID(c.Key) || subs[0].ID() != id {
		t.Fatalf("task: %d %s", len(subs), id)
	}
	if _, ok := subs[0].(cards.Live); !ok {
		t.Fatalf("the task's card lost its liveness: %T", subs[0])
	}
}

type liveStub struct{}

func (liveStub) ID() string                       { return "task-ab12" }
func (liveStub) Changed(ctx context.Context) bool { <-ctx.Done(); return false }
func (liveStub) Render(context.Context, *cards.Render) (*cardsv1.Card, error) {
	return pcards.NewCard("", 0, "Собрать отчёт", &cardsv1.CardBlock{Title: "Собрать отчёт", Icon: "task"}), nil
}

// an episode's card names its show and the episode under it, and a series' card lists its episodes by number and
// name, never as bare numbers
func TestCatalogCardsNameTheShowAndEpisodes(t *testing.T) {
	tc := &cards.ToolCards{}
	ep := ended(call("e1", "catalog_item", `{"ref":"c1"}`), true, "",
		`{"ref":"c1","kind":"episode","title":"1","episode":{"label":"1","name":"Конец приключения"},"series":"Провожающая в последний путь Фрирен 2 сезон"}`)
	subs, _ := tc.CallSubjects(ep)
	b := show(t, "ru", subs[0]).GetRoot().GetCard()
	if b.GetTitle() != "Провожающая в последний путь Фрирен 2 сезон" || b.GetSubtitle() != "Серия 1 · Конец приключения" {
		t.Fatalf("episode card %q / %q", b.GetTitle(), b.GetSubtitle())
	}
	series := ended(call("e2", "catalog_item", `{"ref":"c2"}`), true, "",
		`{"ref":"c2","kind":"series","title":"Фрирен 2 сезон","children":[{"title":"1","episode":{"label":"1","name":"Конец приключения"}},{"title":"2","episode":{"label":"2"}}]}`)
	subs, _ = tc.CallSubjects(series)
	c := show(t, "ru", subs[0])
	golden(t, "call_catalog_series", c)
	var lines []string
	for _, n := range c.GetRoot().GetCard().GetChildren()[0].GetColumn().GetChildren() {
		lines = append(lines, n.GetColumn().GetChildren()[0].GetText().GetText())
	}
	if strings.Join(lines, "|") != "Серия 1 · Конец приключения|Серия 2" {
		t.Fatalf("episodes %q", lines)
	}
	// a play's card never takes a bare episode number for its title
	play := show(t, "ru", cards.NewCatalogPlay("p", cards.CatalogPlaying{Title: "1", Episode: "1", EpisodeName: "Конец приключения"})).GetRoot().GetCard()
	if play.GetTitle() == "1" || play.GetSubtitle() != "Серия 1 · Конец приключения" {
		t.Fatalf("play card %q / %q", play.GetTitle(), play.GetSubtitle())
	}
}
