package settings

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "settings"

// a sparse set: the keys set in this scope, each with its raw value
type overridesOut struct{ Body Overrides }

type deviceIn struct {
	ID string `path:"id"`
}

// bodies are raw: unknown keys and wrong types reach parse, which answers
// settings_invalid for each
type putGlobalIn struct{ api.RawJSON }

type putDeviceIn struct {
	ID string `path:"id"`
	api.RawJSON
}

func reply(o Overrides, err error) (*overridesOut, error) {
	var invalid *InvalidError
	switch {
	case errors.As(err, &invalid):
		return nil, api.Error(http.StatusUnprocessableEntity, "settings_invalid", "/body/"+invalid.Key)
	case errors.Is(err, ErrNotFound):
		return nil, api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrInvalid):
		return nil, api.Error(http.StatusUnprocessableEntity, "settings_invalid")
	case err != nil:
		return nil, api.Error(http.StatusInternalServerError, "internal")
	}
	return &overridesOut{Body: o}, nil
}

func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "get-global-settings", Method: http.MethodGet, Path: "/settings/global", Tags: []string{tag},
	}, func(ctx context.Context, _ *struct{}) (*overridesOut, error) {
		return reply(s.Global(ctx))
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-global-settings", Method: http.MethodPut, Path: "/settings/global", Tags: []string{tag},
	}, func(ctx context.Context, in *putGlobalIn) (*overridesOut, error) {
		return reply(s.PutGlobal(ctx, in.Bytes()))
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-device-settings", Method: http.MethodGet, Path: "/devices/{id}/settings", Tags: []string{tag},
	}, func(ctx context.Context, in *deviceIn) (*overridesOut, error) {
		return reply(s.Device(ctx, in.ID))
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-device-settings", Method: http.MethodPut, Path: "/devices/{id}/settings", Tags: []string{tag},
	}, func(ctx context.Context, in *putDeviceIn) (*overridesOut, error) {
		return reply(s.PutDevice(ctx, in.ID, in.Bytes()))
	})
}
