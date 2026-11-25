package tools_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

const (
	wake     = cadencev2.Trigger_TRIGGER_WAKE
	button   = cadencev2.Trigger_TRIGGER_BUTTON
	followUp = cadencev2.Trigger_TRIGGER_FOLLOW_UP
	event    = cadencev2.Trigger_TRIGGER_EVENT
)

// kindCode is a kind's refusal: a plain code, and text that must never pass.
type kindCode string

func (e kindCode) Error() string     { return "upstream said: private words" }
func (e kindCode) ErrorCode() string { return string(e) }

type taskRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	m      *tasks.Manager
	k      *tasks.FakeKind
	dev    string
	values settings.Values
	tools  map[string]agent.Tool
	reg    *agent.Registry
	// text is the user's words of the turn each call runs in, taskID the task
	// an event turn is about, turn the turn's id
	text, taskID, turn string
}

func newTaskRig(t *testing.T) *taskRig {
	pool := storetest.Pool(t)
	d, err := db.New(pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	m := tasks.NewManager(tasks.ManagerDeps{Store: tasks.NewStore(pool), Bus: events.NewBus(0)})
	k := tasks.NewFakeKind("hermes")
	m.Register(k)
	ctx, cancel := context.WithCancel(context.Background())
	stop := m.Run(ctx)
	t.Cleanup(func() { cancel(); stop() })
	r := &taskRig{t: t, pool: pool, m: m, k: k, dev: d.ID.String(), values: settings.Defaults(), tools: map[string]agent.Tool{}, turn: "turn-1"}
	list := tools.Tasks(tools.TaskDeps{
		Manager: m,
		Settings: func(_ context.Context, id string) (settings.Values, error) {
			if id != r.dev {
				t.Errorf("settings of %s", id)
			}
			return r.values, nil
		},
	})
	for _, tool := range list {
		r.tools[tool.Name] = tool
	}
	r.reg = agent.NewRegistry(list...)
	return r
}

// call validates args against the registered tool and runs it in a turn of trig.
func (r *taskRig) call(name string, trig cadencev2.Trigger, args string) (json.RawMessage, *agent.ToolError) {
	r.t.Helper()
	var tool agent.Tool
	for _, t := range r.reg.Offer([]string{name}, false, wake) {
		tool = t
	}
	if tool.Name == "" {
		r.t.Fatalf("no tool %s", name)
	}
	c := agent.Call{
		DeviceID: r.dev, SessionID: "s1", TurnID: r.turn, Trigger: trig, Text: r.text, TaskID: r.taskID, Args: json.RawMessage(args),
	}
	if e := agent.Validate(tool, c.Args); e != nil {
		return nil, e
	}
	return agent.Run(context.Background(), tool, c)
}

func (r *taskRig) ok(name string, trig cadencev2.Trigger, args string) map[string]any {
	r.t.Helper()
	out, e := r.call(name, trig, args)
	if e != nil {
		r.t.Fatalf("%s %s: %+v", name, args, e)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		r.t.Fatalf("%s: %s", name, out)
	}
	return m
}

func (r *taskRig) refused(name string, trig cadencev2.Trigger, args, code string) *agent.ToolError {
	r.t.Helper()
	out, e := r.call(name, trig, args)
	if e == nil || e.Code != code {
		r.t.Fatalf("%s %s: got %s %+v, want %s", name, args, out, e, code)
	}
	if strings.Contains(e.Message+e.Hint, "private") {
		r.t.Fatalf("kind text passed: %+v", e)
	}
	return e
}

func (r *taskRig) start(instr string) tasks.Task {
	r.t.Helper()
	tk, err := r.m.Start(context.Background(), "hermes", tasks.Spec{Instructions: instr})
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

// wait polls until the task has status, and returns it.
func (r *taskRig) wait(tk tasks.Task, status string) tasks.Task {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := r.m.Get(context.Background(), tk.ShortID)
		if err == nil && got.Status == status {
			return got
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%s never %s: %+v", tk.ShortID, status, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ask makes tk wait on q.
func (r *taskRig) ask(tk tasks.Task, q tasks.Question) tasks.Task {
	r.t.Helper()
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &q})
	waitUntil(r.t, "the question", func() bool {
		got, err := r.m.Get(context.Background(), tk.ShortID)
		return err == nil && got.Question != nil && got.Question.Token == q.Token && got.Question.Text == q.Text
	})
	got, _ := r.m.Get(context.Background(), tk.ShortID)
	return got
}

// tell has the rig's device told tk's question, as an earlier turn that read it.
func (r *taskRig) tell(tk tasks.Task) tasks.Task {
	r.t.Helper()
	if err := r.m.MarkTold(context.Background(), tk.ShortID, tk.Question.Token, uuid.MustParse(r.dev), "earlier"); err != nil {
		r.t.Fatal(err)
	}
	return tk
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTaskStart(t *testing.T) {
	r := newTaskRig(t)
	tool := r.tools["task_start"]
	if tool.Effects != agent.Write || tool.Output != agent.Trusted || tool.Timeout != 10*time.Second || tool.EventTurns || tool.TaintExempt {
		t.Fatalf("tool %+v", tool)
	}
	if !strings.Contains(tool.Input, `"enum": ["hermes"]`) {
		t.Fatalf("schema %s", tool.Input)
	}
	got := r.ok("task_start", followUp, `{"kind":"hermes","instructions":"find a flight\nto Almaty, under 100 euros"}`)
	if !reflect.DeepEqual(got, map[string]any{"id": "t-1000", "status": "running"}) {
		t.Fatalf("ack %v", got)
	}
	if len(r.k.Specs) != 1 || r.k.Specs[0].Instructions != "find a flight\nto Almaty, under 100 euros" || r.k.Specs[0].Origin.String() != r.dev {
		t.Fatalf("specs %+v", r.k.Specs)
	}
	if tk, _ := r.m.Get(context.Background(), "t-1000"); tk.Title != "find a flight" {
		t.Fatalf("task %+v", tk)
	}
	for _, args := range []string{
		`{"instructions":"x"}`, `{"kind":"hermes"}`, `{"kind":"nope","instructions":"x"}`, `{"kind":"hermes","instructions":""}`,
		`{"kind":"hermes","instructions":"` + strings.Repeat("я", 2001) + `"}`, `{"kind":"hermes","instructions":"x","title":"t"}`,
		`{"kind":"hermes","instructions":" \n "}`,
	} {
		r.refused("task_start", wake, args, "invalid_arguments")
	}
	r.ok("task_start", wake, `{"kind":"hermes","instructions":"`+strings.Repeat("я", 2000)+`"}`)
	// an event turn has no user to start work for
	r.refused("task_start", event, `{"kind":"hermes","instructions":"x"}`, "not_in_event_turn")
	if len(r.k.Specs) != 2 {
		t.Fatalf("started %d", len(r.k.Specs))
	}
}

// a start the kind refuses says why; the tool reported it, so the failed task
// is not reported again
func TestTaskStartRefused(t *testing.T) {
	r := newTaskRig(t)
	r.k.StartErr = kindCode("hermes_capacity")
	e := r.refused("task_start", wake, `{"kind":"hermes","instructions":"x"}`, "too_many_tasks")
	if e.Retryable {
		t.Fatalf("a refused start retries: %+v", e)
	}
	r.k.StartErr = kindCode("hermes_unavailable")
	r.refused("task_start", wake, `{"kind":"hermes","instructions":"x"}`, "task_start_failed")
	if left, err := r.m.Unreported(context.Background(), uuid.Nil); err != nil || len(left) != 0 {
		t.Fatalf("unreported %+v %v", left, err)
	}
}

func TestTaskList(t *testing.T) {
	r := newTaskRig(t)
	tool := r.tools["task_list"]
	// the questions are Hermes's words
	if tool.Effects != agent.Read || !tool.Taints() {
		t.Fatalf("tool %+v", tool)
	}
	a, b, c := r.start("first"), r.start("second"), r.start("third")
	r.ask(b, tasks.Question{Kind: tasks.QuestionClarify, Text: "which one?", Options: []string{"red", "blue"}, Token: "c1"})
	r.k.Emit(tasks.Event{Ref: c.ExternalRef, Kind: "done", Result: "RESULT-TEXT"})
	r.wait(c, tasks.StatusDone)
	out, e := r.call("task_list", followUp, `{}`)
	if e != nil {
		t.Fatal(e)
	}
	want := `{"tasks":[` +
		`{"id":"` + b.ShortID + `","kind":"hermes","title":"second","status":"waiting_input","question":{"kind":"clarify","text":"which one?","options":["red","blue"],"token":"c1"}},` +
		`{"id":"` + a.ShortID + `","kind":"hermes","title":"first","status":"running"},` +
		`{"id":"` + c.ShortID + `","kind":"hermes","title":"third","status":"done"}]}`
	if string(out) != want {
		t.Fatalf("list\n%s\nwant\n%s", out, want)
	}
	r.refused("task_list", wake, `{"all":true}`, "invalid_arguments")
	if out, _ := r.call("task_list", event, `{}`); !strings.Contains(string(out), "t-") {
		t.Fatalf("event turn: %s", out)
	}
}

func TestTaskInspect(t *testing.T) {
	r := newTaskRig(t)
	tool := r.tools["task_inspect"]
	if tool.Effects != agent.Read || !tool.Taints() {
		t.Fatalf("tool %+v", tool)
	}
	done := r.start("done one")
	r.k.Emit(tasks.Event{Ref: done.ExternalRef, Kind: "done", Result: "start " + strings.Repeat("é", 3000)})
	r.wait(done, tasks.StatusDone)
	got := r.ok("task_inspect", wake, `{"id":"`+done.ShortID+`","detail":"summary"}`)
	if res, _ := got["result"].(string); got["status"] != "done" || got["truncated"] != true ||
		len([]rune(res)) != 2000 || !strings.HasPrefix(res, "start ") {
		t.Fatalf("summary %v", got)
	}
	run := r.start("running one")
	r.k.Snapshots[run.ExternalRef] = tasks.Snapshot{Status: tasks.StatusRunning, Progress: "p", Transcript: strings.Repeat("a", 5000) + strings.Repeat("ж", 5000) + " end"}
	got = r.ok("task_inspect", wake, `{"id":"`+run.ShortID+`","detail":"transcript"}`)
	if tr, _ := got["transcript"].(string); len([]rune(tr)) != 8000 || !strings.HasSuffix(tr, "ж end") || got["truncated"] != true || got["progress"] != "p" {
		t.Fatalf("transcript %v", got)
	}
	r.k.Snapshots[run.ExternalRef] = tasks.Snapshot{Status: tasks.StatusWaitingInput, Question: &tasks.Question{Kind: tasks.QuestionApproval, Text: "rm?", Token: "a1"}}
	got = r.ok("task_inspect", wake, `{"id":"`+run.ShortID+`","detail":"summary"}`)
	if q, _ := got["question"].(map[string]any); q["token"] != "a1" || q["kind"] != "approval" || got["truncated"] != nil {
		t.Fatalf("question %v", got)
	}
	r.refused("task_inspect", wake, `{"id":"`+run.ShortID+`","detail":"all"}`, "invalid_arguments")
	r.refused("task_inspect", wake, `{"id":"`+run.ShortID+`"}`, "invalid_arguments")
	r.k.InspectErr = kindCode("hermes_unavailable")
	r.refused("task_inspect", wake, `{"id":"`+run.ShortID+`","detail":"summary"}`, "inspect_failed")
}

// a voice id comes in many spellings; one that names no task gets the active ones
func TestTaskIDs(t *testing.T) {
	r := newTaskRig(t)
	a, b := r.start("a"), r.start("b")
	r.k.Emit(tasks.Event{Ref: b.ExternalRef, Kind: "done"})
	r.wait(b, tasks.StatusDone)
	num := strings.TrimPrefix(a.ShortID, "t-")
	for _, id := range []string{a.ShortID, num, "T-" + num, " t" + num + " "} {
		if got := r.ok("task_inspect", wake, `{"id":"`+id+`","detail":"summary"}`); got["id"] != a.ShortID {
			t.Fatalf("%q: %v", id, got)
		}
	}
	for _, id := range []string{"t-99999", "flights", "t-"} {
		e := r.refused("task_inspect", wake, `{"id":"`+id+`","detail":"summary"}`, "task_not_found")
		if !reflect.DeepEqual(e.Candidates, []string{a.ShortID}) {
			t.Fatalf("%q candidates %v", id, e.Candidates)
		}
	}
}

func TestTaskCancel(t *testing.T) {
	r := newTaskRig(t)
	tool := r.tools["task_cancel"]
	if tool.Effects != agent.Write || tool.Output != agent.Trusted || tool.EventTurns || !tool.TaintExempt {
		t.Fatalf("tool %+v", tool)
	}
	tk := r.start("x")
	r.refused("task_cancel", event, `{"id":"`+tk.ShortID+`"}`, "not_in_event_turn")
	if got := r.ok("task_cancel", followUp, `{"id":"`+tk.ShortID+`"}`); !reflect.DeepEqual(got, map[string]any{"id": tk.ShortID, "status": "cancelled"}) {
		t.Fatalf("cancel %v", got)
	}
	if len(r.k.Cancelled) != 1 || r.k.Cancelled[0] != tk.ExternalRef {
		t.Fatalf("cancelled %v", r.k.Cancelled)
	}
	r.refused("task_cancel", wake, `{"id":"`+tk.ShortID+`"}`, "task_not_active")
	other := r.start("y")
	r.k.CancelErr = kindCode("hermes_unavailable")
	r.refused("task_cancel", wake, `{"id":"`+other.ShortID+`"}`, "cancel_failed")
}

func TestTaskReplyClarify(t *testing.T) {
	r := newTaskRig(t)
	tool := r.tools["task_reply"]
	// a kind's answer may take two calls of 10 s each (poll, post)
	if tool.Effects != agent.Write || tool.Output != agent.Trusted || !tool.EventTurns || !tool.TaintExempt || tool.Timeout != 30*time.Second {
		t.Fatalf("tool %+v", tool)
	}
	tk := r.start("x")
	r.ask(tk, tasks.Question{Kind: tasks.QuestionClarify, Text: "which colour?", Token: "c1"})
	id := `"id":"` + tk.ShortID + `"`
	r.refused("task_reply", wake, `{`+id+`,"message":"blue"}`, "invalid_arguments")
	r.refused("task_reply", wake, `{`+id+`,"token":"c1"}`, "invalid_arguments")
	r.refused("task_reply", wake, `{`+id+`,"token":"c1","decision":"once","userWords":"blue"}`, "invalid_arguments")
	r.refused("task_reply", wake, `{`+id+`,"token":"c1","message":""}`, "invalid_arguments")
	r.refused("task_reply", wake, `{`+id+`,"token":"c0","message":"blue"}`, "question_changed")
	for code, want := range map[string]string{
		"hermes_busy": "task_busy", "hermes_prompt_unshowable": "answer_in_hermes", "hermes_secret_prompt": "answer_in_hermes",
		"hermes_stale": "question_changed", "hermes_no_question": "no_question", "hermes_gone": "reply_failed",
	} {
		r.k.SendErr = kindCode(code)
		e := r.refused("task_reply", wake, `{`+id+`,"token":"c1","message":"blue"}`, want)
		if e.Retryable != (want == "task_busy") {
			t.Fatalf("%s retryable %v", code, e.Retryable)
		}
	}
	r.k.SendErr = nil
	// an event turn answers only its own task: none named fails closed
	other := r.start("y")
	r.ask(other, tasks.Question{Kind: tasks.QuestionClarify, Text: "which size?", Token: "c9"})
	r.refused("task_reply", event, `{`+id+`,"token":"c1","message":"blue"}`, "not_in_event_turn")
	r.taskID = other.ShortID
	r.refused("task_reply", event, `{`+id+`,"token":"c1","message":"blue"}`, "not_in_event_turn")
	// and may answer its clarify question from what it knows
	r.taskID = tk.ShortID
	if got := r.ok("task_reply", event, `{"id":"`+strings.TrimPrefix(tk.ShortID, "t-")+`","token":"c1","message":"blue"}`); got["status"] != "running" {
		t.Fatalf("reply %v", got)
	}
	if n := len(r.k.Sent); r.k.Sent[n-1] != tk.ExternalRef+":blue" || r.k.Tokens[n-1] != "c1" {
		t.Fatalf("sent %v %v", r.k.Sent, r.k.Tokens)
	}
	// a running task's own event turn cannot tell it more
	sent := len(r.k.Sent)
	r.refused("task_reply", event, `{`+id+`,"message":"also check the cellar"}`, "not_in_event_turn")
	if len(r.k.Sent) != sent {
		t.Fatalf("sent %v", r.k.Sent)
	}
	r.refused("task_reply", wake, `{`+id+`,"decision":"once","userWords":"yes, do x"}`, "no_approval")
}

func TestTaskReplyApproval(t *testing.T) {
	r := newTaskRig(t)
	tk := r.start("x")
	r.tell(r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods?", Options: []string{"once", "deny"}, Token: "a1"}))
	reply := func(decision, words string) string {
		return `{"id":"` + tk.ShortID + `","token":"a1","decision":"` + decision + `","userWords":"` + words + `"}`
	}
	r.text = "Yes, delete the old pods."
	r.refused("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a1","message":"yes"}`, "invalid_arguments")
	r.refused("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a1","decision":"once"}`, "invalid_arguments")
	r.refused("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a1","decision":"always","userWords":"x"}`, "invalid_arguments")
	// rule 7 style: a follow-up hears whoever talks after a reply; an event turn
	// has nobody to approve, even for its own task
	r.refused("task_reply", followUp, reply("once", "delete the old pods"), "wake_required")
	r.taskID = tk.ShortID
	r.refused("task_reply", event, reply("once", "delete the old pods"), "not_in_event_turn")
	r.refused("task_reply", event, `{"id":"`+tk.ShortID+`","token":"a1","message":"delete the old pods"}`, "not_in_event_turn")
	r.taskID = ""
	r.values.HermesApprovals = "dashboard_only"
	r.refused("task_reply", wake, reply("once", "delete the old pods"), "dashboard_only")
	r.values.HermesApprovals = "voice"
	// the words are the user's own, quoted from this turn: a near miss is not
	// tried again, so the model never hunts for a passing substring
	for _, c := range []struct{ text, words string }{
		{"Yes, delete the old pods.", "delete all the pods"}, {"yes", "Yes, delete the old pods"},
	} {
		r.text = c.text
		e := r.refused("task_reply", wake, reply("once", c.words), "explicit_decision_required")
		if e.Retryable || !strings.Contains(e.Message, "word for word") {
			t.Fatalf("%q: %+v", c.words, e)
		}
	}
	// and give a decision about this operation: no bare yes, no negation, no
	// hypothetical, an approval word or a word of the question (v1's gates)
	for _, c := range []struct{ text, words string }{
		{"Yes.", "yes"}, {"Да, давай!", "да давай"}, {"OK, go ahead", "ok go ahead"}, {"угу", "Угу"},
		{"Go for it!", "go for it"}, {"Согласен.", "согласен"},
		{"What's the weather tomorrow?", "weather tomorrow"},
		{"Don't delete the old pods.", "delete the old pods"}, {"No, never delete the old pods", "delete the old pods"},
		{"Delete the old pods, wait, stop", "delete the old pods"}, {"Нет, не удаляй старые поды", "старые поды"},
		{"Стоп, подожди, разрешаю", "разрешаю"},
		{"What if you delete the old pods?", "delete the old pods"}, {"Если удалишь старые поды, что будет?", "удалишь старые поды"},
	} {
		r.text = c.text
		e := r.refused("task_reply", wake, reply("once", c.words), "explicit_decision_required")
		if e.Retryable {
			t.Fatalf("%q retryable: %+v", c.text, e)
		}
	}
	if len(r.k.Approved) != 0 {
		t.Fatalf("approved %v", r.k.Approved)
	}
	r.text = "Yes, delete the old pods."
	r.refused("task_reply", wake, reply("once", "  "), "invalid_arguments")
	r.refused("task_reply", button, `{"id":"`+tk.ShortID+`","token":"a0","decision":"once","userWords":"delete the old pods"}`, "question_changed")
	if got := r.ok("task_reply", button, reply("once", "yes, DELETE the old pods")); got["status"] != "running" {
		t.Fatalf("approve %v", got)
	}
	if len(r.k.Approved) != 1 || r.k.Approved[0] != tk.ExternalRef+":a1:once" {
		t.Fatalf("approved %v", r.k.Approved)
	}
	var answer string
	if err := r.pool.QueryRow(context.Background(), `SELECT answer::text FROM task_events WHERE task_id = $1 AND kind = 'answered'`, tk.ID).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if want := `{"via": "voice", "words": "yes, DELETE the old pods", "device": "` + r.dev + `", "decision": "once"}`; answer != want {
		t.Fatalf("evidence %s", answer)
	}
	// an approval word does, for any question
	r.tell(r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods in staging?", Token: "a4"}))
	r.text = "Разрешаю."
	r.ok("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a4","decision":"once","userWords":"Разрешаю"}`)
	// a denial is safe: a plain no will do
	r.tell(r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Wipe the disk?", Token: "a2"}))
	r.text = "Нет."
	r.ok("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a2","decision":"deny","userWords":"нет"}`)
	if len(r.k.Approved) != 3 || r.k.Approved[1] != tk.ExternalRef+":a4:once" || r.k.Approved[2] != tk.ExternalRef+":a2:deny" {
		t.Fatalf("approved %v", r.k.Approved)
	}
	// the question's own labels and generic verbs name no operation
	r.tell(r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Command: rm -rf ./old-pods", Token: "a5"}))
	for _, said := range []string{"Run the command.", "Execute the command", "Выполни команду"} {
		r.text = said
		r.refused("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a5","decision":"once","userWords":"`+said+`"}`, "explicit_decision_required")
	}
	r.text = "Delete the old pods."
	r.ok("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a5","decision":"once","userWords":"delete the old pods"}`)
	if len(r.k.Approved) != 4 || r.k.Approved[3] != tk.ExternalRef+":a5:once" {
		t.Fatalf("approved %v", r.k.Approved)
	}
	r.tell(r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Wipe it again?", Token: "a3"}))
	r.text = "yes, wipe it"
	r.k.ApproveErr = kindCode("hermes_stale")
	e := r.refused("task_reply", wake, `{"id":"`+tk.ShortID+`","token":"a3","decision":"once","userWords":"wipe it"}`, "question_changed")
	if e.Retryable {
		t.Fatalf("a changed approval is retried with old words: %+v", e)
	}
}

// an approval is a plain decision: a refusal, a denial, a hypothetical or a
// question approves nothing, whichever words the model quotes from it
func TestTaskApprovalGate(t *testing.T) {
	r := newTaskRig(t)
	en := r.tell(r.ask(r.start("en"), tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods?", Token: "a1"}))
	ru := r.tell(r.ask(r.start("ru"), tasks.Question{Kind: tasks.QuestionApproval, Text: "Удалить старые поды?", Token: "a2"}))
	for _, c := range []struct {
		tk          tasks.Task
		text, words string
		why         string
	}{
		{en, "You cannot delete the old pods", "delete the old pods", "negation"},
		{en, "Nope, delete the old pods", "delete the old pods", "negation"},
		{en, "Nah, delete the old pods", "delete the old pods", "negation"},
		{en, "Delete nothing, the old pods stay", "the old pods", "negation"},
		{en, "Delete none of the old pods", "the old pods", "negation"},
		{en, "Deny it, delete the old pods", "delete the old pods", "negation"},
		{en, "I refuse to delete the old pods", "delete the old pods", "negation"},
		{en, "Reject it, delete the old pods", "delete the old pods", "negation"},
		{ru, "Удаляй без меня старые поды", "старые поды", "negation"},
		{ru, "Удалить старые поды нельзя", "удалить старые поды", "negation"},
		{ru, "Ничего страшного, удаляй старые поды", "удаляй старые поды", "negation"},
		{ru, "Запрещаю удалять старые поды", "удалять старые поды", "negation"},
		{ru, "Отклони, удалить старые поды", "удалить старые поды", "negation"},
		{en, "You would delete the old pods then", "delete the old pods", "would happen"},
		{en, "You could delete the old pods", "delete the old pods", "would happen"},
		{ru, "Удалить бы старые поды", "старые поды", "would happen"},
		{ru, "Удалить ли старые поды", "старые поды", "would happen"},
		{en, "Delete the old pods?", "delete the old pods", "asked a question"},
		{ru, "Удалить старые поды?", "удалить старые поды", "asked a question"},
		{en, "Alice, what happens when you delete the old pods", "delete the old pods", "asked a question"},
		{en, "Is deleting the old pods safe", "deleting the old pods", "asked a question"},
		{en, "Can you delete the old pods", "delete the old pods", "asked a question"},
		{en, "Do delete the old pods", "delete the old pods", "asked a question"},
		{en, "Alicia. Will deleting the old pods hurt", "deleting the old pods", "asked a question"},
		{ru, "Алиса, кто удалит старые поды", "удалит старые поды", "asked a question"},
		{ru, "Разве удалить старые поды", "удалить старые поды", "asked a question"},
	} {
		r.text = c.text
		args := `{"id":"` + c.tk.ShortID + `","token":"` + c.tk.Question.Token + `","decision":"once","userWords":"` + c.words + `"}`
		e := r.refused("task_reply", wake, args, "explicit_decision_required")
		if e.Retryable || !strings.Contains(e.Message, c.why) {
			t.Errorf("%q: %+v, want %q", c.text, e, c.why)
		}
	}
	if len(r.k.Approved) != 0 {
		t.Fatalf("approved %v", r.k.Approved)
	}
	// the wake word is cut before the first word counts, and a plain decision passes
	r.text = "Alice, delete the old pods."
	r.ok("task_reply", wake, `{"id":"`+en.ShortID+`","token":"a1","decision":"once","userWords":"delete the old pods"}`)
	r.text = "Алиса, удали старые поды."
	r.ok("task_reply", wake, `{"id":"`+ru.ShortID+`","token":"a2","decision":"once","userWords":"удали старые поды"}`)
}

// what each turn may call: an event turn Read tools and task_reply; a
// microphone turn tainted by a task's text Read tools and the policy-gated
// answers task_reply and task_cancel (Ruling M5b-R4); never task_start
func TestTaskToolsOffered(t *testing.T) {
	r := newTaskRig(t)
	names := func(ts []agent.Tool) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(r.reg.Offer(nil, false, wake)); got != "task_start,task_list,task_inspect,task_reply,task_cancel" {
		t.Fatalf("wake %s", got)
	}
	for _, tainted := range []bool{false, true} {
		if got := names(r.reg.Offer(nil, tainted, event)); got != "task_list,task_inspect,task_reply" {
			t.Fatalf("event %v: %s", tainted, got)
		}
	}
	if got := names(r.reg.Offer(nil, true, followUp)); got != "task_list,task_inspect,task_reply,task_cancel" {
		t.Fatalf("tainted %s", got)
	}
}
