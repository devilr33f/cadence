package media_test

import (
	"log/slog"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

func mediaState(src cadencev2.MediaSource, title, artist string, md func(*cadencev2.MediaMetadata)) *cadencev2.Envelope {
	m := &cadencev2.MediaMetadata{Title: title, Artist: artist}
	if md != nil {
		md(m)
	}
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: "s", Source: src, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, Metadata: m,
	}}}
}

// a group member's session shows its stream source's cover (an airplay upload, named by the source) and the details
// it lacks while both name the same song; the source's change reaches the member's feed
func TestGroupMemberSharesTheSourcesCover(t *testing.T) {
	bus := events.NewBus(0)
	sub := bus.Watch(func(ev events.Event) bool { return ev.DeviceID == "member" })
	t.Cleanup(sub.Close)
	s := media.NewStore(bus, slog.New(slog.DiscardHandler))
	s.ShareGroups(func(id string) (string, bool) { return "leader", id == "member" }, func(id string) []string {
		if id == "leader" {
			return []string{"member", "speaker"}
		}
		return nil
	})
	hash := strings.Repeat("ab", 32)
	s.Handle("member", mediaState(cadencev2.MediaSource_MEDIA_SOURCE_GROUP, "Town of Strangers", "Bokka", nil))
	if v, _ := s.Get("member"); v.Metadata.ArtworkHash != "" {
		t.Fatalf("a cover before the source has one: %+v", v.Metadata)
	}
	<-sub.C
	s.Handle("leader", mediaState(cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY, "Town of Strangers", "Bokka", func(m *cadencev2.MediaMetadata) {
		m.ArtworkSha256, m.Album = hash, "Bokka"
	}))
	ev := <-sub.C
	got, ok := ev.Data.(media.MediaSession)
	if !ok || got.Metadata.ArtworkHash != hash || got.ArtworkDevice != "leader" || got.Metadata.Album != "Bokka" {
		t.Fatalf("the member's feed after the source's change: %+v", ev.Data)
	}
	if v, _ := s.Get("member"); v.Metadata.ArtworkHash != hash || v.Source != "group" {
		t.Fatalf("member %+v", v)
	}
	// the member ahead of the source (the next song): nothing of the old one
	s.Handle("member", mediaState(cadencev2.MediaSource_MEDIA_SOURCE_GROUP, "Next", "Bokka", nil))
	if v, _ := s.Get("member"); v.Metadata.ArtworkHash != "" || v.Metadata.Album != "" {
		t.Fatalf("another song's cover: %+v", v.Metadata)
	}
	// a member's own url cover is its own; the source's own session is untouched
	s.Handle("member", mediaState(cadencev2.MediaSource_MEDIA_SOURCE_GROUP, "Town of Strangers", "Bokka", func(m *cadencev2.MediaMetadata) {
		m.ArtworkUrl = "https://img.example/a.jpg"
	}))
	if v, _ := s.Get("member"); v.ArtworkDevice != "" || v.Metadata.ArtworkURL != "https://img.example/a.jpg" {
		t.Fatalf("member's own cover %+v", v)
	}
	if v, _ := s.Get("leader"); v.ArtworkDevice != "" {
		t.Fatalf("leader %+v", v)
	}
}
