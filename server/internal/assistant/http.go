package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// maxRunes bounds a typed request, counted once trimmed
const maxRunes = 1000

// statusClientClosed answers a caller that went away (nginx's 499)
const statusClientClosed = 499

// Devices finds a device's canonical id, or devices.ErrNotFound.
type Devices interface {
	Canonical(ctx context.Context, deviceID string) (string, error)
}

// TurnRequest is what the owner typed for a device's assistant.
type TurnRequest struct {
	Text     string `json:"text" minLength:"1" maxLength:"4000" doc:"1 to 1000 characters once trimmed"`
	Language string `json:"language,omitempty" enum:"en,ru" doc:"the reply's language; the device's language setting when absent"`
	Speak    bool   `json:"speak,omitempty" doc:"also play the reply on the device, as an announcement over its music"`
}

type turnIn struct {
	ID   string `path:"id"`
	Body TurnRequest
}

type turnOut struct {
	Body struct {
		TurnID string `json:"turnId" doc:"the turn's id: its steps reach the live feed (sessions and agent topics) under it, and the conversation history stores it"`
	}
}

var docs = map[string]api.Doc{
	"start-assistant-turn": {Summary: "Run an assistant turn typed in the dashboard"},
}

// RegisterAdmin serves POST /devices/{id}/assistant {text, language?, speak?}:
// 202 {turnId} as the turn starts. The turn runs as the device's own, woken as
// a press: its tools act for real. Error bodies carry codes only.
func RegisterAdmin(a huma.API, s *Service, devs Devices) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "start-assistant-turn", Method: http.MethodPost, Path: "/devices/{id}/assistant",
		Tags: []string{"conversations"}, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *turnIn) (*turnOut, error) {
		text := strings.TrimSpace(in.Body.Text)
		if n := utf8.RuneCountInString(text); n == 0 || n > maxRunes {
			return nil, api.Error(http.StatusUnprocessableEntity, "text_invalid")
		}
		dev, err := devs.Canonical(ctx, in.ID)
		switch {
		case errors.Is(err, devices.ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, context.Canceled):
			return nil, api.Error(statusClientClosed, "canceled")
		case err != nil:
			s.d.Log.Error("assistant_lookup_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		id, err := s.Start(ctx, Request{DeviceID: dev, Text: text, Language: in.Body.Language, Speak: in.Body.Speak})
		if errors.Is(err, ErrBusy) {
			return nil, api.Error(http.StatusConflict, "busy")
		}
		out := &turnOut{}
		out.Body.TurnID = id
		return out, nil
	})
}
