package video

import (
	"context"
)

// Current is the request behind the device's session sessionID when it is a video the service played there of its
// own (not a follower's), with the view it plays in now; false for any other session. Memory only (D11).
func (s *Service) Current(deviceID, sessionID string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.live(deviceID)
	if sess == nil || sessionID == "" || sess.device != sessionID || sess.follow != "" || sess.req.VideoID == "" {
		return Request{}, false
	}
	r := sess.req
	r.StartMS, r.View = 0, sess.view
	return r, true
}

// Follow plays r on a follower screen of the multiroom group groupID (P4): resolved for that screen (its own
// profile, its own relay token), with no position of its own (the group's stream decides it). A switch of its
// quality keeps it a follower. It answers the device's session id.
func (s *Service) Follow(ctx context.Context, deviceID string, r Request, groupID string) (string, error) {
	defer s.playLock(deviceID)()
	v, err := s.d.Settings(ctx, deviceID)
	if err != nil {
		return "", err
	}
	if v.MusicPlayer == "" {
		return "", ErrNotSetUp
	}
	pp, err := s.d.Player(ctx, v.MusicPlayer)
	if err != nil {
		return "", ErrNotSetUp
	}
	p, ok := pp.(Player)
	if !ok {
		return "", ErrNotSetUp
	}
	prof := s.Profile(ctx, deviceID)
	if !prof.Screen {
		return "", ErrNoScreen
	}
	start := s.d.Now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), screenPlayTimeout)
	defer cancel()
	r.StartMS = 0
	sess, err := s.resolveScreen(ctx, deviceID, p, prof, r)
	if err == nil {
		sess.follow = groupID
		err = s.sendVideo(ctx, deviceID, sess, 0)
		if err != nil {
			s.dropToken(sess.token)
		}
	}
	ms := s.d.Now().Sub(start).Milliseconds()
	if err != nil {
		s.d.Log.Warn("video_follow_play_failed", "device_id", deviceID, "code", Code(err), "ms", ms)
		return "", err
	}
	s.mu.Lock()
	if old := s.sessions[deviceID]; old != nil && old.token != sess.token {
		s.dropToken(old.token)
	}
	s.sessions[deviceID] = sess
	device := sess.device
	s.mu.Unlock()
	s.d.Log.Info("video_follow_play", "device_id", deviceID, "renditions", len(sess.renditions), "live", sess.live, "ms", ms)
	return device, nil
}
