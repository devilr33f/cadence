package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "devices"

type deviceIn struct {
	ID string `path:"id"`
}

const statusPending = "pending"

type deleteIn struct {
	ID     string `path:"id"`
	Status string `query:"status" enum:"pending" doc:"Delete the device only while it is pending: one enrolled by then answers 409 not_pending, and nothing is deleted."`
}

type detailOut struct{ Body Detail }

// RenameDeviceRequest names a device anew.
type RenameDeviceRequest struct {
	Name string `json:"name" minLength:"1" doc:"trimmed of white space, then 1–64 characters with no control or format character and no line or paragraph separator; no other device's name, in any case"`
}

type renameIn struct {
	ID   string `path:"id"`
	Body RenameDeviceRequest
}

// SetRoomRequest puts a device in a room.
type SetRoomRequest struct {
	Room string `json:"room" maxLength:"256" doc:"the room's name, trimmed of white space, then up to 64 characters with no control or format character and no line or paragraph separator; empty for none. Devices with the same room are in one room"`
}

type roomIn struct {
	ID   string `path:"id"`
	Body SetRoomRequest
}

type configIn struct {
	ID         string `path:"id"`
	Capability string `path:"cap"`
	api.RawJSON
}

type configOut struct {
	Body struct {
		Generation int64 `json:"generation"`
	}
}

type secretIn struct {
	ID         string `path:"id"`
	Capability string `path:"cap"`
	Name       string `path:"name"`
	api.RawJSON
}

type secretDeleteIn struct {
	ID         string `path:"id"`
	Capability string `path:"cap"`
	Name       string `path:"name"`
}

// error bodies are codes only, never the config or secret value
func secretFailure(s *Service, err error) error {
	switch {
	case errors.Is(err, ErrUnknownCapability):
		return api.Error(http.StatusNotFound, "unknown_capability")
	case errors.Is(err, ErrUnknownSecret):
		return api.Error(http.StatusNotFound, "unknown_secret")
	case errors.Is(err, ErrSecretInvalid):
		return api.Error(http.StatusUnprocessableEntity, "secret_invalid")
	case errors.Is(err, ErrConfigTooLarge):
		return api.Error(http.StatusRequestEntityTooLarge, "config_too_large")
	}
	s.log.Error("device_secret_failed")
	return api.Error(http.StatusInternalServerError, "internal")
}

func RegisterAdmin(a huma.API, s *Service) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "get-device", Method: http.MethodGet, Path: "/devices/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *deviceIn) (*detailOut, error) {
		d, err := s.Detail(ctx, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			s.log.Error("device_detail_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &detailOut{Body: s.withGroup(ctx, d)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "rename-device", Method: http.MethodPatch, Path: "/devices/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *renameIn) (*detailOut, error) {
		err := s.Rename(ctx, in.ID, in.Body.Name)
		if err == nil {
			d, err := s.Detail(ctx, in.ID)
			switch {
			case err == nil:
				return &detailOut{Body: s.withGroup(ctx, d)}, nil
			case errors.Is(err, ErrNotFound):
				return nil, api.Error(http.StatusNotFound, "not_found")
			}
			// renamed: only the answer failed
			s.log.Error("device_detail_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrNameInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/name")
		case errors.Is(err, ErrNameTaken):
			return nil, api.Error(http.StatusConflict, "name_taken")
		}
		s.log.Error("device_rename_failed")
		return nil, api.Error(http.StatusInternalServerError, "internal")
	})
	huma.Register(a, huma.Operation{
		OperationID: "set-device-room", Method: http.MethodPut, Path: "/devices/{id}/room", Tags: []string{tag},
	}, func(ctx context.Context, in *roomIn) (*detailOut, error) {
		err := s.SetRoom(ctx, in.ID, in.Body.Room)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrRoomInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", "/body/room")
		case err != nil:
			s.log.Error("device_room_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		d, err := s.Detail(ctx, in.ID)
		switch {
		case err == nil:
			return &detailOut{Body: s.withGroup(ctx, d)}, nil
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		}
		s.log.Error("device_detail_failed")
		return nil, api.Error(http.StatusInternalServerError, "internal")
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-device", Method: http.MethodDelete, Path: "/devices/{id}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *deleteIn) (*struct{}, error) {
		del := s.Delete
		if in.Status == statusPending {
			del = s.DeletePending
		}
		err := del(ctx, in.ID)
		switch {
		case err == nil:
			return &struct{}{}, nil
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrNotPending):
			return nil, api.Error(http.StatusConflict, "not_pending")
		}
		s.log.Error("device_delete_failed")
		return nil, api.Error(http.StatusInternalServerError, "internal")
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-device-secret", Method: http.MethodPut, Path: "/devices/{id}/capabilities/{cap}/secrets/{name}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *secretIn) (*struct{}, error) {
		var body struct {
			Value *string `json:"value"`
		}
		// a bad body reaches SetSecret as an empty value, so an unknown name is a 404 first
		value := ""
		if json.Unmarshal(in.Bytes(), &body) == nil && body.Value != nil {
			value = *body.Value
		}
		if err := s.SetSecret(ctx, in.ID, in.Capability, in.Name, value); err != nil {
			return nil, secretFailure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-device-secret", Method: http.MethodDelete, Path: "/devices/{id}/capabilities/{cap}/secrets/{name}",
		Tags: []string{tag}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *secretDeleteIn) (*struct{}, error) {
		if err := s.DeleteSecret(ctx, in.ID, in.Capability, in.Name); err != nil {
			return nil, secretFailure(s, err)
		}
		return &struct{}{}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "configure-device-capability", Method: http.MethodPost, Path: "/devices/{id}/capabilities/{cap}/configure",
		Tags: []string{tag},
	}, func(ctx context.Context, in *configIn) (*configOut, error) {
		// read by hand: a validation error must never quote a secret back
		var body struct {
			Config  json.RawMessage   `json:"config"`
			Secrets map[string]string `json:"secrets"`
		}
		if err := json.Unmarshal(in.Bytes(), &body); err != nil || len(body.Config) == 0 {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid")
		}
		gen, err := s.Configure(ctx, in.ID, in.Capability, body.Config, body.Secrets)
		var invalid *ConfigInvalidError
		switch {
		case errors.As(err, &invalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "config_invalid", invalid.Path)
		case err != nil:
			return nil, secretFailure(s, err)
		}
		out := &configOut{}
		out.Body.Generation = gen
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-device-config", Method: http.MethodPut, Path: "/devices/{id}/capabilities/{cap}/config",
		Tags: []string{tag},
	}, func(ctx context.Context, in *configIn) (*configOut, error) {
		gen, err := s.SetConfig(ctx, in.ID, in.Capability, in.Bytes())
		var invalid *ConfigInvalidError
		switch {
		case errors.Is(err, ErrUnknownCapability):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrConfigTooLarge):
			return nil, api.Error(http.StatusRequestEntityTooLarge, "config_too_large")
		case errors.As(err, &invalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "config_invalid", invalid.Path)
		case err != nil:
			s.log.Error("device_config_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &configOut{}
		out.Body.Generation = gen
		return out, nil
	})
}
