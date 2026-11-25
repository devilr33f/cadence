package screen

import (
	"context"
	"time"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// an airplay video cast's command waits this long for the screen, off the media plane's reader
const hlsWait = commandWait

// hlsCast is a cast the screen plays: a start given as a fraction waits for the duration
type hlsCast struct {
	fraction float32
}

// HlsCast plays an airplay video cast (audiod's HlsCast) on the screen as a media.video session with the cast's
// session id, owned here like any other play: displacing what plays, gated until the persisted level holds (rule
// 7: the screen's player starts paused), its state back to the sender through audiod (HlsPlaybackInfo). The sender's
// rate pauses or resumes and never touches a level. It never blocks; the url and the sender's name are never logged.
func (s *Server) HlsCast(c *audiodv1.HlsCast) {
	id := c.GetSessionId()
	if id == "" {
		return
	}
	var env func(rid string) *screenv1.Envelope
	switch c.GetAction() {
	case audiodv1.HlsCastAction_HLS_CAST_ACTION_PLAY:
		mp := &screenv1.MediaPlay{
			SessionId: id, Purpose: screenv1.MediaPurpose_MEDIA_PURPOSE_CONTENT, Url: c.GetUrl(), StartMs: c.GetStartMs(),
			Playable: cadencev2.PlayableKind_PLAYABLE_KIND_STREAM,
			Metadata: &cadencev2.MediaMetadata{
				Kind: cadencev2.MediaKind_MEDIA_KIND_VIDEO, Provider: &cadencev2.MediaProvider{Id: "airplay", Name: "AirPlay"},
			},
		}
		s.video.mu.Lock()
		s.video.plays[id] = mp
		s.video.mu.Unlock()
		s.mu.Lock()
		s.hls[id] = &hlsCast{fraction: c.GetStartFraction()}
		s.mu.Unlock()
		s.log.Info("hls_cast_play", "session_id", id)
		env = func(rid string) *screenv1.Envelope {
			mp.RequestId = rid
			return &screenv1.Envelope{Body: &screenv1.Envelope_MediaPlay{MediaPlay: mp}}
		}
	case audiodv1.HlsCastAction_HLS_CAST_ACTION_SCRUB:
		env = hlsCommand(screenv1.MediaAction_MEDIA_ACTION_SEEK, c.GetPositionMs())
	case audiodv1.HlsCastAction_HLS_CAST_ACTION_RATE:
		a := screenv1.MediaAction_MEDIA_ACTION_RESUME
		if c.GetRate() == 0 {
			a = screenv1.MediaAction_MEDIA_ACTION_PAUSE
		}
		env = hlsCommand(a, 0)
	case audiodv1.HlsCastAction_HLS_CAST_ACTION_STOP:
		env = hlsCommand(screenv1.MediaAction_MEDIA_ACTION_STOP, 0)
	default:
		return
	}
	if !s.ownsCast(id) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hlsWait+time.Second)
		defer cancel()
		play := c.GetAction() == audiodv1.HlsCastAction_HLS_CAST_ACTION_PLAY
		// the session the cast displaces, read before the screen can report the cast's own
		prev := ""
		if play {
			prev = s.video.current()
		}
		if err := s.request(ctx, env); err != nil {
			s.log.Info("hls_cast_refused", "session_id", id, "action", c.GetAction().String())
			if play {
				s.endCast(id, 0, 0, "failed")
			}
			return
		}
		if play {
			// the cast took the player: what it played is over, as any play's
			s.video.endReplaced(prev, id)
		}
	}()
}

func hlsCommand(a screenv1.MediaAction, positionMs uint64) func(string) *screenv1.Envelope {
	return func(rid string) *screenv1.Envelope {
		return &screenv1.Envelope{Body: &screenv1.Envelope_MediaCommand{MediaCommand: &screenv1.MediaCommand{
			RequestId: rid, Action: a, PositionMs: positionMs,
		}}}
	}
}

func (s *Server) ownsCast(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hls[id] != nil
}

// castState tells audiod how the screen plays a cast, from the player's state; a start given as a fraction seeks
// there once the duration is known, and the cast's last state ends it with the sender
func (s *Server) castState(ms *screenv1.MediaState) {
	id := ms.GetSessionId()
	s.mu.Lock()
	c := s.hls[id]
	var seek uint64
	if c != nil && c.fraction > 0 && ms.GetDurationMs() > 0 {
		seek = uint64(float64(c.fraction) * float64(ms.GetDurationMs()))
		c.fraction = 0
	}
	s.mu.Unlock()
	if c == nil || s.o.Media == nil {
		return
	}
	if seek > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), hlsWait+time.Second)
			defer cancel()
			_ = s.request(ctx, hlsCommand(screenv1.MediaAction_MEDIA_ACTION_SEEK, seek))
		}()
	}
	if ms.GetEnded() || ms.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_IDLE {
		reason := "finished"
		if ms.GetErrorCode() != "" {
			reason = "failed"
		}
		s.endCast(id, ms.GetPositionMs(), ms.GetDurationMs(), reason)
		return
	}
	var rate float32
	if ms.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
		rate = 1
	}
	buffering := ms.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING
	s.o.Media.HlsInfo(&audiodv1.HlsPlaybackInfo{
		SessionId: id, PositionMs: ms.GetPositionMs(), DurationMs: ms.GetDurationMs(), Rate: rate, Ready: !buffering,
		BufferedMs: ms.GetBufferedMs(), BufferEmpty: buffering && ms.GetBufferedMs() == 0,
		LikelyToKeepUp: !buffering,
	})
}

// endCast ends a cast with its sender: its last playback info, ended with reason
func (s *Server) endCast(id string, positionMs, durationMs uint64, reason string) {
	s.mu.Lock()
	_, ok := s.hls[id]
	delete(s.hls, id)
	s.mu.Unlock()
	if !ok || s.o.Media == nil {
		return
	}
	s.log.Info("hls_cast_ended", "session_id", id, "reason", reason)
	s.o.Media.HlsInfo(&audiodv1.HlsPlaybackInfo{
		SessionId: id, PositionMs: positionMs, DurationMs: durationMs, Ready: true, Ended: true, Reason: reason,
	})
}
