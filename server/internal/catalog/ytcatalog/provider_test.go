package ytcatalog_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/ytcatalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

type music struct{}

func (music) SearchMusic(_ context.Context, q string, _ int) ([]providers.Track, error) {
	return []providers.Track{{Source: providers.SourceYouTube, ID: "dQw4w9WgXcQ", VideoID: "dQw4w9WgXcQ", Title: q + " song",
		Artist: "Artist", DurationS: 212, ArtworkURL: "https://lh3.googleusercontent.com/a"}}, nil
}

func (music) SearchCollections(_ context.Context, kind, q string, _ int) ([]providers.Collection, error) {
	return []providers.Collection{{Source: providers.SourceYouTube, Kind: kind, ID: "MPREb_" + kind, Title: q + " " + kind}}, nil
}

func (music) CollectionSongs(_ context.Context, _ providers.Collection, _ int) ([]providers.Track, error) {
	return []providers.Track{{Source: providers.SourceYouTube, VideoID: "aaaaaaaaaaa", Title: "one"},
		{Source: providers.SourceYouTube, VideoID: "bbbbbbbbbbb", Title: "two"}}, nil
}

type player struct{}

func (player) Resolve(context.Context, string) (providers.Stream, error) {
	return providers.Stream{URL: "https://rr1.googlevideo.com/audio", DurationS: 212}, nil
}

func (player) ResolveScreen(_ context.Context, _ string, _ catalog.DeviceProfile) (ytdlp.Screen, error) {
	return ytdlp.Screen{Stream: catalog.Stream{URL: "https://rr1.googlevideo.com/v1080", AudioURL: "https://rr1.googlevideo.com/a",
		Renditions: []catalog.Rendition{{Height: 1080, URL: "https://rr1.googlevideo.com/v1080", AudioURL: "https://rr1.googlevideo.com/a"},
			{Height: 720, URL: "https://rr1.googlevideo.com/v720", AudioURL: "https://rr1.googlevideo.com/a"}}}}, nil
}

func (player) SearchVideos(_ context.Context, q string, _ int) ([]ytdlp.Video, error) {
	return []ytdlp.Video{{ID: "ccccccccccc", Title: q + " video", Channel: "Ch", DurationS: 600}}, nil
}

func relay(_ context.Context, dev string, ups []streams.Upstream) ([]string, error) {
	out := make([]string, len(ups))
	for i := range ups {
		out[i] = "https://cadence.example/v2/stream/tok-" + dev + "/" + strconv.Itoa(i)
	}
	return out, nil
}

func TestContract(t *testing.T) {
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Relay: relay})
	track := catalog.Key{Provider: catalog.ProviderYouTubeMusic, Source: ytcatalog.SourceMusic, Kind: catalog.KindTrack, ID: "dQw4w9WgXcQ"}
	catalogtest.Contract(t, p, catalogtest.Case{Query: "never", Item: track, Play: track})
	// with youtube itself
	p = ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Relay: relay, Web: &web{}})
	catalogtest.Contract(t, p, catalogtest.Case{Query: "never", Item: track, Play: track})
}

func TestAlbumsAndScreens(t *testing.T) {
	p := ytcatalog.New(ytcatalog.Config{Music: music{}, Player: player{}, Relay: relay})
	ctx := catalog.WithDevice(context.Background(), "tv")
	var album, video catalog.Key
	_ = p.Search(ctx, catalog.SearchQuery{Query: "x"}, func(b catalog.SearchBatch) error {
		for _, it := range b.Items {
			switch it.Kind {
			case catalog.KindAlbum:
				album = it.Key
			case catalog.KindVideo:
				video = it.Key
			}
		}
		return nil
	})
	d, err := p.Item(ctx, catalog.ItemRequest{Key: album})
	if err != nil || len(d.Children) != 2 {
		t.Fatalf("album %+v %v", d, err)
	}
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: video, Profile: catalog.DeviceProfile{Screen: true, MaxHeight: 1080}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Stream.Renditions) != 2 || !strings.Contains(pl.Stream.URL, "tok-tv/1") || !strings.HasSuffix(pl.Stream.AudioURL, "/0") ||
		strings.Contains(pl.Stream.Renditions[1].URL, "googlevideo") {
		t.Fatalf("screen %+v", pl.Stream)
	}
	pl, err = p.Resolve(ctx, catalog.ResolveRequest{Key: video})
	if err != nil || len(pl.Stream.Renditions) != 0 || !strings.HasSuffix(pl.Stream.URL, "/0") {
		t.Fatalf("speaker %+v %v", pl.Stream, err)
	}
}
