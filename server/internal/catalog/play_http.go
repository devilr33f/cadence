package catalog

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// PlayBody is a play an api caller asks for: what the screen's own media.play
// carries, minus the fields only a playing screen knows (its session's id, the
// position a switch keeps).
type PlayBody struct {
	Ref     string    `json:"ref" maxLength:"256" doc:"A ref this server issued: search-catalog's, browse-catalog's or get-catalog-item's item or episode."`
	Episode string    `json:"episode,omitempty" maxLength:"32" doc:"The episode to play, its label as the provider keys it (5, 5.5); a series resolves its own next episode without it."`
	Choices Selection `json:"choices,omitempty" maxProperties:"8" doc:"The exact source, voiceover, quality and other choices the browser made, by choice id. A successful play remembers them for the title."`
	From    string    `json:"from,omitempty" enum:"start,resume" doc:"Where playback starts: resume (the default) or start."`
}

type playIn struct {
	ID   string `path:"id"`
	Body PlayBody
}

type playOut struct {
	Body struct {
		SessionID string `json:"sessionId"`
		Episode   string `json:"episode,omitempty"`
	}
}

var playDocs = map[string]api.Doc{
	"play-catalog": {Summary: "Play a catalog item on a device's screen", Scope: auth.ScopeHA,
		Body: PlayBody{}, BodyRequired: true},
}

// httpStatus is an rpc status token as an admin refusal's status
func httpStatus(status string) int {
	switch status {
	case rpc.StatusOK:
		return http.StatusOK
	case rpc.StatusInvalid:
		return http.StatusUnprocessableEntity
	case rpc.StatusExpired, rpc.StatusNotFound:
		return http.StatusNotFound
	case rpc.StatusRefused:
		return http.StatusConflict
	case rpc.StatusUnavailable:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

// RegisterPlayAdmin serves POST /devices/{id}/catalog/play: the same play a
// screen's own media.play runs (the catalog's resolve, then media.video/play),
// for an ha token's callers (Home Assistant's media browser). The server
// resolves and commands the device; a stream url never leaves it, and nothing
// of the call is logged but the device and a status.
func RegisterPlayAdmin(a huma.API, play calls.Method) {
	defer api.Describe(a, playDocs)
	huma.Register(a, huma.Operation{
		OperationID: "play-catalog", Method: http.MethodPost, Path: "/devices/{id}/catalog/play", Tags: []string{tag},
		Description: "Resolves the ref as the screen's own play does (the remembered choices, resume) and plays it on " +
			"the device's screen. 404 unknown_ref for a ref this server did not issue or that expired, 409 refused " +
			"with the code (unplayable, sign_in_required) when the catalog cannot play it, 422 invalid when the " +
			"arguments do not hold, 502 unavailable when the source or the screen failed.",
	}, func(ctx context.Context, in *playIn) (*playOut, error) {
		args, err := json.Marshal(in.Body)
		if err != nil {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid")
		}
		rep := play(ctx, calls.Call{DeviceID: in.ID, Method: rpc.MediaPlay, Args: args})
		if rep.Status != rpc.StatusOK {
			return nil, playRefused(rep)
		}
		out := &playOut{}
		if b, err := json.Marshal(rep.JSON); err == nil {
			_ = json.Unmarshal(b, &out.Body)
		}
		return out, nil
	})
}

// playRefused is a play's refusal as the admin error model: its status by the
// rpc token, its code the refusal's (never upstream text)
func playRefused(rep calls.Reply) error {
	code := "unavailable"
	if c, ok := rep.JSON.(map[string]string); ok && c["code"] != "" {
		code = c["code"]
	} else if m, ok := rep.JSON.(map[string]any); ok {
		if s, is := m["code"].(string); is && s != "" {
			code = s
		}
	}
	status := httpStatus(rep.Status)
	if status == http.StatusInternalServerError {
		return api.Error(status, "internal")
	}
	return api.Error(status, code)
}
