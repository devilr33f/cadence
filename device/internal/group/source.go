package group

import (
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// LocalSource says the media session is a source of this device's own that the group can carry: a url, file or
// stream, soloist or airplay that audiod plays into its music input, or an external session (a screen's player, a
// screencast, another app) whose pcm reaches audiod's mixer, while it plays or buffers. The group's own stream is
// not, nor a direct route (bitstream to a tv's decoder: it plays on this device alone, D4), nor bluetooth, nor a
// follower screen's video (its picture follows the group's stream: raising it would steal the group's source, P4).
func LocalSource(s *cadencev2.MediaSessionState) bool {
	if s.GetFollow() {
		return false
	}
	switch s.GetState() {
	case cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING:
	default:
		return false
	}
	route := s.GetAudioRoute()
	switch s.GetSource() {
	case cadencev2.MediaSource_MEDIA_SOURCE_URL, cadencev2.MediaSource_MEDIA_SOURCE_STREAM,
		cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST, cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY:
		// audiod's own sessions name no route or the mixer
		return route == cadencev2.AudioRoute_AUDIO_ROUTE_UNSPECIFIED || route == cadencev2.AudioRoute_AUDIO_ROUTE_MIXER
	case cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST,
		cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL:
		return route == cadencev2.AudioRoute_AUDIO_ROUTE_MIXER
	default:
		return false
	}
}

// MediaSession takes the device's media session after each change (media.Media.WatchSession) and says in the state
// whether it is a source of the device's own (LocalSource). It never blocks.
func (g *Group) MediaSession(s *cadencev2.MediaSessionState) {
	local := LocalSource(s)
	direct := s.GetAudioRoute() == cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT && s.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING
	g.mu.Lock()
	defer g.mu.Unlock()
	if local == g.local && direct == g.direct {
		return
	}
	if g.cfg.Enabled && direct && !g.direct {
		// a passthrough session plays here alone; the group is left idle (D4)
		g.log.Info("group_source_local", "reason", "direct", "group", g.cfg.GroupID)
	}
	g.local, g.direct = local, direct
	g.publishLocked()
}
