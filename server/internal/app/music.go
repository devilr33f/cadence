package app

import (
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/soundcloud"
	"git.shino.boo/devilreef/cadence/server/internal/providers/vkmusic"
	"git.shino.boo/devilreef/cadence/server/internal/providers/yandexmusic"
	"git.shino.boo/devilreef/cadence/server/internal/providers/zvuk"
	"git.shino.boo/devilreef/cadence/server/internal/streams"
)

// musicStream has the device fetch a resolved stream from the server's relay
// at the public origin, as the stream is bound to the address the server asked
// from; without an origin the device fetches the upstream url itself.
func musicStream(origin string, relay *streams.Relay) func(deviceID string, upstream providers.Stream) (string, error) {
	if origin == "" {
		return nil
	}
	return func(deviceID string, upstream providers.Stream) (string, error) {
		token, err := relay.AddSet(deviceID, []streams.Upstream{{URL: upstream.URL, HLS: upstream.HLS}}, streams.Options{})
		if err != nil {
			return "", err
		}
		return origin + "/v2/stream/" + token, nil
	}
}

// musicMediaHosts are the hosts the relay fetches songs from: YouTube's and
// those of every music provider that streams its own songs
func musicMediaHosts() []string {
	hosts := []string{".googlevideo.com"}
	for _, h := range [][]string{soundcloud.MediaHosts, yandexmusic.MediaHosts, zvuk.MediaHosts, vkmusic.MediaHosts} {
		hosts = append(hosts, h...)
	}
	return hosts
}

// nowPlaying is the turn's view of what the device plays: its session as the
// queue decorates it, and the queue's next songs while it plays one of them.
func nowPlaying(sessions *media.Store, songs *music.Service, now func() time.Time) func(deviceID string) (agent.NowPlaying, bool) {
	return func(deviceID string) (agent.NowPlaying, bool) {
		s, ok := sessions.Get(deviceID)
		if !ok || s.State == "idle" {
			return agent.NowPlaying{}, false
		}
		pos := seconds(s.PositionMs)
		if at, err := time.Parse(time.RFC3339Nano, s.PositionAt); err == nil && s.State == "playing" {
			pos += int(now().Sub(at).Seconds() * float64(max(s.Rate, 0)))
		}
		dur := seconds(s.DurationMs)
		if dur > 0 {
			pos = min(pos, dur)
		}
		np := agent.NowPlaying{
			Title: s.Metadata.Title, Artist: s.Metadata.Artist, Album: s.Metadata.Album, Source: s.Source,
			Provider: s.Provider, State: s.State, PositionS: max(pos, 0), DurationS: dur,
			Skips: slices.Contains(s.Actions, "next"),
		}
		if q := songs.State(deviceID); q.Active {
			np.Queued, np.Shuffle, np.Repeat = true, q.Shuffle, string(q.Repeat)
			for _, u := range q.Upcoming[:min(len(q.Upcoming), 3)] {
				np.Next = append(np.Next, u.Title+" — "+u.Artist)
			}
		}
		return np, true
	}
}

// seconds is ms in whole seconds, a day at most: no song is longer
func seconds(ms uint64) int {
	return int(min(ms/1000, 86400)) //nolint:gosec // bounded above
}
