package skills

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "skills"

var docs = map[string]api.Doc{
	"list-skills":  {Summary: "List the agent skills by name: the built-in ones and the owner's"},
	"get-skill":    {Summary: "Get an agent skill"},
	"put-skill":    {Summary: "Create or replace an owner skill; one named as a built-in replaces it. Answers it as get-skill does"},
	"delete-skill": {Summary: "Delete an owner skill; a built-in it replaced is back. A built-in alone is 409 builtin: switch it off with a put instead"},
}

type nameIn struct {
	Name string `path:"name"`
}

type putIn struct {
	Name string `path:"name" pattern:"^[a-z][a-z0-9_-]{0,47}$"`
	Body PutSkillRequest
}

type skillOut struct{ Body SkillView }

type listOut struct{ Body []SkillView }

// RegisterAdmin mounts the skills; log nil discards the failure lines, which
// carry the name alone.
func RegisterAdmin(a huma.API, l *Library, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-skills", Method: http.MethodGet, Path: "/skills", Tags: []string{tag},
	}, func(context.Context, *struct{}) (*listOut, error) {
		all := l.All()
		out := &listOut{Body: make([]SkillView, 0, len(all))}
		for _, e := range all {
			out.Body = append(out.Body, view(e))
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-skill", Method: http.MethodGet, Path: "/skills/{name}", Tags: []string{tag},
	}, func(_ context.Context, in *nameIn) (*skillOut, error) {
		e, ok := l.Get(in.Name)
		if !ok {
			return nil, api.Error(http.StatusNotFound, "not_found")
		}
		return &skillOut{Body: view(e)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "put-skill", Method: http.MethodPut, Path: "/skills/{name}", Tags: []string{tag},
	}, func(ctx context.Context, in *putIn) (*skillOut, error) {
		e, path := entryOf(in.Name, in.Body)
		if path != "" {
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid", path)
		}
		got, _, err := l.Put(ctx, e)
		if err != nil {
			log.Error("skill_save_failed", "name", in.Name)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		return &skillOut{Body: view(got)}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-skill", Method: http.MethodDelete, Path: "/skills/{name}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *nameIn) (*struct{}, error) {
		err := l.Delete(ctx, in.Name)
		switch {
		case err == nil:
			return &struct{}{}, nil
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrBuiltin):
			return nil, api.Error(http.StatusConflict, "builtin")
		}
		log.Error("skill_delete_failed", "name", in.Name)
		return nil, api.Error(http.StatusInternalServerError, "internal")
	})
}
