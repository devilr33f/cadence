package cards_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

const poster = "AbCdEfGhIjKlMnOpQrStUv01"

var found = []cards.CatalogTile{
	{Ref: "c1", Title: "Поднятие уровня в одиночку", Kind: "tv", Year: 2024, Episodes: 12, Score: 8.3, Artwork: poster},
	{Ref: "c2", Title: "Поднятие уровня в одиночку 2", Kind: "tv", Year: 2025, Episodes: 13, Score: 8.6},
	{Ref: "c3", Title: "Solo Leveling: ReAwakening", Kind: "movie", Year: 2024, Score: 7.1},
	{Ref: "c5", Title: "Solo Leveling: Arise", Kind: "ona", Year: 2025, Unavailable: true},
	// a title of no words is left out
	{Ref: "c4", Title: "  "},
}

// a catalog_search's titles are poster tiles through the image proxy (a content card: the showcase's), each with
// its year, its episodes (a movie its kind) and its score; OK on one opens its page on the screen that pressed it
func TestCatalogFoundIsPosterTiles(t *testing.T) {
	var opened []string
	open := func(_ context.Context, dev, ref string) error {
		opened = append(opened, dev+" "+ref)
		return nil
	}
	if cards.CatalogFound("turn-1:s1", found[4:], open) != nil {
		t.Fatal("a card without a title")
	}
	subj := cards.CatalogFound("turn-1:s1", found, open)
	c := show(t, "ru", subj)
	list := c.GetRoot().GetCard().GetChildren()[0].GetPagedList()
	if len(list.GetItems()) != 4 {
		t.Fatalf("items %v", list.GetItems())
	}
	first := list.GetItems()[0].GetMediaTile()
	if first.GetImage().GetRef() != "img:"+poster || first.GetImage().GetAspect() != cardsv1.Aspect_ASPECT_POSTER ||
		first.GetSubtitle() != "2024 · 12 эп. · ★8,3" || first.GetAction() == nil {
		t.Fatalf("first %v", first)
	}
	if third := list.GetItems()[2].GetMediaTile(); third.GetSubtitle() != "2024 · Фильм · ★7,1" {
		t.Fatalf("a movie %v", third)
	}
	if fourth := list.GetItems()[3].GetMediaTile(); fourth.GetImage().GetRef() != "img:none" || fourth.GetSubtitle() != "2025 · нет источника" {
		t.Fatalf("unavailable %v", fourth)
	}
	if en := show(t, "en", subj); en.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[0].GetMediaTile().GetSubtitle() != "2024 · 12 ep. · ★8.3" {
		t.Fatalf("en %v", en)
	}
	goldenFixed(t, "catalog_found", c)
}

func playing() cards.CatalogPlaying {
	return cards.CatalogPlaying{Title: "Поднятие уровня в одиночку", Artwork: poster, Episode: "3", EpisodeName: "Я привык к этому",
		Provider: "torrplayer", Source: "anime365", Voiceover: "AniLibria", Quality: "1080p", Screen: "Bedroom TV"}
}

// a catalog_play's card names what plays where (title, episode and its name, provider and source, voiceover,
// quality, the screen) and follows the screen: starting, loading, playing, or failed; a played or failed play
// stays so
func TestCatalogPlayFollowsTheScreen(t *testing.T) {
	play := cards.NewCatalogPlay("turn-1:s2", playing())
	if _, live := any(play).(cards.Live); !live {
		t.Fatal("not live")
	}
	// one screen's service: the playing card is the starting one's next revision
	svc := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }})
	again := func() *cardsv1.Card {
		t.Helper()
		c, err := svc.Show(context.Background(), "tv-1", play)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := again()
	golden(t, "catalog_play_starting", c)
	b := c.GetRoot().GetCard()
	if b.GetTitle() != "Поднятие уровня в одиночку" || b.GetSubtitle() != "Серия 3 · Я привык к этому" {
		t.Fatalf("block %v", b)
	}
	if fb := c.GetFallbackText(); fb != "Поднятие уровня в одиночку · Серия 3 · Я привык к этому · Включаю — Bedroom TV" {
		t.Fatalf("fallback %q", fb)
	}
	var text []string
	shownText(c.ProtoReflect(), &text)
	for _, want := range []string{"Bedroom TV", "torrplayer · anime365", "AniLibria", "1080p", "Включаю"} {
		if !strings.Contains(strings.Join(text, "\n"), want) {
			t.Fatalf("card lacks %q: %v", want, text)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	changed := make(chan bool, 1)
	go func() { changed <- play.Changed(ctx) }()
	time.Sleep(10 * time.Millisecond)
	if !play.Set(cards.PlayBuffering) || !<-changed {
		t.Fatal("buffering is a change")
	}
	if play.Set(cards.PlayBuffering) {
		t.Fatal("the same state again")
	}
	play.Set(cards.PlayPlaying)
	pc := again()
	if pc.GetRevision() <= c.GetRevision() || pc.GetId() != c.GetId() {
		t.Fatalf("revision %d after %d", pc.GetRevision(), c.GetRevision())
	}
	golden(t, "catalog_play_playing", pc)
	if play.Set(cards.PlayFailed) || play.State() != cards.PlayPlaying {
		t.Fatal("a played play failed later")
	}

	failed := cards.NewCatalogPlay("turn-1:s3", cards.CatalogPlaying{Title: "Тьма", Episode: "1", Screen: "Bedroom TV"})
	failed.Set(cards.PlayFailed)
	fc := show(t, "ru", failed)
	if st := fc.GetRoot().GetCard().GetChildren()[0].GetState(); st.GetLabel() != "Не включилось" || st.GetKind() != cardsv1.StateKind_STATE_KIND_UNAVAILABLE {
		t.Fatalf("failed %v", fc)
	}
	golden(t, "catalog_play_failed", fc)

	quiet, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if play.Changed(quiet) || !errors.Is(quiet.Err(), context.DeadlineExceeded) {
		t.Fatal("a change with nothing new")
	}
}

// a catalog_play's card carries its fetch from its call on: what was cached of the ref under a bar, the resolve
// stages setting the bar's share and its word, then what started on the same card, or the call's code in words
func TestCatalogPlayFetches(t *testing.T) {
	fetch := cards.NewCatalogFetch("turn-1:s4", cards.CatalogPlaying{Title: "Поднятие уровня в одиночку", Artwork: poster,
		Screen: "Bedroom TV"})
	c := show(t, "ru", fetch)
	golden(t, "catalog_play_fetching", c)
	var text []string
	shownText(c.ProtoReflect(), &text)
	joined := strings.Join(text, "\n")
	for _, want := range []string{"Поднятие уровня в одиночку", "Bedroom TV", "Включаю"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the fetch lacks %q: %v", want, text)
		}
	}
	// the fetch's bar runs while no stage came
	if bar := c.GetRoot().GetCard().GetChildren()[1].GetProgress(); bar.GetAmount().(*cardsv1.Progress_Indeterminate) == nil {
		t.Fatalf("no bar: %v", c.GetRoot().GetCard())
	}

	// a resolve stage sets its word and the bar's share
	if !fetch.Stage(playprogress.Event{Stage: playprogress.StageFormats}) {
		t.Fatal("a stage is a change")
	}
	sc := show(t, "ru", fetch)
	if sc.GetId() != c.GetId() {
		t.Fatalf("the staged card %v after %v", sc.GetId(), c.GetId())
	}
	shownText(sc.ProtoReflect(), &text)
	if !strings.Contains(strings.Join(text, "\n"), "Читаю форматы") {
		t.Fatalf("the stage's word is missing: %v", text)
	}
	if bar := sc.GetRoot().GetCard().GetChildren()[1].GetProgress(); bar.GetAmount().(*cardsv1.Progress_Fraction).Fraction != 0.55 {
		t.Fatalf("the bar's share: %v", bar.GetAmount())
	}

	// what started replaces the fetch on the same card, with the play's own details
	fetch.Start(playing())
	st := show(t, "ru", fetch)
	if st.GetId() != c.GetId() || st.GetRoot().GetCard().GetSubtitle() != "Серия 3 · Я привык к этому" {
		t.Fatalf("started %v", st)
	}
	if fetch.Stage(playprogress.Event{Stage: playprogress.StageRelay}) || fetch.State() != cards.PlayStarting {
		t.Fatal("a stage after it started")
	}

	// a fetch that fails names the call's code in words
	lost := cards.NewCatalogFetch("turn-1:s5", cards.CatalogPlaying{Title: "Тьма", Screen: "Bedroom TV"})
	lost.Fail("unknown_ref")
	lc := show(t, "ru", lost)
	if st := lc.GetRoot().GetCard().GetChildren()[0].GetState(); st.GetLabel() != "ссылка устарела" {
		t.Fatalf("failed %v", lc)
	}
	golden(t, "catalog_play_fetch_failed", lc)
}

// every built-in tool's step has its own words on a screen: none reads the plain «Работаю»
func TestEveryBuiltinToolHasItsStepLabel(t *testing.T) {
	for _, name := range cards.ToolNames() {
		if name == "subscription_updates" || strings.HasPrefix(name, "task") {
			continue
		}
		for _, lang := range []string{"ru", "en"} {
			key := "step." + name
			if l := cards.Builtin.Format(lang, key, nil); l == key || l == "" {
				t.Errorf("%s: no %s label", lang, key)
			}
		}
	}
}

// a youtube search's card is its videos as wide tiles — OK on one playing it on the screen that pressed — with its
// channels and playlists as lines below, counts in words
func TestVideosFoundIsWideTiles(t *testing.T) {
	var played []string
	open := func(_ context.Context, dev, ref string) error {
		played = append(played, dev+" "+ref)
		return nil
	}
	if cards.VideosFound("turn-1:s6", nil, nil, open) != nil {
		t.Fatal("a card without anything")
	}
	subj := cards.VideosFound("turn-1:s6", []cards.VideoTile{
		{Ref: "v1", Title: "YT Probably Stolen 9", Channel: "Купли Барамидзе", Views: 133000, DurationS: 2760, Artwork: poster},
		{Ref: "v2", Title: "Прямой эфир", Channel: "Купли Барамидзе", Live: true},
		// a tile of no title is left out
		{Ref: "v3"},
	}, []cards.VideoRow{
		{Title: "Купли Барамидзе", Handle: "@kuplinov", Subscribers: 3900000, Videos: 2400},
	}, open)
	c := show(t, "ru", subj)
	goldenFixed(t, "videos_found", c)
	list := c.GetRoot().GetCard().GetChildren()[0].GetPagedList()
	if len(list.GetItems()) != 2 {
		t.Fatalf("items %v", list.GetItems())
	}
	first := list.GetItems()[0].GetMediaTile()
	if first.GetImage().GetRef() != "img:"+poster || first.GetImage().GetAspect() != cardsv1.Aspect_ASPECT_WIDE ||
		first.GetSubtitle() != "Купли Барамидзе · 133 тыс. просмотров · 46:00" || first.GetAction() == nil {
		t.Fatalf("first %v", first)
	}
	if second := list.GetItems()[1].GetMediaTile(); second.GetImage().GetRef() != "img:none" ||
		second.GetSubtitle() != "Купли Барамидзе · Прямой эфир" {
		t.Fatalf("live %v", second)
	}
	// the channels are lines below the tiles
	var text []string
	shownText(c.ProtoReflect(), &text)
	if joined := strings.Join(text, "\n"); !strings.Contains(joined, "@kuplinov · 3,9 млн подписчиков · 2,4 тыс. видео") {
		t.Fatalf("the channel line: %v", text)
	}
	if en := show(t, "en", subj); en.GetRoot().GetCard().GetChildren()[0].GetPagedList().GetItems()[0].GetMediaTile().GetSubtitle() != "Купли Барамидзе · 133K views · 46:00" {
		t.Fatalf("en %v", en)
	}
	goldenFixed(t, "videos_found_en", show(t, "en", subj))
}
