package hermes_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// every user or hermes text in these tests carries this mark; none may reach a log
const private = "PRIVATE"

type krig struct {
	t       *testing.T
	f       *fakeHermes
	pool    *pgxpool.Pool
	logs    *syncBuf
	k       *hermes.Kind
	m       *tasks.Manager
	timeout time.Duration
	opts    []func(*hermes.Deps)
	stop    func()
}

func newKRig(t *testing.T, opts ...func(*hermes.Deps)) *krig {
	r := &krig{t: t, f: newFake(t), pool: storetest.Pool(t), logs: &syncBuf{}, timeout: time.Second, opts: opts}
	t.Cleanup(func() {
		r.halt()
		logs := r.logs.String()
		for _, secret := range []string{private, leak, apiKey} {
			if strings.Contains(logs, secret) {
				t.Errorf("log carries %q:\n%s", secret, logs)
			}
		}
	})
	return r
}

// boot builds a kind and a manager on the rig's database and starts both, as a server start does.
func (r *krig) boot() *krig {
	d := hermes.Deps{
		Client: r.f.client(r.t, r.timeout), Pool: r.pool, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
		PollEvery: 20 * time.Millisecond, Backoff: []time.Duration{10 * time.Millisecond}, StopWait: 2 * time.Second,
	}
	for _, o := range r.opts {
		o(&d)
	}
	r.k = hermes.NewKind(d)
	r.m = tasks.NewManager(tasks.ManagerDeps{Store: tasks.NewStore(r.pool), Bus: events.NewBus(0), Log: d.Log})
	r.m.Register(r.k)
	ctx, cancel := context.WithCancel(context.Background())
	stopK := r.k.Run(ctx)
	stopM := r.m.Run(ctx)
	r.stop = func() { cancel(); stopM(); stopK() }
	return r
}

func (r *krig) halt() {
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
}

func (r *krig) start(instr string) tasks.Task {
	r.t.Helper()
	tk, err := r.m.Start(bg(), "hermes", tasks.Spec{Instructions: instr})
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

func (r *krig) get(tk tasks.Task) tasks.Task {
	r.t.Helper()
	got, err := r.m.Get(bg(), tk.ShortID)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *krig) wait(tk tasks.Task, status string) tasks.Task {
	r.t.Helper()
	var got tasks.Task
	eventually(r.t, tk.ShortID+" "+status, func() bool {
		got = r.get(tk)
		return got.Status == status
	})
	return got
}

func (r *krig) run(tk tasks.Task) string { return r.f.latest(tk.ExternalRef) }

func (r *krig) set(tk tasks.Task, fn func(*fakeRun)) { r.f.set(r.run(tk), fn) }

// token is the token of the question the task asks now, as its reader sees it.
func (r *krig) token(tk tasks.Task) string {
	if q := r.get(tk).Question; q != nil {
		return q.Token
	}
	return ""
}

func (r *krig) send(tk tasks.Task, msg string) (tasks.Task, error) {
	return r.m.Send(bg(), tk.ShortID, r.token(tk), msg)
}

func (r *krig) approve(tk tasks.Task, decision string) (tasks.Task, error) {
	return r.m.Approve(bg(), tk.ShortID, r.token(tk), decision, tasks.Evidence{Via: tasks.ViaDashboard})
}

func kindCode(err error) string {
	var ke *tasks.KindError
	if errors.As(err, &ke) {
		return ke.Code
	}
	return ""
}

func TestStartRetriesIntoOneRun(t *testing.T) {
	r := newKRig(t)
	r.timeout = 100 * time.Millisecond
	r.f.stallCreates, r.f.stall = 1, 400*time.Millisecond
	r.boot()
	instr := "write a haiku " + private + "\nabout go"
	tk := r.start(instr)
	if tk.Status != tasks.StatusRunning || tk.ExternalRef != "cadence-"+tk.ID.String() {
		t.Fatalf("task %+v", tk)
	}
	creates := r.f.callsTo("POST", "/v1/runs")
	if len(creates) != 2 || r.f.runCount() != 1 {
		t.Fatalf("creates %d runs %d", len(creates), r.f.runCount())
	}
	a, b := creates[0], creates[1]
	if a.Key == "" || a.Key != b.Key || !strings.Contains(a.Key, tk.ID.String()) || !maps.Equal(a.Body, b.Body) {
		t.Fatalf("retry changed the submission: %+v %+v", a, b)
	}
	if a.Body["input"] != instr || a.Body["session_id"] != tk.ExternalRef ||
		!strings.Contains(a.Body["instructions"].(string), "delegated by the Cadence voice assistant") {
		t.Fatalf("submission %+v", a.Body)
	}
}

func TestLostStartLeavesNoRunBehind(t *testing.T) {
	r := newKRig(t)
	r.timeout = 50 * time.Millisecond
	r.f.stallCreates, r.f.stall = 1000, 200*time.Millisecond
	r.boot()
	ctx, cancel := context.WithTimeout(bg(), 120*time.Millisecond)
	defer cancel()
	if _, err := r.m.Start(ctx, "hermes", tasks.Spec{Instructions: "x"}); !errors.Is(err, tasks.ErrStartFailed) {
		t.Fatalf("start %v", err)
	}
	// hermes made the run all the same: a replay of the key finds it and stops it
	r.f.mu.Lock()
	r.f.stallCreates = 0
	r.f.mu.Unlock()
	eventually(t, "the stop", func() bool { return len(r.f.callsTo("POST", "/run-1/stop")) == 1 })
	if n := r.f.runCount(); n != 1 {
		t.Fatalf("%d runs", n)
	}
}

func TestIncompatibleServerIsRefused(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.Reprobe = 30 * time.Millisecond })
	r.f.caps = strings.Replace(capsOK, `"durable":true`, `"durable":false`, 1)
	r.boot()
	tk, err := r.m.Start(bg(), "hermes", tasks.Spec{Instructions: "x"})
	if !errors.Is(err, tasks.ErrStartFailed) || tk.Status != tasks.StatusFailed || tk.ErrorCode != "start_failed" {
		t.Fatalf("%+v %v", tk, err)
	}
	if n := len(r.f.callsTo("POST", "/v1/runs")); n != 0 {
		t.Fatalf("%d runs submitted to an incompatible server", n)
	}
	// an upgraded server is taken again after a slow re-probe
	r.f.mu.Lock()
	r.f.caps = capsOK
	r.f.mu.Unlock()
	eventually(t, "the re-probe", func() bool {
		tk, err := r.m.Start(bg(), "hermes", tasks.Spec{Instructions: "x"})
		return err == nil && tk.Status == tasks.StatusRunning
	})
}

func TestRunCapacity(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.MaxRuns = 2 })
	r.boot()
	a, _ := r.start("a"), r.start("b")
	if _, err := r.m.Start(bg(), "hermes", tasks.Spec{Instructions: "c"}); !errors.Is(err, tasks.ErrStartFailed) {
		t.Fatalf("third run: %v", err)
	}
	if _, err := r.k.Start(bg(), tasks.Spec{TaskID: uuid.New(), Instructions: "c"}); hermes.Code(err) != hermes.CodeCapacity {
		t.Fatalf("kind start over capacity: %v", err)
	}
	if n := r.f.runCount(); n != 2 {
		t.Fatalf("%d runs", n)
	}
	if _, err := r.m.Cancel(bg(), a.ShortID); err != nil {
		t.Fatal(err)
	}
	if tk := r.start("d"); tk.Status != tasks.StatusRunning {
		t.Fatalf("%+v", tk)
	}
}

func TestUnreachableServerFailsTheStartUntilItAnswers(t *testing.T) {
	r := newKRig(t)
	r.f.setOverride(func(string, string) (int, string, bool) { return http.StatusServiceUnavailable, leak, true })
	r.boot()
	ctx, cancel := context.WithTimeout(bg(), 300*time.Millisecond)
	defer cancel()
	if _, err := r.m.Start(ctx, "hermes", tasks.Spec{Instructions: "x"}); !errors.Is(err, tasks.ErrStartFailed) {
		t.Fatalf("start against a down server: %v", err)
	}
	r.f.setOverride(nil)
	if tk := r.start("x"); tk.Status != tasks.StatusRunning {
		t.Fatalf("%+v", tk)
	}
}

func TestPollToDone(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) { fr.Status = "running" })
	eventually(t, "progress", func() bool { return r.get(tk).Progress == "running" })
	r.set(tk, func(fr *fakeRun) { fr.Status, fr.Output = "completed", "the answer "+private })
	got := r.wait(tk, tasks.StatusDone)
	if got.Result != "the answer "+private || got.ErrorCode != "" {
		t.Fatalf("done %+v", got)
	}
}

func TestPollToFailedAndCancelled(t *testing.T) {
	r := newKRig(t).boot()
	cases := map[string][2]string{
		"failed":      {tasks.StatusFailed, hermes.CodeFailed},
		"interrupted": {tasks.StatusFailed, hermes.CodeInterrupted},
		"cancelled":   {tasks.StatusCancelled, ""},
	}
	for upstream, want := range cases {
		tk := r.start("x")
		r.set(tk, func(fr *fakeRun) { fr.Status = upstream })
		if got := r.wait(tk, want[0]); got.ErrorCode != want[1] {
			t.Errorf("%s: %+v", upstream, got)
		}
	}
}

func TestClarifyAnsweredBySend(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	if _, err := r.m.Send(bg(), tk.ShortID, "", "nothing asked"); err == nil {
		t.Fatal("a send with no question went through")
	}
	r.set(tk, func(fr *fakeRun) {
		fr.Status = "running"
		fr.Controls = []map[string]any{clarify("p1", 1, "Which color "+private+"?", "red", "blue")}
	})
	got := r.wait(tk, tasks.StatusWaitingInput)
	if q := got.Question; q == nil || q.Kind != tasks.QuestionClarify || q.Text != "Which color "+private+"?" ||
		strings.Join(q.Options, ",") != "red,blue" {
		t.Fatalf("question %+v", got.Question)
	}
	got, err := r.send(tk, "blue "+private)
	if err != nil || got.Status != tasks.StatusRunning {
		t.Fatalf("send %+v %v", got, err)
	}
	ans := r.f.callsTo("POST", "/clarify")
	if len(ans) != 1 || ans[0].Body["request_id"] != "p1" || ans[0].Body["execution_generation"] != float64(1) ||
		ans[0].Body["answer"] != "blue "+private {
		t.Fatalf("clarify %+v", ans)
	}
	time.Sleep(100 * time.Millisecond) // a few polls: the answered prompt is not asked again
	if got := r.get(tk); got.Status != tasks.StatusRunning {
		t.Fatalf("asked again: %+v", got)
	}
	// the kind itself reports the answer taken, whoever passed it on
	r.set(tk, func(fr *fakeRun) { fr.Controls = []map[string]any{clarify("p2", 1, "Which size?")} })
	r.wait(tk, tasks.StatusWaitingInput)
	if err := r.k.Send(bg(), tk.ExternalRef, r.token(tk), "large"); err != nil {
		t.Fatal(err)
	}
	r.wait(tk, tasks.StatusRunning)
	r.set(tk, func(fr *fakeRun) { fr.Status = "completed" })
	r.wait(tk, tasks.StatusDone)
}

func TestAnswerIsBoundToThePromptSeen(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) {
		fr.Status = "running"
		fr.Controls = []map[string]any{approval("p1", 1, "Delete pod a", "kubectl delete pod a")}
	})
	r.wait(tk, tasks.StatusWaitingInput)
	seen := r.token(tk)
	r.set(tk, func(fr *fakeRun) {
		fr.Controls = []map[string]any{approval("p2", 1, "Delete pod b", "kubectl delete pod b")}
	})
	eventually(t, "the new prompt stored", func() bool {
		q := r.get(tk).Question
		return q != nil && strings.Contains(q.Text, "pod b")
	})
	if seen == "" || seen == r.token(tk) {
		t.Fatalf("tokens %q %q", seen, r.token(tk))
	}
	if _, err := r.m.Approve(bg(), tk.ShortID, seen, tasks.DecisionOnce, tasks.Evidence{Via: tasks.ViaDashboard}); !errors.Is(err, tasks.ErrStale) {
		t.Fatalf("approve with the old token: %v", err)
	}
	if err := r.k.Approve(bg(), tk.ExternalRef, seen, tasks.DecisionOnce); hermes.Code(err) != hermes.CodeStale {
		t.Fatalf("kind approve with the old token: %v", err)
	}
	if err := r.k.Send(bg(), tk.ExternalRef, seen, "yes"); hermes.Code(err) != hermes.CodeStale {
		t.Fatalf("kind send with the old token: %v", err)
	}
	if n := len(r.f.callsTo("POST", "/approval")) + len(r.f.callsTo("POST", "/clarify")); n != 0 {
		t.Fatalf("%d answers posted", n)
	}
	if _, err := r.approve(tk, tasks.DecisionOnce); err != nil {
		t.Fatal(err)
	}
	if ans := r.f.callsTo("POST", "/approval"); len(ans) != 1 || ans[0].Body["request_id"] != "p2" {
		t.Fatalf("approval %+v", ans)
	}
}

func TestSecretPromptIsNeverRelayed(t *testing.T) {
	r := newKRig(t).boot()
	for _, question := range []string{"Paste the deploy API key for " + private, "Введи пароль от " + private} {
		tk := r.start("x")
		r.set(tk, func(fr *fakeRun) { fr.Status, fr.Controls = "running", []map[string]any{clarify("p1", 1, question)} })
		got := r.wait(tk, tasks.StatusWaitingInput)
		if q := got.Question; q == nil || strings.Contains(q.Text, private) || !strings.Contains(q.Text, "secret") {
			t.Fatalf("%q relayed as %+v", question, q)
		}
		if _, err := r.send(tk, "hunter2"); kindCode(err) != hermes.CodeSecretPrompt {
			t.Fatalf("send to a secret prompt: %v", err)
		}
	}
	if n := len(r.f.callsTo("POST", "/clarify")); n != 0 {
		t.Fatalf("%d answers posted", n)
	}
	if !strings.Contains(r.logs.String(), "hermes_secret_prompt") {
		t.Fatal("no hermes_secret_prompt log")
	}
}

func TestApprovalOnlyThroughApprove(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) {
		fr.Status = "waiting_for_approval"
		fr.Controls = []map[string]any{approval("p2", 4, "Delete the cache volume "+private, "rm -rf /cache")}
	})
	got := r.wait(tk, tasks.StatusWaitingInput)
	if q := got.Question; q == nil || q.Kind != tasks.QuestionApproval ||
		q.Text != "Delete the cache volume "+private+"\nCommand: rm -rf /cache" || strings.Join(q.Options, ",") != "once,deny" {
		t.Fatalf("question %+v", got.Question)
	}
	if _, err := r.send(tk, "yes do it"); kindCode(err) != hermes.CodeApprovalPending {
		t.Fatalf("send answered an approval: %v", err)
	}
	if n := len(r.f.callsTo("POST", "/clarify")) + len(r.f.callsTo("POST", "/approval")); n != 0 {
		t.Fatalf("send reached hermes: %d", n)
	}
	if err := r.k.Approve(bg(), tk.ExternalRef, r.token(tk), "always"); hermes.Code(err) != hermes.CodeBadDecision {
		t.Fatalf("bad decision: %v", err)
	}
	if got, err := r.approve(tk, tasks.DecisionOnce); err != nil || got.Status != tasks.StatusRunning {
		t.Fatalf("approve %+v %v", got, err)
	}
	ans := r.f.callsTo("POST", "/approval")
	if len(ans) != 1 || ans[0].Body["choice"] != "once" || ans[0].Body["request_id"] != "p2" || ans[0].Body["execution_generation"] != float64(4) {
		t.Fatalf("approval %+v", ans)
	}

	r.set(tk, func(fr *fakeRun) { fr.Controls = []map[string]any{approval("p3", 0, "Push to main", "git push")} })
	r.wait(tk, tasks.StatusWaitingInput)
	if _, err := r.approve(tk, tasks.DecisionDeny); err != nil {
		t.Fatal(err)
	}
	if ans := r.f.callsTo("POST", "/approval"); len(ans) != 2 || ans[1].Body["choice"] != "deny" || ans[1].Body["request_id"] != "p3" {
		t.Fatalf("deny %+v", ans)
	}
}

func TestChangedApprovalIsNeverApproved(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.PollEvery = 400 * time.Millisecond })
	r.boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) {
		fr.Status = "running"
		fr.Controls = []map[string]any{approval("p1", 1, "Delete pod a", "kubectl delete pod a")}
	})
	r.wait(tk, tasks.StatusWaitingInput)
	// hermes swaps the prompt before the user's answer lands: same id with
	// another operation, then a new generation
	for _, next := range []map[string]any{
		approval("p1", 1, "Delete pod b", "kubectl delete pod b"),
		approval("p1", 2, "Delete pod c", "kubectl delete pod c"),
	} {
		r.set(tk, func(fr *fakeRun) { fr.Controls = []map[string]any{next} })
		if _, err := r.approve(tk, tasks.DecisionOnce); err == nil {
			t.Fatalf("approved %v, a prompt the user never saw", next["command"])
		}
		if n := len(r.f.callsTo("POST", "/approval")); n != 0 {
			t.Fatalf("%d approvals posted", n)
		}
		eventually(t, "the new prompt", func() bool {
			q := r.get(tk).Question
			return q != nil && strings.Contains(q.Text, next["command"].(string))
		})
	}
	if _, err := r.approve(tk, tasks.DecisionOnce); err != nil {
		t.Fatal(err)
	}
	if ans := r.f.callsTo("POST", "/approval"); len(ans) != 1 || ans[0].Body["execution_generation"] != float64(2) {
		t.Fatalf("approval %+v", ans)
	}
}

func TestUnshowablePromptIsAnsweredInHermes(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) {
		fr.Status = "running"
		fr.Controls = []map[string]any{approval("p1", 1, strings.Repeat(private, 300), "rm")}
	})
	got := r.wait(tk, tasks.StatusWaitingInput)
	if q := got.Question; q == nil || q.Kind != tasks.QuestionApproval || strings.Contains(q.Text, private) || len(q.Options) != 0 {
		t.Fatalf("question %+v", q)
	}
	if _, err := r.approve(tk, tasks.DecisionOnce); err == nil {
		t.Fatal("approved a prompt nobody could read")
	}
	if n := len(r.f.callsTo("POST", "/approval")); n != 0 {
		t.Fatalf("%d approvals posted", n)
	}
	// answered in hermes itself: the task runs on
	r.set(tk, func(fr *fakeRun) { fr.Controls = nil })
	r.wait(tk, tasks.StatusRunning)
}

func TestCancel(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	got, err := r.m.Cancel(bg(), tk.ShortID)
	if err != nil || got.Status != tasks.StatusCancelled {
		t.Fatalf("cancel %+v %v", got, err)
	}
	if n := len(r.f.callsTo("POST", "/stop")); n != 1 {
		t.Fatalf("%d stops", n)
	}
	if fr := r.run(tk); fr == "" {
		t.Fatal("no run")
	}
}

func TestCancelThatHermesCannotConfirm(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.StopWait = 200 * time.Millisecond })
	r.boot()
	// the stop is taken but the run goes on: never claimed cancelled until hermes says so
	r.f.stopTo = ""
	slow := r.start("x")
	if _, err := r.m.Cancel(bg(), slow.ShortID); err == nil {
		t.Fatal("cancel claimed before hermes confirmed it")
	}
	if got := r.get(slow); got.Status != tasks.StatusRunning {
		t.Fatalf("%+v", got)
	}
	r.set(slow, func(fr *fakeRun) { fr.Status = "cancelled" })
	r.wait(slow, tasks.StatusCancelled)

	// a stop with no answer leaves the task unknown
	lost := r.start("x")
	r.f.setOverride(func(_, path string) (int, string, bool) {
		return http.StatusInternalServerError, leak, strings.HasSuffix(path, "/stop")
	})
	if _, err := r.m.Cancel(bg(), lost.ShortID); err == nil {
		t.Fatal("cancel claimed after a failed stop")
	}
	if got := r.wait(lost, tasks.StatusUnknown); got.ErrorCode != hermes.CodeStopUnknown {
		t.Fatalf("%+v", got)
	}
}

func TestCancelCutShortIsStoppedByThePolls(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	// the caller gives up before the stop is answered, and that stop never lands
	var stops atomic.Int32
	r.f.setOverride(func(_, path string) (int, string, bool) {
		if !strings.HasSuffix(path, "/stop") || stops.Add(1) > 1 {
			return 0, "", false
		}
		time.Sleep(300 * time.Millisecond)
		return http.StatusAccepted, `{"status":"running"}`, true
	})
	ctx, cancel := context.WithTimeout(bg(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.m.Cancel(ctx, tk.ShortID); err == nil {
		t.Fatal("cancel claimed without a stop")
	}
	got := r.wait(tk, tasks.StatusCancelled)
	if n := len(r.f.callsTo("POST", "/stop")); n != 2 || got.ErrorCode != "" {
		t.Fatalf("%d stops, %+v", n, got)
	}
}

func TestRestartResumesPolling(t *testing.T) {
	r := newKRig(t).boot()
	running := r.start("x")
	waiting := r.start("y")
	r.set(waiting, func(fr *fakeRun) {
		fr.Status = "running"
		fr.Controls = []map[string]any{clarify("p1", 1, "Which branch?")}
	})
	r.wait(waiting, tasks.StatusWaitingInput)
	r.halt()
	gets := len(r.f.callsTo("GET", "/"+r.run(running)))

	r.boot()
	eventually(t, "polls after the restart", func() bool { return len(r.f.callsTo("GET", "/"+r.run(running))) > gets+2 })
	if got := r.get(waiting); got.Status != tasks.StatusWaitingInput || got.Question == nil {
		t.Fatalf("waiting task after restart %+v", got)
	}
	if _, err := r.send(waiting, "main"); err != nil {
		t.Fatal(err)
	}
	if ans := r.f.callsTo("POST", "/clarify"); len(ans) != 1 || ans[0].Body["request_id"] != "p1" {
		t.Fatalf("clarify %+v", ans)
	}
	r.set(running, func(fr *fakeRun) { fr.Status, fr.Output = "completed", "ok" })
	if got := r.wait(running, tasks.StatusDone); got.Result != "ok" {
		t.Fatalf("%+v", got)
	}
}

func TestVanishedRunIsUnknown(t *testing.T) {
	r := newKRig(t).boot()
	live := r.start("x")
	r.f.forget(r.run(live))
	if got := r.wait(live, tasks.StatusUnknown); got.ErrorCode != hermes.CodeRunGone {
		t.Fatalf("%+v", got)
	}
	// across a restart: a run hermes no longer knows is never restarted
	parked := r.start("y")
	r.halt()
	r.f.forget(r.run(parked))
	creates := len(r.f.callsTo("POST", "/v1/runs"))
	r.boot()
	if got := r.wait(parked, tasks.StatusUnknown); got.ErrorCode != hermes.CodeRunGone {
		t.Fatalf("%+v", got)
	}
	if n := len(r.f.callsTo("POST", "/v1/runs")); n != creates {
		t.Fatalf("resubmitted: %d creates", n-creates)
	}
}

func TestUnconfirmableRunTurnsUnknown(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.Unconfirmed = 150 * time.Millisecond })
	r.boot()
	tk := r.start("x")
	r.f.setOverride(func(method, path string) (int, string, bool) {
		return http.StatusBadGateway, leak, method == http.MethodGet && strings.HasPrefix(path, "/v1/runs/")
	})
	if got := r.wait(tk, tasks.StatusUnknown); got.ErrorCode != hermes.CodeUnconfirmed {
		t.Fatalf("%+v", got)
	}
}

func TestPollConcurrencyIsBounded(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.MaxPolls, d.MaxRuns = 3, 16 })
	gate := make(chan struct{})
	r.f.gate = gate
	r.boot()
	for range 10 {
		r.start("x")
	}
	eventually(t, "three polls in flight", func() bool { return r.f.peak() == 3 })
	time.Sleep(200 * time.Millisecond)
	close(gate)
	time.Sleep(100 * time.Millisecond)
	if p := r.f.peak(); p != 3 {
		t.Fatalf("peak %d polls in flight", p)
	}
}

func TestPausedPromptResumesInTheSameSession(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("ORIGINAL TASK")
	first := r.run(tk)
	q := clarify("p1", 1, "Which env "+private+"?")
	q["timeout_s"] = 0.2
	r.set(tk, func(fr *fakeRun) { fr.Status, fr.Controls = "running", []map[string]any{q} })
	r.wait(tk, tasks.StatusWaitingInput)
	// hermes would give up on the prompt: the run is stopped and the question kept
	eventually(t, "the pause", func() bool { return len(r.f.callsTo("POST", "/"+first+"/stop")) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := r.get(tk); got.Status != tasks.StatusWaitingInput || got.Question == nil {
		t.Fatalf("paused task %+v", got)
	}
	if _, err := r.send(tk, "staging "+private); err != nil {
		t.Fatal(err)
	}
	creates := r.f.callsTo("POST", "/v1/runs")
	if len(creates) != 2 || creates[1].Key == creates[0].Key || creates[1].Body["session_id"] != tk.ExternalRef {
		t.Fatalf("resume %+v", creates)
	}
	input, _ := creates[1].Body["input"].(string)
	if !strings.Contains(input, "Which env "+private+"?") || !strings.Contains(input, "staging "+private) || strings.Contains(input, "ORIGINAL TASK") {
		t.Fatalf("resume input %q", input)
	}
	if got := r.get(tk); got.Status != tasks.StatusRunning {
		t.Fatalf("%+v", got)
	}
	second := r.run(tk)
	if second == first {
		t.Fatal("no new run")
	}
	r.f.set(second, func(fr *fakeRun) { fr.Status, fr.Output = "completed", "done" })
	r.wait(tk, tasks.StatusDone)
}

func TestExpiredApprovalIsNeverReplayed(t *testing.T) {
	r := newKRig(t).boot()
	for _, decision := range []string{tasks.DecisionOnce, tasks.DecisionDeny} {
		tk := r.start("x")
		first := r.run(tk)
		a := approval("p1", 1, "Drop table "+private, "psql -c drop")
		a["timeout_s"] = 0.1
		r.set(tk, func(fr *fakeRun) { fr.Status, fr.Controls = "running", []map[string]any{a} })
		r.wait(tk, tasks.StatusWaitingInput)
		eventually(t, "the pause", func() bool { return len(r.f.callsTo("POST", "/"+first+"/stop")) == 1 })
		eventually(t, "the stop to land", func() bool {
			r.f.mu.Lock()
			defer r.f.mu.Unlock()
			return r.f.runs[first].Status == "cancelled"
		})
		time.Sleep(60 * time.Millisecond)
		if _, err := r.approve(tk, decision); err != nil {
			t.Fatal(err)
		}
		if n := len(r.f.callsTo("POST", "/"+first+"/approval")); n != 0 {
			t.Fatalf("%s: the expired approval was posted", decision)
		}
		creates := r.f.callsTo("POST", "/v1/runs")
		input, _ := creates[len(creates)-1].Body["input"].(string)
		want := "fresh native approval"
		if decision == tasks.DecisionDeny {
			want = "The user denied this operation"
		}
		if !strings.Contains(input, want) || !strings.Contains(input, "psql -c drop") {
			t.Fatalf("%s: resume input %q", decision, input)
		}
	}
}

func TestInspect(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) {
		fr.Status, fr.Output = "running", "step one "+private
		fr.Controls = []map[string]any{clarify("p1", 1, "Which?")}
	})
	r.wait(tk, tasks.StatusWaitingInput)
	s, err := r.m.Inspect(bg(), tk.ShortID, tasks.DetailSummary)
	if err != nil || s.Status != tasks.StatusWaitingInput || s.Question == nil || s.Question.Text != "Which?" || s.Transcript != "" {
		t.Fatalf("summary %+v %v", s, err)
	}
	s, err = r.m.Inspect(bg(), tk.ShortID, tasks.DetailTranscript)
	if err != nil || s.Transcript != "step one "+private {
		t.Fatalf("transcript %+v %v", s, err)
	}
}

func TestPauseTimerSurvivesARestart(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.PauseLead = time.Second })
	r.boot()
	tk := r.start("x")
	first := r.run(tk)
	q := clarify("p1", 1, "Which env?")
	q["timeout_s"] = 3 // paused two seconds after it shows
	r.set(tk, func(fr *fakeRun) { fr.Status, fr.Controls = "running", []map[string]any{q} })
	r.wait(tk, tasks.StatusWaitingInput)
	r.halt()
	time.Sleep(2200 * time.Millisecond)
	r.boot()
	// the prompt showed over two seconds ago: due at once, not two seconds after the restart
	start := time.Now()
	eventually(t, "the pause", func() bool { return len(r.f.callsTo("POST", "/"+first+"/stop")) == 1 })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("paused %v after the restart", d)
	}
}

func TestFailedResumeKeyIsNeverReused(t *testing.T) {
	r := newKRig(t, func(d *hermes.Deps) { d.SubmitFor = 100 * time.Millisecond })
	r.boot()
	tk := r.start("x")
	q := clarify("p1", 1, "Which env?")
	q["timeout_s"] = 0.1
	r.set(tk, func(fr *fakeRun) { fr.Status, fr.Controls = "running", []map[string]any{q} })
	r.wait(tk, tasks.StatusWaitingInput)
	first := r.run(tk)
	eventually(t, "the pause", func() bool {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		return r.f.runs[first].Status == "cancelled"
	})
	time.Sleep(60 * time.Millisecond)
	r.f.setOverride(func(method, path string) (int, string, bool) {
		return http.StatusServiceUnavailable, leak, method == http.MethodPost && path == "/v1/runs"
	})
	if _, err := r.send(tk, "staging"); err == nil {
		t.Fatal("resume went through a down server")
	}
	failed := r.f.callsTo("POST", "/v1/runs")
	tried := failed[len(failed)-1].Key
	r.halt()
	r.f.setOverride(nil)
	r.boot()
	eventually(t, "the re-attach", func() bool { return r.get(tk).Status == tasks.StatusWaitingInput })
	eventually(t, "the resume", func() bool {
		_, err := r.send(tk, "staging")
		return err == nil
	})
	creates := r.f.callsTo("POST", "/v1/runs")
	if key := creates[len(creates)-1].Key; key == tried || !strings.HasSuffix(key, "-3") {
		t.Fatalf("resume key %q after failed %q", key, tried)
	}
}

func TestInspectCapsTheResult(t *testing.T) {
	r := newKRig(t).boot()
	tk := r.start("x")
	r.set(tk, func(fr *fakeRun) { fr.Status, fr.Output = "completed", strings.Repeat("é", tasks.MaxResult) })
	r.wait(tk, tasks.StatusDone)
	s, err := r.k.Inspect(bg(), tk.ExternalRef, tasks.DetailSummary)
	if err != nil || s.Status != tasks.StatusDone || len(s.Result) == 0 || len(s.Result) > tasks.MaxResult {
		t.Fatalf("%s %d %v", s.Status, len(s.Result), err)
	}
	s, err = r.k.Inspect(bg(), tk.ExternalRef, tasks.DetailTranscript)
	if err != nil || len(s.Transcript) == 0 || len(s.Transcript) > tasks.MaxResult {
		t.Fatalf("transcript %d %v", len(s.Transcript), err)
	}
}
