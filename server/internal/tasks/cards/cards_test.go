package taskcards_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	taskcards "git.shino.boo/devilreef/cadence/server/internal/tasks/cards"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rig struct {
	t      *testing.T
	m      *tasks.Manager
	k      *tasks.FakeKind
	svc    *taskcards.Service
	cs     *cards.Service
	router *calls.Router
	dev    uuid.UUID
	logs   *syncBuf

	mu     sync.Mutex
	sent   []*cadencev2.Envelope
	toasts []string
	quiet  bool
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	d, err := db.New(pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: rand.Text(), Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	bus := events.NewBus(0)
	m := tasks.NewManager(tasks.ManagerDeps{Store: tasks.NewStore(pool), Bus: bus, Log: log})
	k := tasks.NewFakeKind("hermes")
	m.Register(k)
	r := &rig{t: t, m: m, k: k, dev: d.ID.Bytes, logs: logs}
	r.cs = cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }, Log: log})
	r.svc = taskcards.New(taskcards.Deps{
		Manager: m, Cards: r.cs, Log: log,
		Screens: func(_ context.Context, origin string) []string {
			if origin == r.dev.String() {
				return []string{"tv"}
			}
			return nil
		},
		Toast: func(_ context.Context, id string, args []byte) error {
			r.mu.Lock()
			r.toasts = append(r.toasts, id+" "+string(args))
			r.mu.Unlock()
			return nil
		},
		Settings: func(context.Context, string) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			q := settings.QuietHours{}
			if r.quiet {
				q = settings.QuietHours{Enabled: true, Start: "00:00", End: "23:59"}
			}
			return settings.Values{Language: "ru", QuietHours: q, TimeZone: "UTC"}, nil
		},
	})
	r.router = calls.New(func(_ string, env *cadencev2.Envelope) error {
		r.mu.Lock()
		r.sent = append(r.sent, env)
		r.mu.Unlock()
		return nil
	}, log)
	r.router.Register(rpc.TasksList, r.svc.ListMethod())
	r.router.Register(rpc.TasksItem, r.svc.ItemMethod())
	r.router.Register(rpc.CardAction, r.cs.ActionMethod())
	ctx, cancel := context.WithCancel(context.Background())
	stopM := m.Run(ctx)
	stop := r.svc.Run(ctx)
	t.Cleanup(func() { r.router.Close(); cancel(); stop(); stopM() })
	return r
}

// call sends one request and answers its response
func (r *rig) call(id, method string, args any, subscribe bool) *cadencev2.RpcResponse {
	r.t.Helper()
	b, _ := json.Marshal(args)
	r.router.Handle("tv", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: id, Method: method, Json: string(b), Subscribe: subscribe,
	}}})
	var got *cadencev2.RpcResponse
	r.waitFor("the answer to "+id, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, e := range r.sent {
			if resp := e.GetRpcResponse(); resp != nil && resp.GetId() == id {
				got = resp
				return true
			}
		}
		return false
	})
	return got
}

func (r *rig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *rig) act(id string, c *cardsv1.Card, a *cardsv1.Action, value any) *cadencev2.RpcResponse {
	args := map[string]any{"cardId": c.GetId(), "revision": c.GetRevision(), "actionId": a.GetId()}
	if value != nil {
		args["value"] = value
	}
	return r.call(id, rpc.CardAction, args, false)
}

func (r *rig) start(title string) tasks.Task {
	r.t.Helper()
	tk, err := r.m.Start(context.Background(), "hermes", tasks.Spec{Title: title, Instructions: title, Origin: r.dev})
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

func (r *rig) status(short, want string) {
	r.t.Helper()
	r.waitFor("status "+want, func() bool { t, err := r.m.Get(context.Background(), short); return err == nil && t.Status == want })
}

func buttons(c *cardsv1.Card) []*cardsv1.Button { return c.GetRoot().GetCard().GetActions() }

func cardByID(p *cardsv1.Page, suffix string) *cardsv1.Card {
	for _, c := range p.GetCards() {
		if strings.HasSuffix(c.GetId(), suffix) {
			return c
		}
	}
	return nil
}

func TestApprovalFromACard(t *testing.T) {
	r := newRig(t)
	tk := r.start("почисти логи на сервере")
	r.status(tk.ShortID, tasks.StatusRunning)
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &tasks.Question{
		Kind: tasks.QuestionApproval, Text: "Delete the old logs\nCommand: rm -rf /var/log/old", Options: []string{"once", "deny"}, Token: "a1",
	}})
	r.status(tk.ShortID, tasks.StatusWaitingInput)

	list := r.call("l1", rpc.TasksList, map[string]any{}, false)
	if list.GetStatus() != rpc.StatusOK || len(list.GetPage().GetCards()) != 1 {
		t.Fatalf("list %v", list)
	}
	if err := pcards.ValidatePage(list.GetPage()); err != nil {
		t.Fatal(err)
	}
	listed := list.GetPage().GetCards()[0]
	if listed.GetRoot().GetCard().GetTitle() != "почисти логи на сервере" || listed.GetRoot().GetCard().GetTone() != cardsv1.Tone_TONE_WARNING {
		t.Fatalf("listed %v", listed)
	}
	open := r.act("o1", listed, buttons(listed)[0].GetAction(), nil)
	// a card action's page is its typed answer
	if open.GetStatus() != rpc.StatusOK || open.GetCardAction().GetPage() == nil || open.GetPage() != nil {
		t.Fatalf("open %v", open)
	}
	ask := cardByID(open.GetCardAction().GetPage(), "-ask")
	if ask == nil {
		t.Fatalf("no question card in %v", open.GetCardAction().GetPage())
	}
	kids := ask.GetRoot().GetCard().GetChildren()
	if kids[0].GetText().GetText() != "Delete the old logs" || kids[1].GetCode().GetText() != "rm -rf /var/log/old" {
		t.Fatalf("approval %v", kids)
	}
	bs := buttons(ask)
	if len(bs) != 2 || bs[0].GetAction().GetConfirm() == "" || bs[1].GetAction().GetConfirm() == "" || !bs[0].GetPrimary() {
		t.Fatalf("approval buttons %v", bs)
	}
	got := r.act("a1", ask, bs[0].GetAction(), nil)
	if got.GetStatus() != rpc.StatusOK {
		t.Fatalf("approve %v", got)
	}
	if len(r.k.Approved) != 1 || r.k.Approved[0] != tk.ExternalRef+":a1:once" {
		t.Fatalf("approved %v", r.k.Approved)
	}
	r.status(tk.ShortID, tasks.StatusRunning)
	h, err := r.m.History(context.Background(), tk.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]string
	for _, e := range h.Events {
		if len(e.Answer) > 0 {
			_ = json.Unmarshal(e.Answer, &evidence)
		}
	}
	if evidence["via"] != tasks.ViaScreen || evidence["device"] != "tv" || evidence["decision"] != "once" || evidence["words"] != "" {
		t.Fatalf("evidence %v", evidence)
	}
	// the question card was drawn again, and its approval is gone: a second
	// press is refused, never sent to the kind
	again := r.act("a2", ask, bs[1].GetAction(), nil)
	if again.GetStatus() != rpc.StatusExpired {
		t.Fatalf("a press on an old revision: %v", again)
	}
	if len(r.k.Approved) != 1 {
		t.Fatalf("approved twice: %v", r.k.Approved)
	}
	for _, leak := range []string{"почисти", "rm -rf", "Delete the old"} {
		if strings.Contains(r.logs.String(), leak) {
			t.Fatalf("a log line carries %q:\n%s", leak, r.logs)
		}
	}
}

func TestClarifyAndCancelFromCards(t *testing.T) {
	r := newRig(t)
	tk := r.start("выбери цвет")
	r.status(tk.ShortID, tasks.StatusRunning)
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &tasks.Question{
		Kind: tasks.QuestionClarify, Text: "Какой цвет?", Options: []string{"синий", "красный"}, Token: "c1",
	}})
	r.status(tk.ShortID, tasks.StatusWaitingInput)
	item := r.call("i1", rpc.TasksItem, map[string]string{"taskId": tk.ShortID}, false)
	ask := cardByID(item.GetPage(), "-ask")
	if ask == nil {
		t.Fatalf("item %v", item)
	}
	ch := ask.GetRoot().GetCard().GetChildren()[1].GetChoices()
	if ch == nil || len(ch.GetOptions()) != 2 || ch.GetAction().GetConfirm() == "" {
		t.Fatalf("choices %v", ask)
	}
	if bad := r.act("x", ask, ch.GetAction(), "o9"); bad.GetStatus() != rpc.StatusInvalid {
		t.Fatalf("an unknown option: %v", bad)
	}
	if got := r.act("c", ask, ch.GetAction(), "o1"); got.GetStatus() != rpc.StatusOK {
		t.Fatalf("answer %v", got)
	}
	if len(r.k.Sent) != 1 || r.k.Sent[0] != tk.ExternalRef+":красный" || r.k.Tokens[0] != "c1" {
		t.Fatalf("sent %v %v", r.k.Sent, r.k.Tokens)
	}
	r.status(tk.ShortID, tasks.StatusRunning)
	item = r.call("i2", rpc.TasksItem, map[string]string{"taskId": tk.ShortID}, false)
	head := cardByID(item.GetPage(), "-head")
	cancel := buttons(head)[0]
	if cancel.GetAction().GetConfirm() == "" {
		t.Fatalf("cancel must confirm: %v", cancel)
	}
	if got := r.act("k", head, cancel.GetAction(), nil); got.GetStatus() != rpc.StatusOK {
		t.Fatalf("cancel %v", got)
	}
	r.status(tk.ShortID, tasks.StatusCancelled)
	if nf := r.call("i3", rpc.TasksItem, map[string]string{"taskId": "nope0"}, false); nf.GetStatus() != rpc.StatusNotFound {
		t.Fatalf("unknown task %v", nf)
	}
}

func TestTaskPagesFollowTheManager(t *testing.T) {
	r := newRig(t)
	tk := r.start("долгая задача")
	r.status(tk.ShortID, tasks.StatusRunning)
	r.k.Snapshots[tk.ExternalRef] = tasks.Snapshot{Status: tasks.StatusRunning, Transcript: "строка 1\nстрока 2"}
	item := r.call("s1", rpc.TasksItem, map[string]string{"taskId": tk.ShortID}, true)
	now := cardByID(item.GetPage(), "-now")
	if lt := now.GetRoot().GetCard().GetChildren()[0].GetLongText(); !lt.GetTail() || lt.GetText() != "строка 1\nстрока 2" {
		t.Fatalf("live tail %v", now)
	}
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "progress", Progress: "running: terminal"})
	var page *cardsv1.Page
	r.waitFor("a new page", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, e := range r.sent {
			if ev := e.GetRpcEvent(); ev != nil && ev.GetId() == "s1" && ev.GetPage() != nil {
				page = ev.GetPage()
				return true
			}
		}
		return false
	})
	head := cardByID(page, "-head")
	if head.GetRevision() < 2 || head.GetRoot().GetCard().GetChildren()[1].GetText().GetText() != "Инструмент: terminal" {
		t.Fatalf("head after progress %v", head)
	}
}

func TestToastsWaitForQuietHoursToEnd(t *testing.T) {
	r := newRig(t)
	r.mu.Lock()
	r.quiet = true
	r.mu.Unlock()
	a := r.start("тихая задача")
	r.status(a.ShortID, tasks.StatusRunning)
	r.k.Emit(tasks.Event{Ref: a.ExternalRef, Kind: "done", Result: "ok"})
	r.status(a.ShortID, tasks.StatusDone)
	time.Sleep(200 * time.Millisecond)
	r.mu.Lock()
	n := len(r.toasts)
	r.quiet = false
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("a toast in quiet hours: %v", r.toasts)
	}
	b := r.start("громкая задача")
	r.status(b.ShortID, tasks.StatusRunning)
	r.k.Emit(tasks.Event{Ref: b.ExternalRef, Kind: "failed", ErrorCode: "boom"})
	r.waitFor("the toast", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.toasts) == 1 })
	r.mu.Lock()
	got := r.toasts[0]
	r.mu.Unlock()
	if !strings.HasPrefix(got, "tv ") || !strings.Contains(got, "громкая задача") || !strings.Contains(got, `"kind":"danger"`) {
		t.Fatalf("toast %q", got)
	}
	// «Открыть» is a typed action that opens the task's panel, as display.screen/toast takes it
	if !strings.Contains(got, `"action":{"id":"open","kind":"open","label":"Открыть","ref":"`+b.ShortID+`","route":"task"}`) {
		t.Fatalf("toast %q", got)
	}
	toastSchema := capschema.DisplayScreenCommands["toast"]
	if err := validJSON(toastSchema, strings.TrimPrefix(got, "tv ")); err != nil {
		t.Fatalf("toast %q: %v", got, err)
	}
}

func validJSON(schema, doc string) error {
	s, err := jsonschema.UnmarshalJSON(strings.NewReader(schema))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("s.json", s); err != nil {
		return err
	}
	compiled, err := c.Compile("s.json")
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		return err
	}
	return compiled.Validate(v)
}

// the indicator's counts: running, waiting, and those ended since the screen last listed them
func TestSummaryCountsTheIndicator(t *testing.T) {
	r := newRig(t)
	var heardMu sync.Mutex
	var heard []string
	r.svc.OnSummaryChange(func(dev string) { heardMu.Lock(); heard = append(heard, dev); heardMu.Unlock() })
	a := r.start("одна")
	r.status(a.ShortID, tasks.StatusRunning)
	b := r.start("другая")
	r.status(b.ShortID, tasks.StatusRunning)
	r.k.Emit(tasks.Event{Ref: b.ExternalRef, Kind: "done", Result: "ok"})
	r.status(b.ShortID, tasks.StatusDone)
	sum, err := r.svc.Summary(context.Background(), "tv")
	if err != nil {
		t.Fatal(err)
	}
	if sum.GetRunning() != 1 || sum.GetWaiting() != 0 || sum.GetFinished() != 1 || sum.GetRevision() == 0 {
		t.Fatalf("summary %v", sum)
	}
	r.call("l1", rpc.TasksList, map[string]any{}, false)
	again, err := r.svc.Summary(context.Background(), "tv")
	if err != nil {
		t.Fatal(err)
	}
	if again.GetFinished() != 0 || again.GetRunning() != 1 || again.GetRevision() <= sum.GetRevision() {
		t.Fatalf("after the list %v", again)
	}
	// each task change is heard for every screen, the list for its own
	heardMu.Lock()
	defer heardMu.Unlock()
	if len(heard) < 2 || heard[0] != "" || heard[len(heard)-1] != "tv" {
		t.Fatalf("heard %q", heard)
	}
}
