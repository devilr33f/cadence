package media

import (
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

var (
	sources = map[audiodv1.MediaSourceKind]cadencev2.MediaSource{
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL:        cadencev2.MediaSource_MEDIA_SOURCE_URL,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_SOLOIST:    cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY:    cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_STREAM:     cadencev2.MediaSource_MEDIA_SOURCE_STREAM,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO:      cadencev2.MediaSource_MEDIA_SOURCE_VIDEO,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST: cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_EXTERNAL:   cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_GROUP:      cadencev2.MediaSource_MEDIA_SOURCE_GROUP,
		audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_BLUETOOTH:  cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH,
	}
	playbackStates = map[audiodv1.PlaybackState]cadencev2.PlaybackState{
		audiodv1.PlaybackState_PLAYBACK_STATE_IDLE:      cadencev2.PlaybackState_PLAYBACK_STATE_IDLE,
		audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING:   cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		audiodv1.PlaybackState_PLAYBACK_STATE_PAUSED:    cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED,
		audiodv1.PlaybackState_PLAYBACK_STATE_BUFFERING: cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING,
	}
	actionNames = map[audiodv1.MediaAction]string{
		audiodv1.MediaAction_MEDIA_ACTION_PAUSE: "pause", audiodv1.MediaAction_MEDIA_ACTION_RESUME: "resume",
		audiodv1.MediaAction_MEDIA_ACTION_STOP: "stop", audiodv1.MediaAction_MEDIA_ACTION_SEEK: "seek",
		audiodv1.MediaAction_MEDIA_ACTION_NEXT: "next", audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS: "previous",
	}
	sourceNames = map[cadencev2.MediaSource]string{
		cadencev2.MediaSource_MEDIA_SOURCE_URL: "url", cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST: "soloist",
		cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY: "airplay", cadencev2.MediaSource_MEDIA_SOURCE_STREAM: "stream",
		cadencev2.MediaSource_MEDIA_SOURCE_VIDEO: "video", cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST: "screencast",
		cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL: "external", cadencev2.MediaSource_MEDIA_SOURCE_GROUP: "group",
		cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH: "bluetooth",
	}
	stateNames = map[cadencev2.PlaybackState]string{
		cadencev2.PlaybackState_PLAYBACK_STATE_IDLE: "idle", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING: "playing",
		cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED: "paused", cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING: "buffering",
	}
)

// sessionState is audiod's MediaState as the server takes it; an unknown
// value maps to unspecified and an unknown action is left out.
func sessionState(ms *audiodv1.MediaState) *cadencev2.MediaSessionState {
	s := &cadencev2.MediaSessionState{
		SessionId: ms.GetSessionId(), Source: sources[ms.GetSource()], State: playbackStates[ms.GetState()],
		PositionMs: ms.GetPositionMs(), PositionAtMs: ms.GetPositionAtMs(), Rate: ms.GetRate(), DurationMs: ms.GetDurationMs(),
		AudioRoute: cadencev2.AudioRoute(ms.GetAudioRoute()), ControlLatencyMs: ms.GetControlLatencyMs(),
	}
	for _, c := range ms.GetAudioControls() {
		s.AudioControls = append(s.AudioControls, cadencev2.AudioControl(c))
	}
	if ms.Volume != nil {
		v := ms.GetVolume()
		s.Volume = &v
	}
	if md := ms.GetMetadata(); md != nil {
		s.Metadata = &cadencev2.MediaMetadata{Title: md.GetTitle(), Artist: md.GetArtist(), Album: md.GetAlbum(), ArtworkUrl: md.GetArtworkUrl()}
	}
	for _, a := range ms.GetActions() {
		if name, ok := actionNames[a]; ok {
			s.Actions = append(s.Actions, name)
		}
	}
	return s
}

func sourceName(s cadencev2.MediaSource) string {
	if n, ok := sourceNames[s]; ok {
		return n
	}
	return "none"
}

func stateName(s cadencev2.PlaybackState) string {
	if n, ok := stateNames[s]; ok {
		return n
	}
	return "unspecified"
}
