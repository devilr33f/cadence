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

// SessionState is the catalog session a device plays now. Ref is its title
// group when it has one, so a source switch can choose another variant.
// Everything but the session id is third-party data, memory-only (D11).
type SessionState struct {
	SessionID string
	Ref       Ref
	Episode   string
	Choices   Selection
}

// SessionTracker reads the server's ownership of a device's current session.
type SessionTracker interface {
	Current(deviceID string) (SessionState, bool)
}

// SessionView is the current session and the choices its title offers now.
type SessionView struct {
	SessionID string      `json:"sessionId"`
	Ref       Ref         `json:"ref"`
	Episode   string      `json:"episode,omitempty"`
	Choices   Selection   `json:"choices" nullable:"false"`
	Options   OptionsView `json:"options"`
}

type sessionIn struct {
	ID string `path:"id"`
}

type sessionOut struct{ Body SessionView }

// SelectBody changes one choice of the playing catalog item.
type SelectBody struct {
	Group  GroupKind `json:"group" enum:"source,voiceover,quality"`
	Choice string    `json:"choice" minLength:"1" maxLength:"64"`
}

type selectIn struct {
	ID   string `path:"id"`
	Body SelectBody
}

var sessionDocs = map[string]api.Doc{
	"get-device-catalog-session": {Summary: "Read the current catalog session and its choices", Scope: auth.ScopeHA},
	"select-device-catalog": {
		Summary: "Switch the playing catalog item's source, voiceover or quality", Scope: auth.ScopeHA,
		Body: SelectBody{}, BodyRequired: true,
	},
}

// RegisterSessionAdmin exposes only sessions the screenplay tracker owns. A
// select goes through the screen's own media.play with replaces and reason
// switch: it keeps the position, remembers the choice and cannot displace a
// newer session. Refs, titles and choice ids are never logged.
func RegisterSessionAdmin(a huma.API, s *Service, play calls.Method, sessions SessionTracker) {
	defer api.Describe(a, sessionDocs)
	current := func(id string) (SessionState, error) {
		if sessions == nil {
			return SessionState{}, api.Error(http.StatusNotFound, "no_catalog_session")
		}
		cur, ok := sessions.Current(id)
		if !ok {
			return SessionState{}, api.Error(http.StatusNotFound, "no_catalog_session")
		}
		return cur, nil
	}
	huma.Register(a, huma.Operation{
		OperationID: "get-device-catalog-session", Method: http.MethodGet, Path: "/devices/{id}/catalog/session", Tags: []string{tag},
		Description: "The playing title's ref, episode and resolved choices, and the choice groups available for it. 404 no_catalog_session for a URL, AirPlay, music or other session the catalog did not start.",
	}, func(ctx context.Context, in *sessionIn) (*sessionOut, error) {
		cur, err := current(in.ID)
		if err != nil {
			return nil, err
		}
		o, err := s.Options(ctx, Household, cur.Ref, cur.Episode, cur.Choices)
		if err != nil {
			return nil, refuse(err)
		}
		return &sessionOut{Body: SessionView{SessionID: cur.SessionID, Ref: cur.Ref, Episode: cur.Episode,
			Choices: Selection{}.Merge(cur.Choices), Options: o}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "select-device-catalog", Method: http.MethodPost, Path: "/devices/{id}/catalog/select", Tags: []string{tag},
		Description: "Resolves the same episode again with one newly selected choice, keeping the playing position. 422 invalid_choice when the current title does not offer it or it is unavailable; 409 superseded when another session has already replaced it.",
	}, func(ctx context.Context, in *selectIn) (*playOut, error) {
		cur, err := current(in.ID)
		if err != nil {
			return nil, err
		}
		o, err := s.Options(ctx, Household, cur.Ref, cur.Episode, cur.Choices)
		if err != nil {
			return nil, refuse(err)
		}
		if !choiceOffered(o, in.Body.Group, in.Body.Choice) {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_choice")
		}
		args, err := json.Marshal(struct {
			Ref      Ref         `json:"ref"`
			Episode  string      `json:"episode,omitempty"`
			Choices  Selection   `json:"choices"`
			From     ResolveFrom `json:"from"`
			Replaces string      `json:"replaces"`
			Reason   string      `json:"reason"`
		}{cur.Ref, cur.Episode, Selection{in.Body.Group: in.Body.Choice}, FromStart, cur.SessionID, "switch"})
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

func choiceOffered(o OptionsView, kind GroupKind, id string) bool {
	for _, g := range o.Groups {
		if g.Kind != kind {
			continue
		}
		for _, c := range g.Choices {
			if c.ID == id && !c.Unavailable {
				return true
			}
		}
	}
	return false
}
