package providers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "providers"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// catalogSegment is the path of the module catalog under /providers. No
// instance takes it, in any case (routes match without case): a route of one
// instance by name, GET /providers/{name}, would never reach it.
const catalogSegment = "modules"

type createBody struct {
	Name    string            `json:"name"`
	Module  string            `json:"module"`
	Config  json.RawMessage   `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

type createIn struct{ api.RawJSON }

type updateBody struct {
	Config       json.RawMessage   `json:"config"`
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clearSecrets"`
}

type updateIn struct {
	Name string `path:"name"`
	api.RawJSON
}

// a secret is answered {set: true} only, never its value
type instanceOut struct{ Body Instance }

type listOut struct{ Body []Instance }

type nameIn struct {
	Name string `path:"name"`
}

// error bodies are codes only: no message, never a config or secret value
func configInvalid(path string) error {
	return api.Error(http.StatusUnprocessableEntity, "config_invalid", path)
}

// RegisterAdmin mounts the instances, the module catalog and the test; log
// nil discards the test's lines.
func RegisterAdmin(a huma.API, s *Instances, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	registerCatalog(a, s, log)
	registerTest(a, s, log)
	huma.Register(a, huma.Operation{
		OperationID: "create-provider", Method: http.MethodPost, Path: "/providers", Tags: []string{tag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*instanceOut, error) {
		var b createBody
		if err := json.Unmarshal(in.Bytes(), &b); err != nil {
			return nil, configInvalid("")
		}
		if !nameRe.MatchString(b.Name) || strings.EqualFold(b.Name, catalogSegment) {
			return nil, configInvalid("/name")
		}
		inst, err := s.Create(ctx, b.Name, b.Module, b.Config, b.Secrets)
		var invalid *ConfigInvalidError
		switch {
		case errors.Is(err, ErrUnknownModule):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_module")
		case errors.Is(err, ErrNameTaken):
			return nil, api.Error(http.StatusConflict, "name_taken")
		case errors.As(err, &invalid):
			return nil, configInvalid(invalid.Path)
		case err != nil:
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &instanceOut{Body: inst}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "list-providers", Method: http.MethodGet, Path: "/providers", Tags: []string{tag},
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		list, err := s.List(ctx)
		if err != nil {
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &listOut{Body: list}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "update-provider", Method: http.MethodPatch, Path: "/providers/{name}", Tags: []string{tag},
	}, func(ctx context.Context, in *updateIn) (*instanceOut, error) {
		var b *updateBody
		if err := json.Unmarshal(in.Bytes(), &b); err != nil || b == nil {
			return nil, configInvalid("")
		}
		inst, err := s.Update(ctx, in.Name, b.Config, b.Secrets, b.ClearSecrets)
		var invalid *ConfigInvalidError
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrUnknownModule):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_module")
		case errors.As(err, &invalid):
			return nil, configInvalid(invalid.Path)
		case errors.Is(err, ErrBuildFailed):
			log.Error("provider_update_failed", append(instanceAttr(in.Name), "code", "build_failed")...)
			return nil, api.Error(http.StatusUnprocessableEntity, "build_failed")
		case err != nil:
			log.Error("provider_update_failed", append(instanceAttr(in.Name), "code", "internal")...)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &instanceOut{Body: inst}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-provider", Method: http.MethodDelete, Path: "/providers/{name}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *nameIn) (*struct{}, error) {
		switch err := s.Delete(ctx, in.Name); {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrInUse):
			return nil, api.Error(http.StatusConflict, "in_use")
		case err != nil:
			log.Error("provider_delete_failed", instanceAttr(in.Name)...)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &struct{}{}, nil
	})
}

// instanceAttr names the instance in a failure line when the name is one an
// instance can have, an id; any other path text is left out.
func instanceAttr(name string) []any {
	if nameRe.MatchString(name) {
		return []any{"instance", name}
	}
	return nil
}
