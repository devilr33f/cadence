package modes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "modes"

type nameIn struct {
	Name string `path:"name"`
}

// a mode created before the rule may have another name: only a put holds to it
type putIn struct {
	Name string `path:"name" pattern:"^[a-z][a-z0-9_-]{0,31}$"`
	Body PutModeRequest
}

type modeOut struct{ Body ModeView }

type listOut struct{ Body []ModeView }

// RegisterAdmin mounts the modes; log nil discards the failure lines, which
// carry the name alone.
func RegisterAdmin(a huma.API, m *agent.Modes, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-modes", Method: http.MethodGet, Path: "/modes", Tags: []string{tag},
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		list, err := m.List(ctx)
		if err != nil {
			log.Error("mode_read_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := &listOut{Body: make([]ModeView, 0, len(list))}
		for _, mode := range list {
			out.Body = append(out.Body, view(mode))
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-mode", Method: http.MethodGet, Path: "/modes/{name}", Tags: []string{tag},
	}, func(ctx context.Context, in *nameIn) (*modeOut, error) {
		mode, err := m.Get(ctx, in.Name)
		switch {
		case errors.Is(err, agent.ErrModeNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			log.Error("mode_read_failed")
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &modeOut{Body: view(mode)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-mode", Method: http.MethodPut, Path: "/modes/{name}", Tags: []string{tag},
	}, func(ctx context.Context, in *putIn) (*modeOut, error) {
		mode, path := modeOf(in.Name, in.Body)
		if path != "" {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", path)
		}
		_, err := m.Put(ctx, mode)
		var taken *agent.AliasTakenError
		switch {
		case errors.As(err, &taken) && taken.Lang == "":
			return nil, api.Error(http.StatusConflict, "alias_taken", "/path/name")
		case errors.As(err, &taken):
			return nil, api.Error(http.StatusConflict, "alias_taken", fmt.Sprintf("/body/aliases/%s/%d", taken.Lang, taken.Index))
		case err != nil:
			log.Error("mode_save_failed", "name", in.Name)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		// what was stored, as get-mode answers it
		return &modeOut{Body: view(mode)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-mode", Method: http.MethodDelete, Path: "/modes/{name}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *nameIn) (*struct{}, error) {
		err := m.Delete(ctx, in.Name)
		switch {
		case err == nil:
			return &struct{}{}, nil
		case errors.Is(err, agent.ErrModeNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, agent.ErrModeInUse):
			return nil, api.Error(http.StatusConflict, "in_use")
		}
		log.Error("mode_delete_failed", "name", in.Name)
		return nil, api.Error(http.StatusInternalServerError, "internal")
	})
}
