package conversation

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const tag = "conversations"

// noStore keeps transcripts out of every cache (rule 8)
const noStore = "no-store"

type turnsIn struct {
	ID     string `path:"id"`
	Before string `query:"before" doc:"the next of the page before, for the turns after it; absent for the newest"`
	Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"the most turns the page holds"`
}

type turnIn struct {
	ID     string `path:"id"`
	TurnID string `path:"turnId"`
}

type devicePathIn struct {
	ID string `path:"id"`
}

type pageOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         TurnPage
}

type detailOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         TurnDetail
}

type resetOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Reset bool `json:"reset"`
	}
}

type compactOut struct {
	Status       int
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Accepted bool `json:"accepted"`
	}
}

// RegisterAdmin mounts a device's conversation history and its reset/compact
// actions, admin's alone. The handlers log no content: a failed read or write
// logs its operation, and log nil discards even that.
func RegisterAdmin(a huma.API, s *Store, actions *Resetter, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	failed := func(op string) error {
		log.Error("conversation_read_failed", "op", op)
		return api.Error(http.StatusInternalServerError, "internal")
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-device-turns", Method: http.MethodGet, Path: "/devices/{id}/turns", Tags: []string{tag},
	}, func(ctx context.Context, in *turnsIn) (*pageOut, error) {
		turns, next, err := s.Turns(ctx, in.ID, in.Before, in.Limit)
		switch {
		case errors.Is(err, ErrCursor):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_cursor", "/query/before")
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("list")
		}
		out := &pageOut{CacheControl: noStore, Body: TurnPage{Turns: make([]TurnView, len(turns)), Next: next}}
		for i, t := range turns {
			out.Body.Turns[i] = summarize(t)
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-device-turn", Method: http.MethodGet, Path: "/devices/{id}/turns/{turnId}", Tags: []string{tag},
	}, func(ctx context.Context, in *turnIn) (*detailOut, error) {
		t, err := s.Turn(ctx, in.ID, in.TurnID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("get")
		}
		return &detailOut{CacheControl: noStore, Body: detailOf(t)}, nil
	})
	actionFailed := func(op string) error {
		log.Error("conversation_action_failed", "op", op)
		return api.Error(http.StatusInternalServerError, "internal")
	}
	huma.Register(a, huma.Operation{
		OperationID: "reset-device-conversation", Method: http.MethodPost, Path: "/devices/{id}/conversation/reset", Tags: []string{tag},
	}, func(ctx context.Context, in *devicePathIn) (*resetOut, error) {
		if actions == nil {
			return nil, actionFailed("reset")
		}
		_, err := actions.Reset(ctx, in.ID, nil)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, actionFailed("reset")
		}
		out := &resetOut{CacheControl: noStore}
		out.Body.Reset = true
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "compact-device-conversation", Method: http.MethodPost, Path: "/devices/{id}/conversation/compact", Tags: []string{tag},
	}, func(ctx context.Context, in *devicePathIn) (*compactOut, error) {
		if actions == nil {
			return nil, actionFailed("compact")
		}
		started, err := actions.CompactNow(ctx, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, actionFailed("compact")
		case !started:
			return nil, api.Error(http.StatusConflict, "busy")
		}
		out := &compactOut{Status: http.StatusAccepted, CacheControl: noStore}
		out.Body.Accepted = true
		return out, nil
	})
}
