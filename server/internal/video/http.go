package video

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type Devices interface {
	Canonical(ctx context.Context, deviceID string) (string, error)
}

type playIn struct {
	ID   string `path:"id"`
	Body struct {
		URL     string `json:"url" minLength:"1" maxLength:"2048" doc:"A youtube.com or youtu.be link: watch, shorts, live or embed; its t= is where it starts."`
		StartMS *int64 `json:"startMs,omitempty" minimum:"0" doc:"Where to start, in ms; left out: the link's t=, else the start."`
		Screen  bool   `json:"screenOnly,omitempty" doc:"Refuse a device without a screen (409 no_screen) instead of playing the audio."`
		View    string `json:"view,omitempty" enum:"video,audio,visualizer" doc:"How a screen shows a music video: its picture, its audio with the cover, or its audio with the visualizer; left out: the picture, or the visualizer for a music.youtube.com link."`
	}
}

type changeIn struct {
	ID   string `path:"id"`
	Body struct {
		Rendition string `json:"rendition,omitempty" maxLength:"16" doc:"A rendition's id (1080p, 720p60); a rendition picked in an audio view goes back to the video."`
		Audio     string `json:"audio,omitempty" maxLength:"64" doc:"An audio language's id (a-ru)."`
		View      string `json:"view,omitempty" enum:"video,audio,visualizer" doc:"One of the video's views."`
	}
}

type playOut struct{ Body PlayResult }

type idIn struct {
	ID string `path:"id"`
}

type viewOut struct {
	Body VideoView
}

// RegisterAdmin serves POST /devices/{id}/video (play a YouTube link: renditions on a screen, the audio elsewhere),
// GET /devices/{id}/video (the playing video: what it is, its renditions, audio languages and views) and PATCH
// /devices/{id}/video (switch them, from where it plays). A rendition is also chosen with send-device-command's
// media.video select_track, kind video, by its id. The link is never logged or stored.
func RegisterAdmin(a huma.API, s *Service, devs Devices) {
	huma.Register(a, huma.Operation{
		OperationID: "play-device-video", Method: http.MethodPost, Path: "/devices/{id}/video", Tags: []string{"media"},
		Summary: "Play a YouTube link on the device",
		Description: "A screen (display.screen with screen: true, and media.video) plays the video at the best quality " +
			"its profile allows, through the server's stream relay; another device plays its audio as a media.url " +
			"session. Never changes the volume. 422 invalid_url for a link that is not a YouTube video; 409 no_screen, " +
			"no_player, unplayable, sign_in_required or device_offline; 503 not_set_up without a musicPlayer. A screen " +
			"shows its resolving step while the server resolves the link, and the failure when it fails.",
	}, func(ctx context.Context, in *playIn) (*playOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		id, start, ok := ParseURL(in.Body.URL)
		if !ok {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_url", "/body/url")
		}
		if in.Body.StartMS != nil {
			start = *in.Body.StartMS
		}
		res, err := s.Play(ctx, dev, Request{VideoID: id, StartMS: start, ScreenOnly: in.Body.Screen, View: in.Body.View,
			Music: IsMusicURL(in.Body.URL)})
		if err != nil {
			return nil, httpError(err)
		}
		return &playOut{Body: res}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-device-video", Method: http.MethodGet, Path: "/devices/{id}/video", Tags: []string{"media"},
		Summary: "Get the video the device plays",
		Description: "The video the server started on the device (memory only): its title, channel, the start of its " +
			"description, its still, the renditions the server resolved with the one selected, its audio languages " +
			"(original and dubbings) and, for a music video, its views. sessionId names the device's media session it " +
			"plays as; empty lists when the server started none or its streams expired.",
	}, func(ctx context.Context, in *idIn) (*viewOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		v, _ := s.View(dev)
		return &viewOut{Body: v}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "update-device-video", Method: http.MethodPatch, Path: "/devices/{id}/video", Tags: []string{"media"},
		Summary: "Switch the quality, audio language or view of the video the device plays",
		Description: "Plays the video the server started on the device again from where it is, at another rendition, " +
			"in another audio language or in another view (a music video's video, audio or visualizer); nothing is " +
			"resolved again. Never changes the volume. 404 no_session without a video, 422 unknown_rendition, " +
			"unknown_audio or unknown_view for a choice it does not hold; 409 device_offline.",
	}, func(ctx context.Context, in *changeIn) (*viewOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		c := Change{Rendition: in.Body.Rendition, Audio: in.Body.Audio, View: in.Body.View}
		if c == (Change{}) {
			return nil, api.Error(http.StatusUnprocessableEntity, "nothing_to_change", "/body")
		}
		if err := s.Switch(ctx, dev, c); err != nil {
			return nil, httpError(err)
		}
		v, _ := s.View(dev)
		return &viewOut{Body: v}, nil
	})
}

func canonical(ctx context.Context, devs Devices, id string) (string, error) {
	dev, err := devs.Canonical(ctx, id)
	switch {
	case errors.Is(err, devices.ErrNotFound):
		return "", api.Error(http.StatusNotFound, "not_found")
	case err != nil:
		return "", api.Error(http.StatusInternalServerError, "internal")
	}
	return dev, nil
}

func httpError(err error) error {
	var device *commands.DeviceError
	switch {
	case errors.Is(err, ErrNotSetUp):
		return api.Error(http.StatusServiceUnavailable, "not_set_up")
	case errors.Is(err, ErrNoSession):
		return api.Error(http.StatusNotFound, "no_session")
	case errors.Is(err, ErrUnknownRendition), errors.Is(err, ErrUnknownAudio), errors.Is(err, ErrUnknownView):
		return api.Error(http.StatusUnprocessableEntity, Code(err))
	case errors.Is(err, ErrNoScreen), errors.Is(err, ErrNoPlayer), errors.Is(err, providers.ErrUnplayable),
		errors.Is(err, providers.ErrSignInRequired):
		return api.Error(http.StatusConflict, Code(err))
	case errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return api.Error(http.StatusConflict, "no_player")
	case errors.Is(err, commands.ErrOffline):
		return api.Error(http.StatusConflict, "device_offline")
	case errors.Is(err, commands.ErrTimeout):
		return api.Error(http.StatusGatewayTimeout, "timeout")
	case errors.As(err, &device):
		return api.Error(http.StatusConflict, device.Code)
	case err.Error() == providers.CodePlayerFailed:
		return api.Error(http.StatusBadGateway, providers.CodePlayerFailed)
	}
	return api.Error(http.StatusInternalServerError, "internal")
}
