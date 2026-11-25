package commands

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// statusClientClosed answers a caller that went away (nginx's 499)
const statusClientClosed = 499

type commandIn struct {
	ID         string `path:"id"`
	Capability string `path:"cap"`
	Name       string `path:"name"`
	api.RawJSON
	api.Caller
}

// commandOut's body is {"result": <the device's result>} with the result as it
// came (compact, never re-encoded)
type commandOut struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// RegisterAdmin serves POST /devices/{id}/capabilities/{cap}/commands/{name};
// the body is the command's args, a JSON object (empty: none). Error bodies
// carry codes and the failing path, never an arg or device text.
func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "send-device-command", Method: http.MethodPost,
		Path: "/devices/{id}/capabilities/{cap}/commands/{name}", Tags: []string{"commands"}, Description: haNote(),
	}, func(ctx context.Context, in *commandIn) (*commandOut, error) {
		// the scope gate let ha in; which commands it may send is this list's
		if !mayCall(in.Scopes(), in.Capability, in.Name, in.Bytes()) {
			s.log.Warn("api_forbidden", "op", "send-device-command", "scope", string(auth.ScopeAdmin))
			return nil, api.Error(http.StatusForbidden, "forbidden")
		}
		res, err := s.Send(ctx, in.ID, in.Capability, in.Name, in.Bytes())
		if err != nil {
			return nil, failure(s, err)
		}
		if res == nil {
			res = []byte("null")
		}
		body := append(append([]byte(`{"result":`), res...), '}')
		return &commandOut{ContentType: "application/json", Body: body}, nil
	})
}

func failure(s *Service, err error) error {
	var invalid *InvalidArgumentsError
	var device *DeviceError
	switch {
	case errors.Is(err, devices.ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, devices.ErrUnknownCapability):
		return api.Error(http.StatusNotFound, "unknown_capability")
	case errors.Is(err, devices.ErrUnknownCommand):
		return api.Error(http.StatusNotFound, "unknown_command")
	case errors.As(err, &invalid):
		return api.Error(http.StatusUnprocessableEntity, "invalid_arguments", invalid.Path)
	case errors.Is(err, ErrArgumentsTooLarge):
		return api.Error(http.StatusRequestEntityTooLarge, "arguments_too_large")
	case errors.Is(err, ErrOffline):
		return api.Error(http.StatusConflict, "device_offline")
	case errors.Is(err, ErrBusy):
		return api.Error(http.StatusTooManyRequests, "busy")
	case errors.Is(err, ErrTimeout):
		return api.Error(http.StatusGatewayTimeout, "timeout")
	case errors.As(err, &device):
		// the device's own code, a token, refines it (ruling M7a-R1)
		return api.ErrorWithDetail(http.StatusConflict, "command_failed", device.Detail())
	case errors.Is(err, context.Canceled):
		// the caller went away: no server error, and likely no one reads this
		return api.Error(statusClientClosed, "canceled")
	}
	s.log.Error("command_error", "code", codeOf(err))
	return api.Error(http.StatusInternalServerError, "internal")
}
