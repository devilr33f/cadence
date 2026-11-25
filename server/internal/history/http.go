package history

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "history"

type listIn struct {
	DeviceID string `query:"deviceId" doc:"Only plays on this device; absent for the household."`
	Limit    int    `query:"limit" minimum:"1" maximum:"100" default:"50" doc:"The most plays the page holds."`
	Before   string `query:"before" doc:"The previous page's next cursor (started_at and id); absent for the newest."`
}

type deleteIn struct {
	ID int64 `path:"id" minimum:"1"`
}

type listOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         HistoryPage
}

var docs = map[string]api.Doc{
	"list-play-history":   {Summary: "List recently played items, newest first"},
	"delete-play-history": {Summary: "Delete one recently played item"},
}

func RegisterAdmin(a huma.API, s *Service, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	failed := func(op string) error {
		log.Error("history_api_failed", "op", op)
		return api.Error(http.StatusInternalServerError, "internal")
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-play-history", Method: http.MethodGet, Path: "/history", Tags: []string{tag},
	}, func(ctx context.Context, in *listIn) (*listOut, error) {
		page, err := s.List(ctx, in.DeviceID, in.Before, in.Limit)
		switch {
		case errors.Is(err, ErrCursor):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_cursor", "/query/before")
		case errors.Is(err, ErrDevice):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_device", "/query/deviceId")
		case err != nil:
			return nil, failed("list")
		}
		return &listOut{CacheControl: "no-store", Body: page}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-play-history", Method: http.MethodDelete, Path: "/history/{id}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *deleteIn) (*struct{}, error) {
		err := s.Delete(ctx, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("delete")
		}
		return &struct{}{}, nil
	})
}
