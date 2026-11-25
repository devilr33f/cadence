package music

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

type Devices interface {
	Canonical(ctx context.Context, deviceID string) (string, error)
}

type idIn struct {
	ID string `path:"id"`
}

type viewOut struct{ Body View }

type modesIn struct {
	ID   string `path:"id"`
	Body struct {
		Shuffle *bool   `json:"shuffle,omitempty" doc:"Shuffle the songs after the playing one; left out: unchanged."`
		Repeat  *Repeat `json:"repeat,omitempty" enum:"off,all,one" doc:"off, all (the queue) or one (the song); left out: unchanged."`
	}
}

type jumpIn struct {
	ID   string `path:"id"`
	Body struct {
		Offset int `json:"offset" minimum:"1" maximum:"200" doc:"How far ahead in the queue's upcoming list: 1 is its first song."`
	}
}

// RegisterAdmin serves the device's music queue: GET /devices/{id}/music,
// PUT /devices/{id}/music/modes and POST /devices/{id}/music/jump. Next,
// previous, pause and the rest go through send-device-command's media.player,
// whose skips the queue answers while it plays.
func RegisterAdmin(a huma.API, s *Service, devs Devices) {
	huma.Register(a, huma.Operation{
		OperationID: "get-device-music", Method: http.MethodGet, Path: "/devices/{id}/music", Tags: []string{"media"},
		Summary: "Get the device's music queue",
		Description: "The songs the server's music queue holds for the device (memory only), its shuffle and repeat " +
			"modes, and whether the device plays one of them now.",
	}, func(ctx context.Context, in *idIn) (*viewOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOut{Body: s.State(dev)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "set-device-music-modes", Method: http.MethodPut, Path: "/devices/{id}/music/modes", Tags: []string{"media"},
		Summary: "Set the device's shuffle and repeat",
	}, func(ctx context.Context, in *modesIn) (*viewOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		if in.Body.Repeat != nil && !ValidRepeat(*in.Body.Repeat) {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/repeat")
		}
		if in.Body.Shuffle != nil {
			s.SetShuffle(dev, *in.Body.Shuffle)
		}
		if in.Body.Repeat != nil {
			s.SetRepeat(dev, *in.Body.Repeat)
		}
		return &viewOut{Body: s.State(dev)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "jump-device-music", Method: http.MethodPost, Path: "/devices/{id}/music/jump", Tags: []string{"media"},
		Summary: "Play a song further down the device's queue",
		Description: "409 no_queue without a queue, queue_end past its end; a song that will not play is skipped " +
			"for the next. Answers the queue once the song was sent to the device.",
	}, func(ctx context.Context, in *jumpIn) (*viewOut, error) {
		dev, err := canonical(ctx, devs, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.Jump(ctx, dev, in.Body.Offset); err != nil {
			return nil, httpError(err)
		}
		return &viewOut{Body: s.State(dev)}, nil
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
	switch {
	case errors.Is(err, commands.ErrOffline):
		return api.Error(http.StatusConflict, "device_offline")
	case errors.Is(err, commands.ErrTimeout):
		return api.Error(http.StatusGatewayTimeout, "timeout")
	}
	var device *commands.DeviceError
	if errors.As(commandError(err), &device) {
		return api.Error(http.StatusConflict, device.Code)
	}
	return api.Error(http.StatusInternalServerError, "internal")
}
