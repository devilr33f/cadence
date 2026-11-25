package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// maxAnnounceRunes bounds an announcement's text, counted once trimmed
const maxAnnounceRunes = 500

// statusClientClosed answers a caller that went away (nginx's 499)
const statusClientClosed = 499

// Devices finds a device's canonical id, or devices.ErrNotFound.
type Devices interface {
	Canonical(ctx context.Context, deviceID string) (string, error)
}

type announceIn struct {
	ID string `path:"id"`
	api.RawJSON
}

type announceOut struct {
	Body struct {
		StreamID string `json:"streamId"`
	}
}

// RegisterAdmin serves POST /devices/{id}/announce {text, language?}: 202
// {"streamId"} once the announcement's first frame went out. Error bodies
// carry codes only, never the text.
func RegisterAdmin(a huma.API, co *Coordinator, devs Devices) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "announce-to-device", Method: http.MethodPost, Path: "/devices/{id}/announce",
		Tags: []string{"voice"}, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *announceIn) (*announceOut, error) {
		dev, err := devs.Canonical(ctx, in.ID)
		switch {
		case errors.Is(err, devices.ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, context.Canceled):
			return nil, api.Error(statusClientClosed, "canceled")
		case err != nil:
			co.d.Log.Error("announce_lookup_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		ann, ok := parseAnnouncement(in.Bytes())
		if !ok {
			return nil, api.Error(http.StatusUnprocessableEntity, "announce_invalid")
		}
		id, err := co.Announce(ctx, dev, ann)
		if err != nil {
			return nil, announceFailure(err)
		}
		out := &announceOut{}
		out.Body.StreamID = id
		return out, nil
	})
}

func announceFailure(err error) error {
	switch {
	case errors.Is(err, ErrOffline):
		return api.Error(http.StatusConflict, codeOffline)
	case errors.Is(err, ErrNoAudio):
		return api.Error(http.StatusConflict, codeNoAudio)
	case errors.Is(err, ErrBusy):
		return api.Error(http.StatusConflict, codeBusy)
	case errors.Is(err, ErrTTSUnconfigured):
		return api.Error(http.StatusConflict, codeTTSUnconfigured)
	case errors.Is(err, ErrSuperseded):
		return api.Error(http.StatusConflict, codeSuperseded)
	case errors.Is(err, ErrTTSFailed):
		return api.Error(http.StatusBadGateway, "tts_failed")
	case errors.Is(err, ErrTTSTimeout):
		return api.Error(http.StatusGatewayTimeout, codeTTSTimeout)
	case errors.Is(err, context.Canceled):
		return api.Error(statusClientClosed, codeCanceled)
	}
	return api.Error(http.StatusInternalServerError, "internal")
}

// parseAnnouncement reads {text, language?} strictly: keys as spelled and no
// others, text 1–500 characters once trimmed, language en or ru.
func parseAnnouncement(body []byte) (Announcement, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return Announcement{}, false
	}
	var a Announcement
	for k, raw := range fields {
		var s *string
		if json.Unmarshal(raw, &s) != nil || s == nil {
			return Announcement{}, false
		}
		switch k {
		case "text":
			a.Text = strings.TrimSpace(*s)
		case "language":
			if *s != "en" && *s != "ru" {
				return Announcement{}, false
			}
			a.Language = *s
		default:
			return Announcement{}, false
		}
	}
	n := utf8.RuneCountInString(a.Text)
	return a, n > 0 && n <= maxAnnounceRunes
}
