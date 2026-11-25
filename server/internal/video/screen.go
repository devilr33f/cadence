package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// RegisterScreen serves the screen's media.video: a switch of the video the server plays on it, from its own player
// options (the quality, the audio language, a music video's view).
func (s *Service) RegisterScreen(r *calls.Router) {
	r.Register(rpc.MediaVideo, s.switchMethod)
}

func (s *Service) switchMethod(ctx context.Context, c calls.Call) calls.Reply {
	var a struct {
		SessionID string `json:"sessionId"`
		Quality   string `json:"quality,omitempty"`
		Audio     string `json:"audio,omitempty"`
		View      string `json:"view,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	var rest json.RawMessage
	if dec.Decode(&a) != nil || dec.Decode(&rest) != io.EOF || a.SessionID == "" || len(a.SessionID) > 64 ||
		len(a.Quality) > 16 || len(a.Audio) > 64 || (a.View != "" && !ValidView(a.View)) ||
		a.Quality == "" && a.Audio == "" && a.View == "" {
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	s.mu.Lock()
	sess := s.live(c.DeviceID)
	ours := sess != nil && sess.device == a.SessionID
	s.mu.Unlock()
	if !ours {
		return calls.Fail(rpc.StatusNotFound, "no_session")
	}
	err := s.Switch(ctx, c.DeviceID, Change{Rendition: a.Quality, Audio: a.Audio, View: a.View})
	switch {
	case err == nil:
		return calls.OK(struct{}{})
	case errors.Is(err, ErrNoSession):
		return calls.Fail(rpc.StatusNotFound, "no_session")
	case errors.Is(err, ErrUnknownRendition), errors.Is(err, ErrUnknownAudio), errors.Is(err, ErrUnknownView):
		return calls.Fail(rpc.StatusInvalid, Code(err))
	}
	return calls.Fail(rpc.StatusUnavailable, Code(err))
}
