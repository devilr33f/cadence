package video

import (
	"context"
	"slices"
)

// Change is a switch of the playing video: another rendition, audio language or view; "" keeps each.
type Change struct {
	Rendition string
	Audio     string
	View      string
}

// SelectRendition plays the session's rendition id from where the device is now.
func (s *Service) SelectRendition(ctx context.Context, deviceID, id string) error {
	return s.Switch(ctx, deviceID, Change{Rendition: id})
}

// Switch plays the session again as c changes it, from where the device is now: the urls it resolved still work,
// so nothing is resolved again. A rendition picked in an audio view goes back to the video.
func (s *Service) Switch(ctx context.Context, deviceID string, c Change) error {
	defer s.playLock(deviceID)()
	s.mu.Lock()
	sess := s.live(deviceID)
	if sess == nil {
		s.mu.Unlock()
		return ErrNoSession
	}
	next := *sess
	s.mu.Unlock()
	if c.Rendition != "" {
		i := slices.IndexFunc(next.renditions, func(r rendition) bool { return r.ID == c.Rendition })
		if i < 0 {
			return ErrUnknownRendition
		}
		next.selected = i
		if c.View == "" {
			next.view = ViewVideo
		}
	}
	if c.Audio != "" {
		i := slices.IndexFunc(next.audio, func(a audioTrack) bool { return a.ID == c.Audio })
		if i < 0 {
			return ErrUnknownAudio
		}
		next.audioSel = i
	}
	if c.View != "" {
		if !slices.Contains(next.views, c.View) {
			return ErrUnknownView
		}
		next.view = c.View
	}
	var pos int64
	if !next.live {
		pos = s.position(deviceID)
	}
	if err := s.sendVideo(ctx, deviceID, &next, pos); err != nil {
		return err
	}
	s.mu.Lock()
	sess.selected, sess.audioSel, sess.view, sess.device = next.selected, next.audioSel, next.view, next.device
	s.mu.Unlock()
	s.d.Log.Info("video_switched", "device_id", deviceID, "height", next.renditions[next.selected].Height,
		"view", next.view, "audio", next.audioSel)
	return nil
}

// Renditions answers the device's session's renditions, highest first, false without a live session.
func (s *Service) Renditions(deviceID string) ([]RenditionView, bool) {
	v, ok := s.View(deviceID)
	return v.Renditions, ok
}

// VideoView is the video the server plays on the device, as the dashboard shows it: what it is, its qualities, audio
// languages and views. Memory only (D11).
type VideoView struct {
	SessionID   string          `json:"sessionId,omitempty" doc:"The device's media session this video plays as; the Media card shows these choices only while it is the device's session."`
	Title       string          `json:"title,omitempty"`
	Channel     string          `json:"channel,omitempty"`
	Description string          `json:"description,omitempty" doc:"The start of the video's description (at most 500 characters)."`
	DurationMS  int64           `json:"durationMs,omitempty"`
	ArtworkURL  string          `json:"artworkUrl,omitempty" doc:"The still, through the server's image proxy."`
	Live        bool            `json:"live,omitempty"`
	Music       bool            `json:"music,omitempty" doc:"YouTube files it as music, or it came from music.youtube.com."`
	View        string          `json:"view,omitempty" enum:"video,audio,visualizer" doc:"How the screen shows it."`
	Views       []string        `json:"views" nullable:"false" enum:"video,audio,visualizer" doc:"The views it switches between (a music video's); empty for none."`
	Renditions  []RenditionView `json:"renditions" nullable:"false" doc:"The video's qualities, highest first."`
	Audio       []AudioView     `json:"audio" nullable:"false" doc:"Its audio languages, the original marked; empty for one."`
}

// View answers the device's session's view, false without a live session
func (s *Service) View(deviceID string) (VideoView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.live(deviceID)
	if sess == nil {
		return VideoView{Views: []string{}, Renditions: []RenditionView{}, Audio: []AudioView{}}, false
	}
	v := VideoView{
		SessionID: sess.device, Title: sess.meta.Title, Channel: sess.meta.Channel, Description: sess.info.Description,
		DurationMS: sess.info.DurationMS, ArtworkURL: sess.meta.ArtworkURL, Live: sess.live, Music: sess.music,
		View: sess.view, Views: slices.Clone(sess.views), Renditions: make([]RenditionView, len(sess.renditions)), Audio: []AudioView{},
	}
	if v.Views == nil {
		v.Views = []string{}
	}
	for i, rd := range sess.renditions {
		v.Renditions[i] = rd.RenditionView
		v.Renditions[i].Selected = i == sess.selected
	}
	if len(sess.audio) > 1 {
		for i, a := range sess.audio {
			x := a.AudioView
			x.Selected = i == sess.audioSel
			v.Audio = append(v.Audio, x)
		}
	}
	return v, true
}
