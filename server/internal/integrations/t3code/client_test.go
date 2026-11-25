package t3code_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
)

const testToken = "t3-token-that-must-never-surface"

var quiet = slog.New(slog.DiscardHandler)

type harness struct {
	fake   *t3codetest.Server
	client *t3code.Client
}

func start(t *testing.T, cfg t3codetest.Config) harness {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	fake := t3codetest.New(cfg)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL + "/", Token: testToken, HTTP: srv.Client(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t3code.FastTimings(c, 50*time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "connected", c.Connected)
	return harness{fake: fake, client: c}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// follower collects a thread's normalized updates and raw items.
type follower struct {
	mu      sync.Mutex
	tracker *t3code.Tracker
	updates []t3code.Update
	items   []t3code.ThreadItem
}

func follow(t *testing.T, c *t3code.Client, threadID string) (*follower, *t3code.Subscription) {
	f := &follower{tracker: t3code.NewTracker(threadID)}
	sub := c.SubscribeThread(threadID, func(it t3code.ThreadItem) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.items = append(f.items, it)
		f.updates = append(f.updates, f.tracker.Apply(it)...)
	})
	t.Cleanup(sub.Close)
	return f, sub
}

func (f *follower) find(kind string) (t3code.Update, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.updates {
		if u.Kind == kind {
			return u, true
		}
	}
	return t3code.Update{}, false
}

func (f *follower) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, u := range f.updates {
		if u.Kind == kind {
			n++
		}
	}
	return n
}

func (f *follower) wait(t *testing.T, kind string) t3code.Update {
	t.Helper()
	var u t3code.Update
	waitFor(t, kind, func() bool {
		var ok bool
		u, ok = f.find(kind)
		return ok
	})
	return u
}

func (f *follower) snapshotted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.items {
		if it.Kind == t3code.ThreadSnapshotKind {
			return true
		}
	}
	return false
}

func send(t *testing.T, c *t3code.Client, threadID, text string) {
	t.Helper()
	m := t3code.DispatchMessage{ThreadID: threadID, Text: text, DispatchMode: t3code.DispatchMode{Type: t3code.DispatchStartImmediately}}
	if _, err := c.DispatchCommand(context.Background(), m, ""); err != nil {
		t.Fatal(err)
	}
}

func lastCommand(t *testing.T, fake *t3codetest.Server) (t3codetest.Command, map[string]any) {
	t.Helper()
	cmds := fake.Commands()
	if len(cmds) == 0 {
		t.Fatal("no command")
	}
	cmd := cmds[len(cmds)-1]
	var m map[string]any
	if err := json.Unmarshal(cmd.Raw, &m); err != nil {
		t.Fatal(err)
	}
	return cmd, m
}

func TestShellStream(t *testing.T) {
	h := start(t, t3codetest.Config{})
	var mu sync.Mutex
	shell := t3code.NewShellTracker()
	var told []t3code.ShellUpdate
	sub := h.client.SubscribeShell(func(it t3code.ShellItem) {
		mu.Lock()
		defer mu.Unlock()
		told = append(told, shell.Apply(it)...)
	})
	defer sub.Close()
	waitFor(t, "snapshot", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(shell.Threads) == 5 && len(shell.Projects) == 1
	})
	mu.Lock()
	p := shell.Projects[t3codetest.ProjectID]
	mu.Unlock()
	if p.Title != "Cadence" || p.DefaultModelSelection == nil || p.DefaultModelSelection.Model == "" {
		t.Fatalf("project %+v", p)
	}
	send(t, h.client, t3codetest.ThreadAsk, "ask about colors")
	waitFor(t, "shell question", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, u := range told {
			if u.Kind == t3code.KindQuestion && u.NeedsDetail && u.RequestID != "" && u.ProjectID == t3codetest.ProjectID {
				return true
			}
		}
		return false
	})
	mu.Lock()
	th := shell.Threads[t3codetest.ThreadAsk]
	mu.Unlock()
	if th.Status != "waiting" || th.PendingRuntimeRequest == nil || th.LatestRunID == nil || th.Title != "ask" {
		t.Fatalf("shell %+v", th)
	}
	proj, err := h.client.GetThreadProjection(context.Background(), t3codetest.ThreadAsk)
	if err != nil {
		t.Fatal(err)
	}
	pending := t3code.PendingFromProjection(*proj)
	if len(pending) != 1 || pending[0].Kind != t3code.KindQuestion || pending[0].RequestID != th.PendingRuntimeRequest.ID {
		t.Fatalf("pending %+v", pending)
	}
	if h.fake.Acks() == 0 {
		t.Error("no chunk was acked")
	}
}

func TestFinishedRun(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadFinish)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadFinish, "finish the docs")
	u := f.wait(t, t3code.KindStopped)
	if u.Status != t3code.RunCompleted || u.Text != "Finished: finish the docs" || u.Title != "finish" || u.RunID == "" {
		t.Fatalf("stopped %+v", u)
	}
	cmd, m := lastCommand(t, h.fake)
	if cmd.Type != "message.dispatch" || !strings.HasPrefix(cmd.CommandID, "cadence-cmd-") ||
		m["createdBy"] != "user" || m["creationSource"] != "mcp" || !strings.HasPrefix(m["messageId"].(string), "cadence-msg-") {
		t.Fatalf("command %+v %v", cmd, m)
	}
	if f.count(t3code.KindError) != 0 {
		t.Error("error told for a completed run")
	}
}

// The fake sends an unknown event type and an unknown turn item type whose
// fields clash with known ones, plus unknown fields everywhere: they arrive
// as items and change nothing.
func TestUnknownEventsIgnored(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadFinish)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadFinish, "finish")
	f.wait(t, t3code.KindStopped)
	f.mu.Lock()
	defer f.mu.Unlock()
	var unknownEvent, unknownItem bool
	for _, it := range f.items {
		if it.Event == nil {
			continue
		}
		switch it.Event.Type {
		case "future.event-type":
			unknownEvent = true
		case t3code.EventTurnItemUpdated:
			var item t3code.TurnItem
			if err := json.Unmarshal(it.Event.Payload, &item); err != nil {
				t.Fatalf("turn item: %v", err)
			}
			if item.Type == "future_widget" {
				unknownItem = true
			}
		}
	}
	if !unknownEvent || !unknownItem {
		t.Fatalf("unknown event %v, unknown item %v", unknownEvent, unknownItem)
	}
	if len(f.updates) != 1 {
		t.Fatalf("updates %+v", f.updates)
	}
}

func TestQuestionAnswered(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadAsk)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadAsk, "ask me")
	q := f.wait(t, t3code.KindQuestion)
	if len(q.Questions) != 1 || q.Questions[0].ID != t3codetest.QuestionID || len(q.Questions[0].Options) != 2 ||
		!q.Questions[0].CustomAllowed() || q.RequestID == "" {
		t.Fatalf("question %+v", q)
	}
	if id, _, ok := t3code.SplitToken(q.Token); !ok || id != q.RequestID {
		t.Fatalf("token %q", q.Token)
	}
	f.mu.Lock()
	cur, ok := f.tracker.Pending(q.Token)
	f.mu.Unlock()
	if !ok || cur.Token != q.Token {
		t.Fatal("token not pending")
	}
	resp := t3code.RespondRuntimeRequest{ThreadID: t3codetest.ThreadAsk, RequestID: q.RequestID, Answers: map[string]any{t3codetest.QuestionID: "Blue"}}
	if _, err := h.client.DispatchCommand(context.Background(), resp, "cadence-cmd-answer-1"); err != nil {
		t.Fatal(err)
	}
	r := f.wait(t, t3code.KindResolved)
	if r.Token != q.Token {
		t.Fatalf("resolved %+v", r)
	}
	if s := f.wait(t, t3code.KindStopped); s.Status != t3code.RunCompleted || s.Text != "Answered" {
		t.Fatalf("stopped %+v", s)
	}
	cmd, m := lastCommand(t, h.fake)
	answers, _ := m["answers"].(map[string]any)
	if cmd.Type != "runtime-request.respond" || cmd.CommandID != "cadence-cmd-answer-1" || answers[t3codetest.QuestionID] != "Blue" || m["decision"] != nil {
		t.Fatalf("command %+v %v", cmd, m)
	}
	// a second answer to the same request is refused by T3, as a code
	_, err := h.client.DispatchCommand(context.Background(), resp, "")
	var e *t3code.Error
	if !errors.As(err, &e) || e.Code != t3code.CodeRefused || e.Tag != "OrchestrationV2DispatchCommandError" {
		t.Fatalf("second answer: %v", err)
	}
	if f.count(t3code.KindQuestion) != 1 {
		t.Error("question told twice")
	}
}

func TestApprovalDecided(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadApprove)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadApprove, "approve the test run")
	a := f.wait(t, t3code.KindApproval)
	if a.Approval == nil || a.Approval.RequestKind != "command" || a.Approval.Prompt != "Run make test?" || len(a.Approval.Options) != 3 {
		t.Fatalf("approval %+v", a)
	}
	resp := t3code.RespondRuntimeRequest{ThreadID: t3codetest.ThreadApprove, RequestID: a.RequestID, Decision: t3code.DecisionAcceptForSession}
	if _, err := h.client.DispatchCommand(context.Background(), resp, ""); err != nil {
		t.Fatal(err)
	}
	f.wait(t, t3code.KindResolved)
	f.wait(t, t3code.KindStopped)
	_, m := lastCommand(t, h.fake)
	if m["decision"] != "acceptForSession" || m["answers"] != nil {
		t.Fatalf("command %v", m)
	}
}

func TestErrorRun(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadFail)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadFail, "fail please")
	s := f.wait(t, t3code.KindStopped)
	if s.Status != t3code.RunFailed {
		t.Fatalf("stopped %+v", s)
	}
	waitFor(t, "two errors", func() bool { return f.count(t3code.KindError) == 2 })
	e, _ := f.find(t3code.KindError)
	if e.ErrorCode != "overloaded" || e.ErrorClass != "provider_error" || e.Text != "the model is unavailable" {
		t.Fatalf("error %+v", e)
	}
}

func TestInterruptAndStop(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, _ := follow(t, h.client, t3codetest.ThreadHold)
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadHold, "hold on")
	var runID string
	waitFor(t, "run", func() bool {
		p, err := h.client.GetThreadProjection(context.Background(), t3codetest.ThreadHold)
		if err == nil && len(p.Runs) == 1 {
			runID = p.Runs[0].ID
		}
		return runID != ""
	})
	// steering the running run reaches it without a new run
	steer := t3code.DispatchMessage{ThreadID: t3codetest.ThreadHold, Text: "also this", DispatchMode: t3code.DispatchMode{Type: t3code.DispatchSteerActive, TargetRunID: runID}}
	if _, err := h.client.DispatchCommand(context.Background(), steer, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.DispatchCommand(context.Background(), t3code.InterruptRun{ThreadID: t3codetest.ThreadHold, RunID: runID}, ""); err != nil {
		t.Fatal(err)
	}
	if s := f.wait(t, t3code.KindStopped); s.Status != t3code.RunInterrupted || s.RunID != runID {
		t.Fatalf("stopped %+v", s)
	}
	send(t, h.client, t3codetest.ThreadHold, "hold again")
	waitFor(t, "second run", func() bool {
		p, err := h.client.GetThreadProjection(context.Background(), t3codetest.ThreadHold)
		return err == nil && len(p.Runs) == 2 && p.Runs[1].Status == "running"
	})
	if _, err := h.client.DispatchCommand(context.Background(), t3code.StopThread{ThreadID: t3codetest.ThreadHold}, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { return f.count(t3code.KindStopped) == 2 })
	types := []string{}
	for _, c := range h.fake.Commands() {
		types = append(types, c.Type)
	}
	if strings.Join(types, ",") != "message.dispatch,message.dispatch,run.interrupt,message.dispatch,thread.stop" {
		t.Fatalf("commands %v", types)
	}
}

func TestLaunchThread(t *testing.T) {
	h := start(t, t3codetest.Config{})
	res, err := h.client.LaunchThread(context.Background(), t3code.LaunchInput{
		ProjectID: t3codetest.ProjectID, Title: "From the kitchen",
		ModelSelection: t3code.ModelSelection{InstanceID: "claude", Model: "claude-opus-5-5"}, Text: "finish a launch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ThreadID == "" || res.Projection.Thread.Title != "From the kitchen" || res.Sequence == 0 {
		t.Fatalf("launch %+v", res)
	}
	cmd, m := lastCommand(t, h.fake)
	ws, _ := m["workspaceStrategy"].(map[string]any)
	initial, _ := m["initialMessage"].(map[string]any)
	if cmd.Type != "launchThread" || m["runtimeMode"] != "approval-required" || m["interactionMode"] != "default" ||
		m["creationSource"] != "mcp" || ws["type"] != "root" || initial["text"] != "finish a launch" || m["Text"] != nil {
		t.Fatalf("launch command %v", m)
	}
	_, err = h.client.LaunchThread(context.Background(), t3code.LaunchInput{ProjectID: "nope", Title: "x", ModelSelection: t3code.ModelSelection{Model: "m"}})
	if t3code.ErrorCode(err) != t3code.CodeRefused {
		t.Fatalf("bad launch: %v", err)
	}
}

func TestUnknownThread(t *testing.T) {
	h := start(t, t3codetest.Config{})
	_, err := h.client.GetThreadProjection(context.Background(), "thread-nope")
	var e *t3code.Error
	if !errors.As(err, &e) || e.Code != t3code.CodeRefused || e.Tag != "OrchestrationV2GetThreadProjectionError" {
		t.Fatalf("projection: %v", err)
	}
	sub := h.client.SubscribeThread("thread-nope", func(t3code.ThreadItem) {})
	select {
	case <-sub.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("subscription of an unknown thread kept going")
	}
	if sub.Err() != t3code.CodeRefused {
		t.Fatalf("err %q", sub.Err())
	}
}

// After a dropped connection every stream is opened again from its cursor,
// and what happened while it was down arrives once.
func TestReconnectResumes(t *testing.T) {
	h := start(t, t3codetest.Config{})
	f, sub := follow(t, h.client, t3codetest.ThreadFinish)
	var mu sync.Mutex
	shell := t3code.NewShellTracker()
	var told []t3code.ShellUpdate
	shellSub := h.client.SubscribeShell(func(it t3code.ShellItem) {
		mu.Lock()
		defer mu.Unlock()
		told = append(told, shell.Apply(it)...)
	})
	defer shellSub.Close()
	waitFor(t, "snapshot", f.snapshotted)
	send(t, h.client, t3codetest.ThreadFinish, "finish first")
	f.wait(t, t3code.KindStopped)
	waitFor(t, "shell", func() bool { return shellSub.Cursor() > 0 })
	before := sub.Cursor()
	h.fake.Drop()
	h.fake.Start(t3codetest.ThreadFinish, "finish while away")
	waitFor(t, "reconnect", func() bool { return len(h.fake.Subscribes()) >= 4 })
	waitFor(t, "second stop", func() bool { return f.count(t3code.KindStopped) == 2 })
	f.mu.Lock()
	last := f.updates[len(f.updates)-1]
	f.mu.Unlock()
	if last.Kind != t3code.KindStopped || last.Text != "Finished: finish while away" {
		t.Fatalf("stopped %+v", last)
	}
	waitFor(t, "shell stopped twice", func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, u := range told {
			if u.Kind == t3code.KindStopped && u.ThreadID == t3codetest.ThreadFinish {
				n++
			}
		}
		return n == 2
	})
	subs := h.fake.Subscribes()
	var resumedThread, resumedShell bool
	for _, s := range subs[2:] {
		switch {
		case s.Method == t3code.MethodSubscribeThread && s.AfterSequence != nil && *s.AfterSequence == before:
			resumedThread = true
		case s.Method == t3code.MethodSubscribeShell && s.AfterSequence != nil && *s.AfterSequence > 0:
			resumedShell = true
		}
	}
	if !resumedThread || !resumedShell {
		for _, s := range subs {
			t.Logf("%s %s %v", s.Method, s.ThreadID, s.AfterSequence)
		}
		t.Fatal("streams not resumed from their cursors")
	}
	time.Sleep(100 * time.Millisecond)
	if f.count(t3code.KindStopped) != 2 {
		t.Fatalf("stopped told %d times", f.count(t3code.KindStopped))
	}
	f.mu.Lock()
	snapshots := 0
	for _, it := range f.items {
		if it.Kind == t3code.ThreadSnapshotKind {
			snapshots++
		}
	}
	f.mu.Unlock()
	if snapshots != 1 {
		t.Fatalf("%d snapshots: a resume replays events", snapshots)
	}
}

func TestTicketFallback(t *testing.T) {
	h := start(t, t3codetest.Config{TicketOnly: true})
	if _, err := h.client.GetThreadProjection(context.Background(), t3codetest.ThreadFinish); err != nil {
		t.Fatal(err)
	}
}

func TestPing(t *testing.T) {
	fake := t3codetest.New(t3codetest.Config{Token: testToken})
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, _ := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: testToken, HTTP: srv.Client(), Log: quiet})
	t3code.FastTimings(c, 50*time.Millisecond, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "pings", func() bool { return fake.Pings() >= 3 })
	if !c.Connected() {
		t.Fatal("pongs did not keep the connection")
	}
}

func TestConnectFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		token   string
		want    string
	}{
		"wrong token": {token: "wrong", want: t3code.CodeAuthFailed},
		"old server": {handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUpgradeRequired)
		}, want: t3code.CodeProtocol},
		"not t3": {handler: func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}, want: t3code.CodeBadResponse},
	} {
		t.Run(name, func(t *testing.T) {
			var h http.Handler = t3codetest.New(t3codetest.Config{Token: testToken})
			if tc.handler != nil {
				h = tc.handler
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			token := tc.token
			if token == "" {
				token = testToken
			}
			c, _ := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: token, HTTP: srv.Client(), Log: quiet})
			t3code.FastTimings(c, 50*time.Millisecond, time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go c.Run(ctx)
			waitFor(t, "failure", func() bool { return c.Status().LastError != "" })
			if got := c.Status().LastError; got != tc.want || c.Connected() {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
			err := c.Call(context.Background(), t3code.MethodGetThreadProjection, map[string]string{"threadId": "x"}, nil)
			if t3code.ErrorCode(err) != t3code.CodeUnavailable || strings.Contains(err.Error(), token) {
				t.Fatalf("call while down: %v", err)
			}
		})
	}
}

func TestNewClientRefusesBadConfig(t *testing.T) {
	for _, u := range []string{"ftp://h", "https://u:pw@h", "https://h?x=1", "https://h#f", "https:// h", "", "https://"} {
		if _, err := t3code.NewClient(t3code.ClientConfig{BaseURL: u, Token: "t"}); err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: %v", u, err)
		}
	}
	if _, err := t3code.NewClient(t3code.ClientConfig{BaseURL: "https://h/prefix", Token: ""}); err == nil {
		t.Error("no token taken")
	}
}
