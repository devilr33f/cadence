package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

const adminToken = "good-token"

type adminRig struct {
	*mrig
	app   *fiber.App
	kinds []KindStatus
}

func newAdminRig(t *testing.T) *adminRig {
	r := &adminRig{mrig: newMRig(t)}
	adm := NewAdmin(AdminDeps{
		Manager: r.m, Log: slog.New(slog.NewTextHandler(r.logs, nil)),
		Kinds: func(context.Context) ([]KindStatus, error) { return r.kinds, nil },
	})
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Only(adminToken),
		Admin:       []func(huma.API){func(h huma.API) { RegisterAdmin(h, adm) }},
	})
	return r
}

func (r *adminRig) do(method, path, body string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *adminRig) obj(status int, method, path, body string) map[string]any {
	r.t.Helper()
	got, raw := r.do(method, path, body)
	if got != status {
		r.t.Fatalf("%s %s: %d %s, want %d", method, path, got, raw, status)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		r.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
	}
	return out
}

func (r *adminRig) list(path string) []map[string]any {
	r.t.Helper()
	status, raw := r.do(http.MethodGet, path, "")
	if status != http.StatusOK {
		r.t.Fatalf("GET %s: %d %s", path, status, raw)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *adminRig) expect(status int, code, path, method, route, body string) {
	r.t.Helper()
	got := r.obj(status, method, route, body)
	if got["error"] != code || (path != "" && got["path"] != path) {
		r.t.Fatalf("%s %s %s: %v, want %s %s", method, route, body, got, code, path)
	}
}

func shortIDs(ts []map[string]any) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t["shortId"].(string))
	}
	return out
}

// waiting starts a task and leaves it waiting on q.
func (r *adminRig) waiting(title string, q *Question) Task {
	r.t.Helper()
	r.run()
	tk := r.start(title)
	r.next()
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: q})
	r.next()
	return tk
}

var (
	clarifyQ  = &Question{Kind: QuestionClarify, Text: "which colour?", Options: []string{"red", "blue"}, Token: "c1"}
	approvalQ = &Question{Kind: QuestionApproval, Text: "delete it?", Options: []string{"once", "deny"}, Token: "a1"}
)

func TestAdminAuth(t *testing.T) {
	r := newAdminRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	resp, err := r.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", resp.StatusCode)
	}
}

func TestAdminListFiltersAndShape(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	dev := r.device()
	a, _ := r.s.Create(ctx, "hermes", "first", dev)
	b, _ := r.s.Create(ctx, "fake", "second", uuid.Nil)
	c, _ := r.s.Create(ctx, "hermes", "third", dev)
	r.apply(a.ID, Update{Status: StatusRunning, ExternalRef: sp("x1")}, "progress")
	r.apply(b.ID, Update{Status: StatusDone, Result: sp("RESULT"), Finished: true}, "done")
	r.apply(c.ID, Update{Status: StatusWaitingInput, Question: clarifyQ}, "waiting_input")

	all := r.list("/api/v1/tasks")
	if got := shortIDs(all); !slices.Equal(got, []string{c.ShortID, b.ShortID, a.ShortID}) {
		t.Fatalf("newest first: %v", got)
	}
	for _, row := range all {
		if _, ok := row["result"]; ok {
			t.Fatalf("list carries the result: %v", row)
		}
		if _, ok := row["events"]; ok {
			t.Fatalf("list carries events: %v", row)
		}
	}
	third := all[0]
	q, _ := third["question"].(map[string]any)
	if third["status"] != StatusWaitingInput || third["kind"] != "hermes" || third["title"] != "third" || third["deviceId"] != dev.String() ||
		q["kind"] != "clarify" || q["text"] != "which colour?" || q["token"] != "c1" || len(q["options"].([]any)) != 2 {
		t.Fatalf("shape: %v", third)
	}
	second := all[1]
	if second["deviceId"] != nil || second["question"] != nil || second["finishedAt"] == nil || second["reportedAt"] != nil || second["errorCode"] != "" && second["errorCode"] != nil {
		t.Fatalf("done shape: %v", second)
	}
	for _, k := range []string{"createdAt", "updatedAt", "finishedAt", "reportedAt", "progress", "errorCode", "question", "deviceId"} {
		if _, ok := second[k]; !ok {
			t.Fatalf("field %s missing: %v", k, second)
		}
	}

	if got := shortIDs(r.list("/api/v1/tasks?status=running,waiting_input")); !slices.Equal(got, []string{c.ShortID, a.ShortID}) {
		t.Fatalf("status list: %v", got)
	}
	if got := shortIDs(r.list("/api/v1/tasks?status=done")); !slices.Equal(got, []string{b.ShortID}) {
		t.Fatalf("status: %v", got)
	}
	if got := shortIDs(r.list("/api/v1/tasks?device=" + dev.String())); !slices.Equal(got, []string{c.ShortID, a.ShortID}) {
		t.Fatalf("device: %v", got)
	}
	if got := shortIDs(r.list("/api/v1/tasks?kind=fake")); !slices.Equal(got, []string{b.ShortID}) {
		t.Fatalf("kind: %v", got)
	}
	if got := shortIDs(r.list("/api/v1/tasks?limit=2")); !slices.Equal(got, []string{c.ShortID, b.ShortID}) {
		t.Fatalf("limit 2: %v", got)
	}
	if got := shortIDs(r.list("/api/v1/tasks?limit=200")); len(got) != 3 {
		t.Fatalf("limit 200: %v", got)
	}
	if got := r.list("/api/v1/tasks?status=failed"); len(got) != 0 {
		t.Fatalf("empty list: %v", got)
	}

	for _, bad := range []struct{ query, path string }{
		{"status=bogus", "status"}, {"status=running,bogus", "status"}, {"status=", "status"}, {"device=not-a-uuid", "device"},
		{"limit=0", "limit"}, {"limit=201", "limit"}, {"limit=-1", "limit"}, {"limit=x", "limit"},
		{"kind=" + strings.Repeat("k", 65), "kind"},
	} {
		r.expect(http.StatusUnprocessableEntity, "invalid", bad.path, http.MethodGet, "/api/v1/tasks?"+bad.query, "")
	}
}

func TestAdminGetShowsResultEventsAndNotices(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	dev := r.device()
	tk, _ := r.s.Create(ctx, "hermes", "job", dev)
	r.apply(tk.ID, Update{Status: StatusRunning, ExternalRef: sp("x1")}, "progress")
	r.apply(tk.ID, Update{Status: StatusWaitingInput, Question: approvalQ}, "waiting_input")
	if _, err := r.s.Answered(ctx, tk.ID, mustAnswer(t, DecisionOnce, Evidence{Via: ViaVoice, Device: "dev-1", Words: "yes delete it"})); err != nil {
		t.Fatal(err)
	}
	seq := r.apply(tk.ID, Update{Status: StatusDone, Result: sp("the RESULT text"), Finished: true}, "done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: tk.ID, EventSeq: seq, DeviceID: dev, Text: "it is done"}); err != nil {
		t.Fatal(err)
	}

	got := r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+tk.ShortID, "")
	if got["shortId"] != tk.ShortID || got["result"] != "the RESULT text" || got["status"] != StatusDone {
		t.Fatalf("task: %v", got)
	}
	evs := got["events"].([]any)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	if !slices.Equal(kinds, []string{"progress", "waiting_input", "answered", "done"}) {
		t.Fatalf("events: %v", kinds)
	}
	first := evs[0].(map[string]any)
	if first["seq"] != float64(1) || first["at"] == nil {
		t.Fatalf("event: %v", first)
	}
	for _, k := range []string{"decision", "via", "device", "words"} {
		if _, ok := first[k]; ok {
			t.Fatalf("a progress event carries %s", k)
		}
	}
	ans := evs[2].(map[string]any)
	if ans["decision"] != "once" || ans["via"] != "voice" || ans["device"] != "dev-1" || ans["words"] != "yes delete it" {
		t.Fatalf("answered: %v", ans)
	}
	ns := got["notices"].([]any)
	if len(ns) != 1 {
		t.Fatalf("notices: %v", ns)
	}
	n := ns[0].(map[string]any)
	if n["eventSeq"] != float64(seq) || n["text"] != "it is done" || n["createdAt"] == nil || n["deliveredAt"] != nil {
		t.Fatalf("notice: %v", n)
	}
	// a task with neither shows empty lists
	bare, _ := r.s.Create(ctx, "hermes", "bare", uuid.Nil)
	g := r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+bare.ShortID, "")
	if e, ok := g["events"].([]any); !ok || len(e) != 0 {
		t.Fatalf("bare events: %v", g["events"])
	}
	if e, ok := g["notices"].([]any); !ok || len(e) != 0 {
		t.Fatalf("bare notices: %v", g["notices"])
	}

	r.expect(http.StatusNotFound, "not_found", "", http.MethodGet, "/api/v1/tasks/t-99999", "")
	r.expect(http.StatusNotFound, "not_found", "", http.MethodGet, "/api/v1/tasks/nope", "")
}

func mustAnswer(t *testing.T, decision string, by Evidence) []byte {
	t.Helper()
	b, ok := answerOf(decision, by)
	if !ok {
		t.Fatal("answer refused")
	}
	return b
}

func TestAdminAnsweredWordsGoAfterTheScrub(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	r.run()
	tk := r.waiting("job", approvalQ)
	if _, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionOnce, Evidence{Via: ViaVoice, Device: "dev-1", Words: "do delete it"}); err != nil {
		t.Fatal(err)
	}
	words := func() (map[string]any, bool) {
		got := r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+tk.ShortID, "")
		for _, e := range got["events"].([]any) {
			if m := e.(map[string]any); m["kind"] == "answered" {
				_, has := m["words"]
				return m, has
			}
		}
		t.Fatal("no answered event")
		return nil, false
	}
	if _, has := words(); !has {
		t.Fatal("words missing before the scrub")
	}
	if _, err := r.pool.Exec(ctx, `UPDATE task_events SET at = now() - interval '15 days' WHERE task_id = $1 AND kind = 'answered'`, tk.ID); err != nil {
		t.Fatal(err)
	}
	stop := r.s.RunRetention(ctx, slog.New(slog.DiscardHandler), 20*time.Millisecond)
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		m, has := words()
		if !has {
			if m["decision"] != "once" || m["via"] != "voice" || m["device"] != "dev-1" {
				t.Fatalf("scrubbed answer lost its record: %v", m)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("words kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAdminInspect(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	tk := r.waiting("job", clarifyQ)
	r.k.Snapshots[tk.ExternalRef] = Snapshot{Status: StatusRunning, Progress: "going", Result: "partial", Transcript: "TRANSCRIPT"}

	got := r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+tk.ShortID+"/inspect", "")
	if got["status"] != StatusRunning || got["progress"] != "going" || got["result"] != "partial" {
		t.Fatalf("summary: %v", got)
	}
	if _, ok := got["transcript"]; ok {
		t.Fatalf("summary carries a transcript: %v", got)
	}
	got = r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+tk.ShortID+"/inspect?detail=transcript", "")
	if got["transcript"] != "TRANSCRIPT" {
		t.Fatalf("transcript: %v", got)
	}
	r.expect(http.StatusUnprocessableEntity, "invalid", "detail", http.MethodGet, "/api/v1/tasks/"+tk.ShortID+"/inspect?detail=full", "")
	r.expect(http.StatusNotFound, "not_found", "", http.MethodGet, "/api/v1/tasks/t-99999/inspect", "")

	r.k.InspectErr = errors.New("upstream said: SECRET")
	status, body := r.do(http.MethodGet, "/api/v1/tasks/"+tk.ShortID+"/inspect", "")
	if status != http.StatusBadGateway || body != `{"error":"kind_unavailable"}` {
		t.Fatalf("kind down: %d %s", status, body)
	}
	r.k.InspectErr = nil

	ghost, _ := r.s.Create(ctx, "ghost", "orphan", uuid.Nil)
	r.apply(ghost.ID, Update{Status: StatusRunning, ExternalRef: sp("g1")}, "progress")
	r.expect(http.StatusBadGateway, "kind_unavailable", "", http.MethodGet, "/api/v1/tasks/"+ghost.ShortID+"/inspect", "")
}

func TestAdminCancel(t *testing.T) {
	r := newAdminRig(t)
	r.run()
	tk := r.start("job")
	r.next()
	got := r.obj(http.StatusOK, http.MethodPost, "/api/v1/tasks/"+tk.ShortID+"/cancel", "")
	if got["status"] != StatusCancelled || got["shortId"] != tk.ShortID || got["finishedAt"] == nil {
		t.Fatalf("cancelled: %v", got)
	}
	if len(r.k.Cancelled) != 1 {
		t.Fatalf("kind cancels: %v", r.k.Cancelled)
	}
	r.expect(http.StatusConflict, "not_active", "", http.MethodPost, "/api/v1/tasks/"+tk.ShortID+"/cancel", "")
	r.expect(http.StatusNotFound, "not_found", "", http.MethodPost, "/api/v1/tasks/t-99999/cancel", "")

	r.k.CancelErr = codedErr("hermes_busy")
	again := r.start("job 2")
	r.next()
	r.expect(http.StatusConflict, "busy", "", http.MethodPost, "/api/v1/tasks/"+again.ShortID+"/cancel", "")
}

func TestAdminReply(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	tk := r.waiting("job", clarifyQ)
	route := "/api/v1/tasks/" + tk.ShortID + "/reply"

	for _, bad := range []struct{ body, path string }{
		{`{"message":"blue"}`, "token"},
		{`{"message":"blue","token":""}`, "token"},
		{`{"token":"c1"}`, "message"},
		{`{"message":"","token":"c1"}`, "message"},
		{`{"message":"` + strings.Repeat("x", 2001) + `","token":"c1"}`, "message"},
		{`{"message":"blue","token":"c1","extra":1}`, ""},
		{`{"message":"blue","token":7}`, ""},
		{`not json`, ""},
		{`{"message":"blue","token":"c1"} {}`, ""},
		{`{"message":"blue","token":"c1"}}`, ""},
		{`{"message":"blue","token":"c1"}]`, ""},
		{``, ""},
	} {
		r.expect(http.StatusUnprocessableEntity, "invalid", bad.path, http.MethodPost, route, bad.body)
	}
	if len(r.k.Sent) != 0 {
		t.Fatalf("a refused reply reached the kind: %v", r.k.Sent)
	}
	r.expect(http.StatusConflict, "stale", "", http.MethodPost, route, `{"message":"blue","token":"other"}`)

	r.k.SendErr = codedErr("hermes_stale")
	r.expect(http.StatusConflict, "stale", "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	r.k.SendErr = codedErr("hermes_prompt_unshowable")
	r.expect(http.StatusConflict, "prompt_unshowable", "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	r.k.SendErr = codedErr("hermes_unavailable")
	r.expect(http.StatusBadGateway, "kind_failed", "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	r.k.SendErr = errors.New("upstream said: SECRET")
	r.expect(http.StatusBadGateway, "kind_failed", "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	for code, want := range map[string]string{
		"hermes_capacity": "capacity", "hermes_not_found": "gone", "hermes_no_question": "no_question", "hermes_approval_pending": "approval_pending",
	} {
		r.k.SendErr = codedErr(code)
		r.expect(http.StatusConflict, want, "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	}
	r.k.SendErr = nil

	got := r.obj(http.StatusOK, http.MethodPost, route, `{"message":"blue","token":"c1"}`)
	if got["status"] != StatusRunning || got["question"] != nil {
		t.Fatalf("answered: %v", got)
	}
	if last := len(r.k.Tokens) - 1; last < 0 || r.k.Tokens[last] != "c1" || !strings.HasSuffix(r.k.Sent[last], ":blue") {
		t.Fatalf("kind got %v %v", r.k.Sent, r.k.Tokens)
	}
	// the question is answered: nothing to reply to
	r.expect(http.StatusConflict, "no_question", "", http.MethodPost, route, `{"message":"blue","token":"c1"}`)

	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: approvalQ})
	r.until("waiting_input")
	r.expect(http.StatusConflict, "approval_pending", "", http.MethodPost, route, `{"message":"yes","token":"a1"}`)

	r.expect(http.StatusNotFound, "not_found", "", http.MethodPost, "/api/v1/tasks/t-99999/reply", `{"message":"blue","token":"c1"}`)
	r.expect(http.StatusUnprocessableEntity, "invalid", "token", http.MethodPost, "/api/v1/tasks/t-99999/reply", `{"message":"blue"}`)

	done, _ := r.s.Create(ctx, "fake", "finished", uuid.Nil)
	r.apply(done.ID, Update{Status: StatusDone, Finished: true}, "done")
	r.expect(http.StatusConflict, "not_active", "", http.MethodPost, "/api/v1/tasks/"+done.ShortID+"/reply", `{"message":"blue","token":"c1"}`)
}

func TestAdminApprove(t *testing.T) {
	r := newAdminRig(t)
	tk := r.waiting("job", approvalQ)
	route := "/api/v1/tasks/" + tk.ShortID + "/approve"

	for _, bad := range []struct{ body, path string }{
		{`{"decision":"once"}`, "token"},
		{`{"decision":"once","token":""}`, "token"},
		{`{"decision":"always","token":"a1"}`, "decision"},
		{`{"decision":"","token":"a1"}`, "decision"},
		{`{"token":"a1"}`, "decision"},
		{`{"decision":"once","token":"a1","why":"x"}`, ""},
	} {
		r.expect(http.StatusUnprocessableEntity, "invalid", bad.path, http.MethodPost, route, bad.body)
	}
	r.expect(http.StatusConflict, "stale", "", http.MethodPost, route, `{"decision":"once","token":"old"}`)
	r.k.ApproveErr = codedErr("hermes_stale")
	r.expect(http.StatusConflict, "stale", "", http.MethodPost, route, `{"decision":"once","token":"a1"}`)
	r.k.ApproveErr = codedErr("hermes_no_approval")
	r.expect(http.StatusConflict, "no_approval", "", http.MethodPost, route, `{"decision":"once","token":"a1"}`)
	r.k.ApproveErr = nil
	if len(r.k.Approved) != 2 {
		t.Fatalf("kind approvals %v", r.k.Approved)
	}

	// the dashboard answers whatever hermesApprovals says; the setting only limits voice
	got := r.obj(http.StatusOK, http.MethodPost, route, `{"decision":"once","token":"a1"}`)
	if got["status"] != StatusRunning || got["question"] != nil {
		t.Fatalf("approved: %v", got)
	}
	if last := r.k.Approved[len(r.k.Approved)-1]; !strings.HasSuffix(last, ":a1:once") {
		t.Fatalf("kind got %v", r.k.Approved)
	}
	task := r.obj(http.StatusOK, http.MethodGet, "/api/v1/tasks/"+tk.ShortID, "")
	var ans map[string]any
	for _, e := range task["events"].([]any) {
		if m := e.(map[string]any); m["kind"] == "answered" {
			ans = m
		}
	}
	if ans == nil || ans["via"] != "dashboard" || ans["decision"] != "once" {
		t.Fatalf("evidence: %v", ans)
	}
	if _, has := ans["words"]; has {
		t.Fatalf("a dashboard answer has no words: %v", ans)
	}
	r.expect(http.StatusConflict, "no_approval", "", http.MethodPost, route, `{"decision":"once","token":"a1"}`)

	// deny resumes too
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "again?", Token: "a2"}})
	r.until("waiting_input")
	r.obj(http.StatusOK, http.MethodPost, route, `{"decision":"deny","token":"a2"}`)
	if last := r.k.Approved[len(r.k.Approved)-1]; !strings.HasSuffix(last, ":a2:deny") {
		t.Fatalf("deny: %v", r.k.Approved)
	}

	// a clarify question needs no approval; an unknown id is 404
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: clarifyQ})
	r.until("waiting_input")
	r.expect(http.StatusConflict, "no_approval", "", http.MethodPost, route, `{"decision":"once","token":"c1"}`)
	r.expect(http.StatusNotFound, "not_found", "", http.MethodPost, "/api/v1/tasks/t-99999/approve", `{"decision":"once","token":"a1"}`)
}

func TestAdminKinds(t *testing.T) {
	r := newAdminRig(t)
	r.kinds = []KindStatus{{Name: "hermes", Instance: "agent-a", Configured: "agent-b"}}
	got := r.list("/api/v1/tasks/kinds")
	if len(got) != 1 || got[0]["name"] != "hermes" || got[0]["instance"] != "agent-a" || got[0]["configured"] != "agent-b" {
		t.Fatalf("kinds: %v", got)
	}
	r.kinds = []KindStatus{{Name: "hermes", Configured: "agent-b"}}
	got = r.list("/api/v1/tasks/kinds")
	if got[0]["instance"] != nil || got[0]["configured"] != "agent-b" {
		t.Fatalf("disabled: %v", got)
	}
	r.kinds = nil
	if got = r.list("/api/v1/tasks/kinds"); got == nil || len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
}

func TestAdminCancelReachesTheLiveFeed(t *testing.T) {
	r := newAdminRig(t)
	r.run()
	tk := r.start("job")
	r.next()
	r.obj(http.StatusOK, http.MethodPost, "/api/v1/tasks/"+tk.ShortID+"/cancel", "")
	e := r.next()
	l := live(t, e)
	if e.Type != events.TaskEvent || l.ShortID != tk.ShortID || l.Kind != "cancelled" || l.Status != StatusCancelled || l.TaskKind != "fake" {
		t.Fatalf("event %+v %+v", e, l)
	}
	b, _ := json.Marshal(e.Data)
	var keys map[string]any
	_ = json.Unmarshal(b, &keys)
	if len(keys) != 5 {
		t.Fatalf("live data carries more than ids and codes: %s", b)
	}
}

func TestAdminLogsCarryNoTaskText(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	const (
		titleM    = "TITLEMARKER"
		questionM = "QUESTIONMARKER"
		resultM   = "RESULTMARKER"
		messageM  = "MESSAGEMARKER"
		wordsM    = "WORDSMARKER"
		noticeM   = "NOTICEMARKER"
		upstreamM = "UPSTREAMMARKER"
	)
	markers := []string{titleM, questionM, resultM, messageM, wordsM, noticeM, upstreamM}
	r.run()
	dev := r.device()
	tk, err := r.m.Start(ctx, "fake", Spec{Instructions: titleM, Origin: dev})
	if err != nil {
		t.Fatal(err)
	}
	r.next()
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: questionM, Token: "c1"}})
	r.next()
	id := "/api/v1/tasks/" + tk.ShortID

	r.k.Snapshots[tk.ExternalRef] = Snapshot{Status: StatusWaitingInput, Result: resultM, Transcript: resultM}
	r.k.InspectErr = errors.New(upstreamM)
	r.do(http.MethodGet, id+"/inspect", "")
	r.k.InspectErr = nil
	r.do(http.MethodGet, id+"/inspect?detail=transcript", "")
	r.k.SendErr = codedErr("hermes_busy")
	r.do(http.MethodPost, id+"/reply", `{"message":"`+messageM+`","token":"c1"}`)
	r.k.SendErr = errors.New(upstreamM)
	r.do(http.MethodPost, id+"/reply", `{"message":"`+messageM+`","token":"c1"}`)
	r.k.SendErr = nil
	r.do(http.MethodPost, id+"/reply", `{"message":"`+messageM+`","token":"c1","x":"`+messageM+`"}`)
	r.do(http.MethodPost, id+"/reply", `{"message":"`+messageM+`","token":"c1"}`)

	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: questionM, Token: "a1"}})
	r.until("waiting_input")
	r.k.ApproveErr = errors.New(upstreamM)
	r.do(http.MethodPost, id+"/approve", `{"decision":"once","token":"a1"}`)
	r.k.ApproveErr = nil
	if _, err := r.m.Approve(ctx, tk.ShortID, "a1", DecisionOnce, Evidence{Via: ViaVoice, Device: dev.String(), Words: wordsM}); err != nil {
		t.Fatal(err)
	}
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "done", Result: resultM})
	r.until("done")
	if err := r.s.AddNotice(ctx, Notice{TaskID: tk.ID, EventSeq: 99, DeviceID: dev, Text: noticeM}); err != nil {
		t.Fatal(err)
	}
	r.do(http.MethodPost, id+"/approve", `{"decision":"once","token":"a1"}`)
	r.do(http.MethodPost, id+"/cancel", "")
	_, body := r.do(http.MethodGet, id, "")
	for _, m := range []string{titleM, resultM, wordsM, noticeM} {
		if !strings.Contains(body, m) {
			t.Fatalf("the dashboard misses %s: %s", m, body)
		}
	}
	r.do(http.MethodGet, "/api/v1/tasks?status=done", "")
	r.do(http.MethodGet, "/api/v1/tasks/kinds", "")
	r.do(http.MethodGet, "/api/v1/tasks/t-99999", "")
	r.do(http.MethodPost, "/api/v1/tasks/t-99999/cancel", "")

	logs := r.logs.String()
	if !strings.Contains(logs, "task_changed") {
		t.Fatalf("no logs at all: %q", logs)
	}
	for _, m := range markers {
		if strings.Contains(logs, m) {
			t.Fatalf("log carries %s:\n%s", m, logs)
		}
	}
}

func TestAdminServerErrorsLogTheOperationOnly(t *testing.T) {
	r := newAdminRig(t)
	r.pool.Close()
	status, body := r.do(http.MethodGet, "/api/v1/tasks", "")
	if status != http.StatusInternalServerError || body != `{"error":"internal"}` {
		t.Fatalf("db down: %d %s", status, body)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "task_admin_failed") || !strings.Contains(logs, "op=list") {
		t.Fatalf("logs %q", logs)
	}
	for route, op := range map[string]string{"/api/v1/tasks/t-1000/inspect": "inspect", "/api/v1/tasks/t-1000": "get"} {
		if status, body := r.do(http.MethodGet, route, ""); status != http.StatusInternalServerError || body != `{"error":"internal"}` {
			t.Fatalf("%s: %d %s", route, status, body)
		}
		if logs := r.logs.String(); !strings.Contains(logs, "op="+op) {
			t.Fatalf("no op=%s in %q", op, logs)
		}
	}
	// a store error is no 404 or 422 for a body that fails to decode either
	if status, body := r.do(http.MethodPost, "/api/v1/tasks/t-1000/reply", `{"x":1}`); status != http.StatusInternalServerError || body != `{"error":"internal"}` {
		t.Fatalf("bad body with the store down: %d %s", status, body)
	}
}

// until reads task events up to the first of the given kind.
func (r *adminRig) until(kind string) {
	r.t.Helper()
	for range 20 {
		if live(r.t, r.next()).Kind == kind {
			return
		}
	}
	r.t.Fatalf("no %s event", kind)
}
