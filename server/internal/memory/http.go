package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	tag = "memory"

	defaultPage = 50
	maxPage     = 200
	defaultLog  = 20
)

// fail maps an error to its response; a 5xx logs the operation only.
func fail(a *Admin, op string, err error) error {
	var bad *InvalidError
	switch {
	case errors.As(err, &bad):
		return api.Error(http.StatusUnprocessableEntity, bad.Code, bad.Path)
	case errors.Is(err, ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrConflict):
		return api.Error(http.StatusConflict, "conflict")
	case errors.Is(err, ErrInvalid):
		return api.Error(http.StatusUnprocessableEntity, codeInvalid, "")
	}
	a.d.Log.Error("memory_admin_failed", "op", op)
	return api.Error(http.StatusInternalServerError, "internal")
}

// decode reads a strict JSON body: unknown fields and wrong types are invalid.
func decode(body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return invalid("")
	}
	if dec.More() {
		return invalid("")
	}
	return nil
}

type memoryJSON struct {
	ID         string     `json:"id"`
	Scope      string     `json:"scope"`
	DeviceID   *string    `json:"deviceId"`
	Kind       string     `json:"kind"`
	Text       string     `json:"text"`
	Pinned     bool       `json:"pinned"`
	Hits       int        `json:"hits"`
	LastHitAt  *time.Time `json:"lastHitAt"`
	Confidence float32    `json:"confidence"`
	Evidence   []int64    `json:"evidence"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	Embedded   bool       `json:"embedded"`
	Replaces   string     `json:"replaces,omitempty"`
	Proposal   string     `json:"proposal,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func toJSON(m Memory) memoryJSON {
	out := memoryJSON{
		ID: m.ID, Scope: m.Scope, Kind: m.Kind, Text: m.Text, Pinned: m.Pinned, Hits: m.Hits, LastHitAt: m.LastHitAt,
		Confidence: m.Confidence, Evidence: m.Evidence, Source: m.Source, Status: m.Status, Embedded: m.HasEmbedding,
		Replaces: m.Replaces, Proposal: m.Proposal, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if m.DeviceID != "" {
		out.DeviceID = &m.DeviceID
	}
	if out.Evidence == nil {
		out.Evidence = []int64{}
	}
	return out
}

type compactionJSON struct {
	ID            string    `json:"id"`
	UptoMessageID int64     `json:"uptoMessageId"`
	Summary       string    `json:"summary"`
	OpenThreads   []string  `json:"openThreads"`
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"createdAt"`
}

type runJSON struct {
	ID           string    `json:"id"`
	CompactionID string    `json:"compactionId"`
	Applied      int       `json:"applied"`
	Dropped      int       `json:"dropped"`
	Pending      int       `json:"pending"`
	MemoryIDs    []string  `json:"memoryIds"`
	CreatedAt    time.Time `json:"createdAt"`
}

// page reads limit (at least 1, clamped to maxPage) and offset.
func page(q url.Values, def int, withOffset bool) (limit, offset int, err error) {
	limit = def
	if v := q.Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return 0, 0, invalid("limit")
		}
		limit = min(n, maxPage)
	}
	if v := q.Get("offset"); withOffset && v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			return 0, 0, invalid("offset")
		}
		offset = n
	}
	return limit, offset, nil
}

func listFilter(q url.Values) (Filter, error) {
	f := Filter{Scope: q.Get("scope"), DeviceID: q.Get("deviceId"), Kind: q.Get("kind"), Status: q.Get("status")}
	switch f.Scope {
	case "", ScopeGlobal, ScopeDevice:
	default:
		return f, invalid("scope")
	}
	if f.Kind != "" {
		if err := validKind(f.Kind); err != nil {
			return f, err
		}
	}
	switch f.Status {
	case "", StatusActive, StatusPending:
	default:
		return f, invalid("status")
	}
	if f.DeviceID != "" {
		if _, err := parseUUID(f.DeviceID); err != nil {
			return f, invalid("deviceId")
		}
	}
	if v := q.Get("pinned"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil || (v != "true" && v != "false") {
			return f, invalid("pinned")
		}
		f.Pinned = &b
	}
	return f, nil
}

type listIn struct{ api.RawQuery }

type createIn struct{ api.RawJSON }

type editIn struct {
	ID string `path:"id"`
	api.RawJSON
}

type idIn struct {
	ID string `path:"id"`
}

type deviceIn struct {
	ID string `path:"id"`
	api.RawQuery
}

type memoryOut struct{ Body memoryJSON }

type memoriesOut struct{ Body []memoryJSON }

// approveOut is a 204 when there was nothing to approve
type approveOut struct {
	Status int
	Body   *memoryJSON
}

type compactionsOut struct{ Body []compactionJSON }

type runsOut struct{ Body []runJSON }

// RegisterAdmin registers the memory operations on the admin api.
func RegisterAdmin(h huma.API, a *Admin) {
	defer api.Describe(h, docs)
	huma.Register(h, huma.Operation{
		OperationID: "list-memories", Method: http.MethodGet, Path: "/memories", Tags: []string{tag},
	}, func(ctx context.Context, in *listIn) (*memoriesOut, error) {
		q := in.Values()
		f, err := listFilter(q)
		if err != nil {
			return nil, fail(a, "list", err)
		}
		limit, offset, err := page(q, defaultPage, true)
		if err != nil {
			return nil, fail(a, "list", err)
		}
		ms, err := a.List(ctx, f, limit, offset)
		if err != nil {
			return nil, fail(a, "list", err)
		}
		out := make([]memoryJSON, len(ms))
		for i, m := range ms {
			out[i] = toJSON(m)
		}
		return &memoriesOut{Body: out}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "create-memory", Method: http.MethodPost, Path: "/memories", Tags: []string{tag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*memoryOut, error) {
		var body struct {
			Scope    string `json:"scope"`
			DeviceID string `json:"deviceId"`
			Kind     string `json:"kind"`
			Text     string `json:"text"`
		}
		if err := decode(in.Bytes(), &body); err != nil {
			return nil, fail(a, "create", err)
		}
		m, err := a.Create(ctx, Create(body))
		if err != nil {
			return nil, fail(a, "create", err)
		}
		return &memoryOut{Body: toJSON(m)}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "edit-memory", Method: http.MethodPatch, Path: "/memories/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *editIn) (*memoryOut, error) {
		var body struct {
			Text     *string `json:"text"`
			Kind     *string `json:"kind"`
			Scope    *string `json:"scope"`
			DeviceID *string `json:"deviceId"`
			Pinned   *bool   `json:"pinned"`
		}
		if err := decode(in.Bytes(), &body); err != nil {
			// an unknown id is a 404 even with a bad body
			if _, gerr := a.d.Store.Get(ctx, in.ID); errors.Is(gerr, ErrNotFound) {
				return nil, fail(a, "edit", gerr)
			}
			return nil, fail(a, "edit", err)
		}
		m, err := a.Edit(ctx, in.ID, Edit(body))
		if err != nil {
			return nil, fail(a, "edit", err)
		}
		return &memoryOut{Body: toJSON(m)}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "delete-memory", Method: http.MethodDelete, Path: "/memories/{id}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := a.Delete(ctx, in.ID); err != nil {
			return nil, fail(a, "delete", err)
		}
		return &struct{}{}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "approve-memory", Method: http.MethodPost, Path: "/memories/{id}/approve", Tags: []string{tag},
	}, func(ctx context.Context, in *idIn) (*approveOut, error) {
		m, ok, err := a.Approve(ctx, in.ID)
		if err != nil {
			return nil, fail(a, "approve", err)
		}
		if !ok {
			return &approveOut{Status: http.StatusNoContent}, nil
		}
		j := toJSON(m)
		return &approveOut{Status: http.StatusOK, Body: &j}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "reject-memory", Method: http.MethodPost, Path: "/memories/{id}/reject", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		if err := a.Reject(ctx, in.ID); err != nil {
			return nil, fail(a, "reject", err)
		}
		return &struct{}{}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "list-device-compactions", Method: http.MethodGet, Path: "/devices/{id}/compactions", Tags: []string{tag},
	}, func(ctx context.Context, in *deviceIn) (*compactionsOut, error) {
		limit, _, err := page(in.Values(), defaultLog, false)
		if err != nil {
			return nil, fail(a, "compactions", err)
		}
		cs, err := a.Compactions(ctx, in.ID, limit)
		if err != nil {
			return nil, fail(a, "compactions", err)
		}
		out := make([]compactionJSON, len(cs))
		for i, cp := range cs {
			threads := cp.Threads()
			if threads == nil {
				threads = []string{}
			}
			out[i] = compactionJSON{
				ID: cp.ID, UptoMessageID: cp.UptoMessageID, Summary: cp.Summary, OpenThreads: threads, Model: cp.Model, CreatedAt: cp.CreatedAt,
			}
		}
		return &compactionsOut{Body: out}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "list-device-learning", Method: http.MethodGet, Path: "/devices/{id}/learning", Tags: []string{tag},
	}, func(ctx context.Context, in *deviceIn) (*runsOut, error) {
		limit, _, err := page(in.Values(), defaultLog, false)
		if err != nil {
			return nil, fail(a, "learning", err)
		}
		runs, err := a.Runs(ctx, in.ID, limit)
		if err != nil {
			return nil, fail(a, "learning", err)
		}
		out := make([]runJSON, len(runs))
		for i, ru := range runs {
			out[i] = runJSON{
				ID: ru.ID, CompactionID: ru.CompactionID, Applied: ru.Applied, Dropped: ru.Dropped, Pending: ru.Pending,
				MemoryIDs: ru.MemoryIDs, CreatedAt: ru.CreatedAt,
			}
		}
		return &runsOut{Body: out}, nil
	})
}
