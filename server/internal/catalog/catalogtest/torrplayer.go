package catalogtest

import "git.shino.boo/devilreef/cadence/server/internal/catalog"

// keys of the Torrplayer fake
var (
	FrierenAniLiberty = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty",
		Kind: catalog.KindSeries, ID: "t-frieren"}
	FrierenAnime365 = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "anime365",
		Kind: catalog.KindSeries, ID: "t-9931"}
	FrierenEpisode5 = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty",
		Kind: catalog.KindEpisode, ID: "t-frieren", Episode: "5"}
	FrierenRecap = catalog.Key{Provider: catalog.ProviderTorrplayer, Source: "aniliberty",
		Kind: catalog.KindEpisode, ID: "t-frieren", Episode: "5.5"}
)

// Torrplayer is a fake shaped like torrplayer: three sources, of which
// animego fails and nyaa holds torrents; one title on two sources with the
// same Shikimori id (merged into a title group by catalog.GroupKey), its
// episodes with a fractional recap, and an episode's voiceover, quality and
// torrent groups, one voiceover without seeders.
func Torrplayer() *Fake {
	f := &Fake{
		Provider: catalog.ProviderTorrplayer,
		Caps: catalog.Capabilities{catalog.CapSearch, catalog.CapBrowse, catalog.CapItem, catalog.CapOptions,
			catalog.CapResolve, catalog.CapAct, catalog.CapReport},
		SourceList: []catalog.SourceInfo{
			{ID: "aniliberty", Name: "AniLiberty", Enabled: true},
			{ID: "anime365", Name: "anime365", Enabled: true},
			{ID: "animego", Name: "AnimeGo", Enabled: true},
			{ID: "nyaa", Name: "Nyaa", Enabled: false, Torrents: true},
		},
		Fail: map[catalog.SourceID]catalog.FailureCode{"animego": catalog.FailTimeout},
	}
	show := func(aired int) *catalog.ShowInfo {
		return &catalog.ShowInfo{
			Names: catalog.Names{RU: "Провожающая в последний путь Фрирен", EN: "Frieren: Beyond Journey's End"},
			Year:  2023, Format: "TV", EpisodesAired: aired, EpisodesTotal: 28, Status: "released",
			Season: "autumn", Genres: []string{"adventure", "fantasy"}, Score: 9.3,
			IDs: catalog.ExternalIDs{Shikimori: "52991", MAL: "52991"},
		}
	}
	episode := func(label string, ordinal float64, typ string) catalog.Key {
		k := FrierenAniLiberty
		k.Kind, k.Episode = catalog.KindEpisode, label
		f.Add(Entry{
			Item: catalog.Item{Key: k, Title: "Фрирен, серия " + label,
				Episode: &catalog.EpisodeInfo{Label: label, Ordinal: ordinal, Type: typ}, DurationS: 1440},
			Groups: []catalog.ChoiceGroup{
				{Kind: catalog.GroupVoiceover, Label: "Озвучка", Hidden: 2, Choices: []catalog.Choice{
					{ID: "anilibria", Label: "AniLibria", Badges: []catalog.Badge{{Kind: catalog.BadgeVoice}},
						Stream: &catalog.StreamSource{Translator: "AniLibria", TransType: catalog.TransVoice, Language: "ru", Quality: 1080}},
					{ID: "subs", Label: "Субтитры", Badges: []catalog.Badge{{Kind: catalog.BadgeSub}},
						Stream: &catalog.StreamSource{Translator: "Crunchyroll", TransType: catalog.TransSub, Language: "ru", Quality: 720}},
					{ID: "dead", Label: "AniDUB", Unavailable: true, Reason: catalog.FailNoSeeders,
						Torrent: &catalog.TorrentSource{Episodes: "1-28", SizeBytes: 30 << 30, Quality: 1080, Codec: "hevc"}},
				}},
				{Kind: catalog.GroupQuality, Label: "Качество", Choices: []catalog.Choice{
					{ID: "1080", Label: "1080p"}, {ID: "720", Label: "720p"},
				}},
			},
			Play: &catalog.Playable{Kind: catalog.PlayStream, Stream: &catalog.Stream{
				URL:        "http://torrplayer.lan/resolve/r1",
				Subtitles:  []catalog.Subtitle{{URL: "http://torrplayer.lan/sub/r1.ass", Language: "ru", Format: "ass"}},
				Skips:      []catalog.Skip{{Kind: catalog.SkipOpening, StartMS: 90_000, EndMS: 180_000}},
				ResumeMS:   300_000,
				DurationMS: 1_440_000,
			}},
		})
		return k
	}
	eps := []catalog.Key{episode("1", 1, "tv"), episode("5", 5, "tv"), episode("5.5", 5.5, "recap")}
	f.Add(Entry{
		Item:     catalog.Item{Key: FrierenAniLiberty, Title: "Провожающая в последний путь Фрирен", Show: show(28)},
		Children: eps,
		Continue: &catalog.ContinueAt{Episode: "5", PositionS: 300},
		Actions: []catalog.Action{
			{ID: "my_list", Kind: catalog.ActionMyList, Label: "Мой список", Enabled: true,
				Values: []string{"watching", "planned", "completed", "on_hold", "dropped"}},
			{ID: "subscribe", Kind: catalog.ActionSubscribe, Label: "Подписаться", Enabled: true},
		},
	})
	f.Add(Entry{
		Item: catalog.Item{Key: FrierenAnime365, Title: "Frieren: Beyond Journey's End", Show: show(26)},
	})
	f.Add(Entry{
		Item: catalog.Item{Key: catalog.Key{Source: "anime365", Kind: catalog.KindMovie, ID: "t-77"},
			Title: "Кольцо Фрирен (спешл)", Unavailable: true},
	})
	f.Feeds = map[string][]catalog.Key{catalog.FeedContinue: {FrierenAniLiberty}}
	return f
}
