package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	tag = "tasks"

	defaultPage = 50
	maxPage     = 200
	maxMessage  = 2000
)

// conflictCodes maps the kind codes that mean the answer does not fit the
// task's state to the one name the dashboard sees for that condition (409).
// Every other kind code is a 502.
var conflictCodes = map[string]string{
	"hermes_busy": "busy", "hermes_prompt_unshowable": "prompt_unshowable", "hermes_no_question": "no_question",
	"hermes_approval_pending": "approval_pending", "hermes_no_approval": "no_approval", "hermes_secret_prompt": "secret_prompt",
	"hermes_finished": "finished", "hermes_conflict": "conflict", "hermes_gone": "gone", "hermes_not_found": "gone",
	"hermes_capacity": "capacity",
}

// KindStatus is one task kind as the dashboard sees it: Instance is what the
// running kind was built with at startup (empty: disabled), Configured the
// current global setting. They differ until a restart applies the setting.
type KindStatus struct{ Name, Instance, Configured string }

type AdminDeps struct {
	Manager *Manager
	Kinds   func(ctx context.Context) ([]KindStatus, error)
	Log     *slog.Logger
}

// Admin is the dashboard's way to read and answer tasks (M5b-D9).
type Admin struct{ d AdminDeps }

func NewAdmin(d AdminDeps) *Admin {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Admin{d: d}
}

func badInput(path string) error { return api.Error(http.StatusUnprocessableEntity, "invalid", path) }

// fail maps an error to its response; a 5xx logs the operation only.
func (a *Admin) fail(op string, err error) error {
	var ke *KindError
	switch {
	case errors.Is(err, ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrNotActive):
		return api.Error(http.StatusConflict, "not_active")
	case errStale(err):
		return api.Error(http.StatusConflict, "stale")
	case errors.Is(err, ErrNoApproval):
		return api.Error(http.StatusConflict, "no_approval")
	case errors.As(err, &ke):
		if name, ok := conflictCodes[ke.Code]; ok {
			return api.Error(http.StatusConflict, name)
		}
		return api.Error(http.StatusBadGateway, "kind_failed")
	case errors.Is(err, ErrUnknownKind):
		return api.Error(http.StatusBadGateway, "kind_unavailable")
	}
	a.d.Log.Error("task_admin_failed", "op", op)
	return api.Error(http.StatusInternalServerError, "internal")
}

// badBody is a malformed body. An unknown id is a 404 even then.
func (a *Admin) badBody(ctx context.Context, id string) error {
	switch _, err := a.d.Manager.Get(ctx, id); {
	case errors.Is(err, ErrNotFound):
		return api.Error(http.StatusNotFound, "not_found")
	case err != nil:
		return a.fail("body", err)
	}
	return badInput("")
}

// decode reads a strict JSON body: unknown fields, wrong types and trailing
// data are invalid.
func decode(body []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	var rest json.RawMessage
	return dec.Decode(&rest) == io.EOF
}

// questionFields are what a task question answers.
type questionFields struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Options []string `json:"options" nullable:"false"`
	Token   string   `json:"token"`
}

// questionJSON is a task's open question.
type questionJSON questionFields

// Schema is the question's object, null when the task asks nothing (huma
// takes no nullable object by reference, so it is written in place).
func (questionJSON) Schema(r huma.Registry) *huma.Schema {
	s := huma.SchemaFromType(r, reflect.TypeFor[questionFields]())
	s.Nullable = true
	s.Description = "the open question, null when the task asks nothing"
	return s
}

// TaskJSON is a task as the list and the detail answer it; exported, so the
// document takes in the fields the detail embeds.
type TaskJSON struct {
	ShortID    string        `json:"shortId"`
	Kind       string        `json:"kind"`
	Title      string        `json:"title"`
	DeviceID   *string       `json:"deviceId"`
	Status     string        `json:"status"`
	Progress   string        `json:"progress"`
	Question   *questionJSON `json:"question"`
	ErrorCode  string        `json:"errorCode"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
	FinishedAt *time.Time    `json:"finishedAt"`
	ReportedAt *time.Time    `json:"reportedAt"`
}

func toQuestion(q *Question) *questionJSON {
	if q == nil {
		return nil
	}
	out := &questionJSON{Kind: q.Kind, Text: q.Text, Options: q.Options, Token: q.Token}
	if out.Options == nil {
		out.Options = []string{}
	}
	return out
}

func toJSON(t Task) TaskJSON {
	out := TaskJSON{
		ShortID: t.ShortID, Kind: t.Kind, Title: t.Title, Status: t.Status, Progress: t.Progress, ErrorCode: t.ErrorCode,
		Question: toQuestion(t.Question), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, FinishedAt: t.FinishedAt, ReportedAt: t.ReportedAt,
	}
	if t.Origin != uuid.Nil {
		id := t.Origin.String()
		out.DeviceID = &id
	}
	return out
}

type eventJSON struct {
	Seq      int       `json:"seq"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Decision string    `json:"decision,omitempty"`
	Via      string    `json:"via,omitempty"`
	Device   string    `json:"device,omitempty"`
	Words    string    `json:"words,omitempty"`
}

type noticeJSON struct {
	EventSeq    int        `json:"eventSeq"`
	Text        string     `json:"text"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt"`
}

type detailJSON struct {
	TaskJSON
	Result  string       `json:"result"`
	Events  []eventJSON  `json:"events"`
	Notices []noticeJSON `json:"notices"`
}

func toDetail(h History) detailJSON {
	out := detailJSON{TaskJSON: toJSON(h.Task), Result: h.Task.Result, Events: []eventJSON{}, Notices: []noticeJSON{}}
	for _, e := range h.Events {
		ev := eventJSON{Seq: e.Seq, Kind: e.Kind, At: e.At}
		if len(e.Answer) > 0 {
			var ans struct {
				Decision string `json:"decision"`
				Via      string `json:"via"`
				Device   string `json:"device"`
				Words    string `json:"words"`
			}
			if json.Unmarshal(e.Answer, &ans) == nil {
				ev.Decision, ev.Via, ev.Device, ev.Words = ans.Decision, ans.Via, ans.Device, ans.Words
			}
		}
		out.Events = append(out.Events, ev)
	}
	for _, n := range h.Notices {
		out.Notices = append(out.Notices, noticeJSON(n))
	}
	return out
}

type kindJSON struct {
	Name       string  `json:"name"`
	Instance   *string `json:"instance"`
	Configured *string `json:"configured"`
}

type inspectJSON struct {
	Status     string        `json:"status"`
	Progress   string        `json:"progress"`
	Question   *questionJSON `json:"question"`
	Result     string        `json:"result"`
	ErrorCode  string        `json:"errorCode"`
	Transcript *string       `json:"transcript,omitempty"`
}

// listFilter reads the list query; bad names the invalid parameter.
func listFilter(q url.Values) (f Filter, bad string) {
	f = Filter{Kind: q.Get("kind"), Limit: defaultPage}
	if len([]rune(f.Kind)) > 64 {
		return f, "kind"
	}
	if q.Has("status") {
		for s := range strings.SplitSeq(q.Get("status"), ",") {
			switch s {
			case StatusQueued, StatusRunning, StatusWaitingInput, StatusDone, StatusFailed, StatusCancelled, StatusUnknown:
				f.Statuses = append(f.Statuses, s)
			default:
				return f, "status"
			}
		}
	}
	if v := q.Get("device"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil || id == uuid.Nil {
			return f, "device"
		}
		f.Origin = id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			return f, "limit"
		}
		f.Limit = n
	}
	return f, ""
}

type (
	noIn    struct{}
	queryIn struct{ api.RawQuery }
	idIn    struct {
		ID string `path:"id"`
	}
	inspectIn struct {
		ID string `path:"id"`
		api.RawQuery
	}
	bodyIn struct {
		ID string `path:"id"`
		api.RawJSON
	}

	kindsOut   struct{ Body []kindJSON }
	tasksOut   struct{ Body []TaskJSON }
	taskOut    struct{ Body TaskJSON }
	detailOut  struct{ Body detailJSON }
	inspectOut struct{ Body inspectJSON }
)

// RegisterAdmin registers the task operations on the admin api.
func RegisterAdmin(h huma.API, a *Admin) {
	defer api.Describe(h, docs)
	// before /tasks/{id}, which would take "kinds" for an id
	huma.Register(h, huma.Operation{
		OperationID: "list-task-kinds", Method: http.MethodGet, Path: "/tasks/kinds", Tags: []string{tag},
	}, func(ctx context.Context, _ *noIn) (*kindsOut, error) {
		var ks []KindStatus
		if a.d.Kinds != nil {
			var err error
			if ks, err = a.d.Kinds(ctx); err != nil {
				return nil, a.fail("kinds", err)
			}
		}
		out := make([]kindJSON, len(ks))
		for i, k := range ks {
			out[i] = kindJSON{Name: k.Name}
			if k.Instance != "" {
				out[i].Instance = &ks[i].Instance
			}
			if k.Configured != "" {
				out[i].Configured = &ks[i].Configured
			}
		}
		return &kindsOut{Body: out}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "list-tasks", Method: http.MethodGet, Path: "/tasks", Tags: []string{tag},
	}, func(ctx context.Context, in *queryIn) (*tasksOut, error) {
		f, bad := listFilter(in.Values())
		if bad != "" {
			return nil, badInput(bad)
		}
		ts, err := a.d.Manager.List(ctx, f)
		if err != nil {
			return nil, a.fail("list", err)
		}
		out := make([]TaskJSON, len(ts))
		for i, t := range ts {
			out[i] = toJSON(t)
		}
		return &tasksOut{Body: out}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "get-task", Method: http.MethodGet, Path: "/tasks/{id}", Tags: []string{tag},
	}, func(ctx context.Context, in *idIn) (*detailOut, error) {
		hist, err := a.d.Manager.History(ctx, in.ID)
		if err != nil {
			return nil, a.fail("get", err)
		}
		return &detailOut{Body: toDetail(hist)}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "inspect-task", Method: http.MethodGet, Path: "/tasks/{id}/inspect", Tags: []string{tag},
	}, func(ctx context.Context, in *inspectIn) (*inspectOut, error) {
		d := DetailSummary
		switch in.Values().Get("detail") {
		case "", string(DetailSummary):
		case string(DetailTranscript):
			d = DetailTranscript
		default:
			return nil, badInput("detail")
		}
		s, err := a.d.Manager.Inspect(ctx, in.ID, d)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrInspect), errors.Is(err, ErrUnknownKind):
			return nil, api.Error(http.StatusBadGateway, "kind_unavailable")
		case err != nil:
			return nil, a.fail("inspect", err)
		}
		out := inspectJSON{
			Status: s.Status, Progress: s.Progress, Question: toQuestion(s.Question), Result: s.Result, ErrorCode: s.ErrorCode,
		}
		if d == DetailTranscript {
			out.Transcript = &s.Transcript
		}
		return &inspectOut{Body: out}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "cancel-task", Method: http.MethodPost, Path: "/tasks/{id}/cancel", Tags: []string{tag},
	}, func(ctx context.Context, in *idIn) (*taskOut, error) {
		t, err := a.d.Manager.Cancel(ctx, in.ID)
		if err != nil {
			return nil, a.fail("cancel", err)
		}
		return &taskOut{Body: toJSON(t)}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "reply-to-task", Method: http.MethodPost, Path: "/tasks/{id}/reply", Tags: []string{tag},
	}, func(ctx context.Context, in *bodyIn) (*taskOut, error) {
		var body struct {
			Message string `json:"message"`
			Token   string `json:"token"`
		}
		if !decode(in.Bytes(), &body) {
			return nil, a.badBody(ctx, in.ID)
		}
		if body.Token == "" {
			return nil, badInput("token")
		}
		if n := len([]rune(body.Message)); n < 1 || n > maxMessage {
			return nil, badInput("message")
		}
		t, err := a.d.Manager.Get(ctx, in.ID)
		if err != nil {
			return nil, a.fail("reply", err)
		}
		switch {
		case t.Terminal():
			return nil, api.Error(http.StatusConflict, "not_active")
		case t.Status != StatusWaitingInput || t.Question == nil:
			return nil, api.Error(http.StatusConflict, "no_question")
		case t.Question.Kind == QuestionApproval:
			return nil, api.Error(http.StatusConflict, "approval_pending")
		}
		t, err = a.d.Manager.Send(ctx, t.ShortID, body.Token, body.Message)
		if err != nil {
			return nil, a.fail("reply", err)
		}
		return &taskOut{Body: toJSON(t)}, nil
	})
	huma.Register(h, huma.Operation{
		OperationID: "approve-task", Method: http.MethodPost, Path: "/tasks/{id}/approve", Tags: []string{tag},
	}, func(ctx context.Context, in *bodyIn) (*taskOut, error) {
		var body struct {
			Decision string `json:"decision"`
			Token    string `json:"token"`
		}
		if !decode(in.Bytes(), &body) {
			return nil, a.badBody(ctx, in.ID)
		}
		if body.Token == "" {
			return nil, badInput("token")
		}
		if body.Decision != DecisionOnce && body.Decision != DecisionDeny {
			return nil, badInput("decision")
		}
		t, err := a.d.Manager.Approve(ctx, in.ID, body.Token, body.Decision, Evidence{Via: ViaDashboard})
		if err != nil {
			return nil, a.fail("approve", err)
		}
		return &taskOut{Body: toJSON(t)}, nil
	})
}
