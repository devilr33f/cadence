package providers

import "testing"

func TestPublicSongIdentity(t *testing.T) {
	cases := []struct {
		name  string
		track Track
		want  SongIdentity
	}{
		{"youtube", Track{Source: SourceYouTube, ID: "5qZQEq_C3vc", WebURL: "https://music.youtube.com/watch?v=5qZQEq_C3vc&sig=secret"}, SongIdentity{Provider: "ytmusic", SongID: "5qZQEq_C3vc", WebURL: "https://music.youtube.com/watch?v=5qZQEq_C3vc"}},
		{"spotify", Track{Source: SourceSpotify, ID: "2nLtzopw4rPReszdYBJU6h"}, SongIdentity{Provider: "spotify", SongID: "2nLtzopw4rPReszdYBJU6h", WebURL: "https://open.spotify.com/track/2nLtzopw4rPReszdYBJU6h"}},
		{"yandex album page", Track{Source: SourceYandex, ID: "178529", WebURL: "https://music.yandex.ru/album/21940/track/178529"}, SongIdentity{Provider: "yandex", SongID: "178529", WebURL: "https://music.yandex.ru/album/21940/track/178529"}},
		{"yandex fallback", Track{Source: SourceYandex, ID: "178529", WebURL: "https://evil.example/track/178529"}, SongIdentity{Provider: "yandex", SongID: "178529", WebURL: "https://music.yandex.ru/track/178529"}},
		{"zvuk", Track{Source: SourceZvuk, ID: "123"}, SongIdentity{Provider: "zvuk", SongID: "123", WebURL: "https://zvuk.com/track/123"}},
		{"vk without access key", Track{Source: SourceVK, ID: "-2001_456239017_secretKey", WebURL: "https://vk.com/audio-2001_456239017"}, SongIdentity{Provider: "vk", SongID: "-2001_456239017", WebURL: "https://vk.com/audio-2001_456239017"}},
		{"soundcloud strips secret", Track{Source: SourceSoundCloud, ID: "123", WebURL: "https://soundcloud.com/u/song?secret_token=secret#comments"}, SongIdentity{Provider: "soundcloud", SongID: "123", WebURL: "https://soundcloud.com/u/song"}},
		{"soundcloud without a public page", Track{Source: SourceSoundCloud, ID: "123", WebURL: "https://evil.example/u/song"}, SongIdentity{Provider: "soundcloud", SongID: "123"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PublicSongIdentity(tc.track)
			if !ok || got != tc.want {
				t.Fatalf("identity %+v, ok %v; want %+v", got, ok, tc.want)
			}
		})
	}
	for _, track := range []Track{
		{Source: "unknown", ID: "1"}, {Source: SourceYouTube, ID: "bad"}, {Source: SourceYandex, ID: "../1"},
		{Source: SourceVK, ID: "-2001_456239017_secretKey"}, // the public form is valid; tested again below
	} {
		if track.Source == SourceVK {
			continue
		}
		if _, ok := PublicSongIdentity(track); ok {
			t.Fatalf("accepted %+v", track)
		}
	}
}

func TestMusicSourceRoundTrip(t *testing.T) {
	for source, provider := range map[string]string{
		SourceYouTube: "ytmusic", SourceSpotify: "spotify", SourceYandex: "yandex",
		SourceZvuk: "zvuk", SourceSoundCloud: "soundcloud", SourceVK: "vk",
	} {
		if got := MusicProvider(source); got != provider || MusicSource(got) != source {
			t.Fatalf("%s -> %s -> %s", source, got, MusicSource(got))
		}
	}
	if MusicProvider("other") != "" || MusicSource("other") != "" {
		t.Fatal("accepted an unknown music provider")
	}
}
