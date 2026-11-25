package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

// subModule is a Subscribable whose answers are recorded.
type subModule struct {
	mu      sync.Mutex
	answers []subscriptions.Answer
}

func (m *subModule) Module() string { return "t3code" }
func (m *subModule) Events() []string {
	return []string{subscriptions.EventStopped, subscriptions.EventQuestion, subscriptions.EventApproval}
}
func (m *subModule) Validate(string, []string) error { return nil }
func (m *subModule) List(context.Context, string, []string) ([]subscriptions.Entry, error) {
	return nil, nil
}
func (m *subModule) Updates(string) <-chan subscriptions.Update { return nil }
func (m *subModule) Answer(_ context.Context, _ subscriptions.Ref, a subscriptions.Answer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers = append(m.answers, a)
	return nil
}

type subRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	hub    *subscriptions.Hub
	mod    *subModule
	dev    string
	values settings.Values
	byName map[string]agent.Tool
}

func newSubRig(t *testing.T) *subRig {
	t.Helper()
	pool := storetest.Pool(t)
	ctx := context.Background()
	d, err := db.New(pool).CreateDevice(ctx, db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO provider_instances (name, module) VALUES ('work', 't3code')`); err != nil {
		t.Fatal(err)
	}
	r := &subRig{t: t, pool: pool, mod: &subModule{}, dev: d.ID.String(), values: settings.Defaults(), byName: map[string]agent.Tool{}}
	get := func(context.Context, string) (settings.Values, error) { return r.values, nil }
	r.hub = subscriptions.New(subscriptions.Deps{Store: subscriptions.NewStore(pool), Settings: get})
	if err := r.hub.Registry().Register(r.mod); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Subscriptions(tools.SubscriptionDeps{Hub: r.hub, Settings: get}) {
		r.byName[tool.Name] = tool
	}
	return r
}

func (r *subRig) call(name string, trig cadencev2.Trigger, turn, text, args string) (json.RawMessage, *agent.ToolError) {
	r.t.Helper()
	tool, err := agent.Compile(r.byName[name])
	if err != nil {
		r.t.Fatal(err)
	}
	if e := agent.Validate(tool, json.RawMessage(args)); e != nil {
		return nil, e
	}
	return agent.Run(context.Background(), tool, agent.Call{
		DeviceID: r.dev, TurnID: turn, Trigger: trig, Text: text, Args: json.RawMessage(args),
	})
}

func (r *subRig) ask(token, kind string) {
	r.t.Helper()
	ev := subscriptions.EventQuestion
	if kind == subscriptions.QuestionApproval {
		ev = subscriptions.EventApproval
	}
	r.hub.Ingest(context.Background(), r.mod, subscriptions.Update{
		Topic: subscriptions.NewTopic("t3code", "work", "thread", "a"), Event: ev, Title: "Deploy", Ref: subscriptions.Ref{ID: "a"},
		Question: &subscriptions.Question{Kind: kind, Token: token, Text: "Deploy the release to production?",
			Options: []subscriptions.Option{{ID: "blue", Label: "Blue"}, {ID: "green", Label: "Green"}}},
	})
}

const wokenTrig = cadencev2.Trigger_TRIGGER_WAKE

// subscribe, list and unsubscribe act on the session's device, in a follow-up
// too (only rule 7 and approvals need a session the user began)
func TestSubscriptionToolsWoken(t *testing.T) {
	r := newSubRig(t)
	if _, e := r.call("subscribe", cadencev2.Trigger_TRIGGER_FOLLOW_UP, "t1", "", `{"topic":"t3code:work:*"}`); e != nil {
		t.Fatalf("follow-up %+v", e)
	}
	if _, e := r.call("unsubscribe", cadencev2.Trigger_TRIGGER_FOLLOW_UP, "t1", "", `{"topic":"t3code:work:*"}`); e != nil {
		t.Fatalf("follow-up unsubscribe %+v", e)
	}
	out, e := r.call("subscribe", wokenTrig, "t1", "", `{"topic":"t3code:work:thread:a","until":"next_stop"}`)
	if e != nil || !strings.Contains(string(out), `"until":"next_stop"`) || !strings.Contains(string(out), `"delivery":"speaker"`) {
		t.Fatalf("subscribe %s %+v", out, e)
	}
	if _, e := r.call("subscribe", wokenTrig, "t1", "", `{"topic":"t3code:nope:*"}`); e == nil || e.Code != "unknown_instance" {
		t.Fatalf("unknown instance %+v", e)
	}
	out, e = r.call("subscriptions", wokenTrig, "t1", "", `{}`)
	if e != nil || !strings.Contains(string(out), "t3code:work:thread:a") {
		t.Fatalf("list %s %+v", out, e)
	}
	subs, _ := r.hub.List(context.Background(), r.dev)
	if len(subs) != 1 || subs[0].CreatedBy != subscriptions.ByVoice {
		t.Fatalf("stored %+v", subs)
	}
	if _, e := r.call("unsubscribe", wokenTrig, "t1", "", `{"topic":"t3code:work:thread:a"}`); e != nil {
		t.Fatalf("unsubscribe %+v", e)
	}
	if _, e := r.call("unsubscribe", wokenTrig, "t1", "", `{"id":"`+subs[0].ID+`"}`); e == nil || e.Code != "not_found" {
		t.Fatalf("twice %+v", e)
	}
}

// a clarify question is answered by voice in a woken turn, by option ids
func TestSubscriptionReplyClarify(t *testing.T) {
	r := newSubRig(t)
	if _, err := r.hub.Subscribe(context.Background(), subscriptions.Request{Topic: "t3code:work:*", Device: r.dev, CreatedBy: subscriptions.ByAPI}); err != nil {
		t.Fatal(err)
	}
	r.ask("tok-c", subscriptions.QuestionClarify)
	if _, e := r.call("subscription_reply", wokenTrig, "t1", "", `{"token":"tok-c","answers":["red"]}`); e == nil || e.Code != "invalid_arguments" {
		t.Fatalf("not an option %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t1", "", `{"token":"tok-c","decision":"once","userWords":"x"}`); e == nil || e.Code != "invalid_arguments" {
		t.Fatalf("decision %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t1", "", `{"token":"tok-c","answers":["green"]}`); e != nil {
		t.Fatalf("answer %+v", e)
	}
	if len(r.mod.answers) != 1 || r.mod.answers[0].Answers[0] != "green" || r.mod.answers[0].By.Via != subscriptions.ViaVoice {
		t.Fatalf("module %+v", r.mod.answers)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t1", "", `{"token":"tok-c","answers":["green"]}`); e == nil || e.Code != "question_not_found" {
		t.Fatalf("again %+v", e)
	}
}

// an approval by voice: the setting allows it, the session is woken, the
// question was told on the device before the turn, and the user's own words
// pass the approval gates
func TestSubscriptionReplyApproval(t *testing.T) {
	r := newSubRig(t)
	ctx := context.Background()
	if _, err := r.hub.Subscribe(ctx, subscriptions.Request{Topic: "t3code:work:*", Device: r.dev, CreatedBy: subscriptions.ByAPI}); err != nil {
		t.Fatal(err)
	}
	r.ask("tok-a", subscriptions.QuestionApproval)
	approve := `{"token":"tok-a","decision":"once","userWords":"разрешаю деплой"}`
	said := "Алиса, разрешаю деплой"
	if _, e := r.call("subscription_reply", wokenTrig, "t2", said, approve); e == nil || e.Code != "dashboard_only" {
		t.Fatalf("setting off %+v", e)
	}
	r.values.SubscriptionApprovals = settings.SubscriptionApprovalsVoice
	if _, e := r.call("subscription_reply", cadencev2.Trigger_TRIGGER_FOLLOW_UP, "t2", said, approve); e == nil || e.Code != "wake_required" {
		t.Fatalf("follow-up %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t2", said, approve); e == nil || e.Code != "question_not_told" {
		t.Fatalf("untold %+v", e)
	}
	r.tell(ctx, "t1")
	// told by this very turn is not before it
	if _, e := r.call("subscription_reply", wokenTrig, "t1", said, approve); e == nil || e.Code != "question_not_told" {
		t.Fatalf("told by this turn %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t2", "Алиса, да", `{"token":"tok-a","decision":"once","userWords":"да"}`); e == nil || e.Code != "explicit_decision_required" {
		t.Fatalf("bare yes %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t2", "не разрешаю деплой", `{"token":"tok-a","decision":"once","userWords":"разрешаю деплой"}`); e == nil || e.Code != "explicit_decision_required" {
		t.Fatalf("negated %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t2", said, `{"token":"tok-a","answers":["blue"]}`); e == nil || e.Code != "invalid_arguments" {
		t.Fatalf("answers to an approval %+v", e)
	}
	if _, e := r.call("subscription_reply", wokenTrig, "t2", said, approve); e != nil {
		t.Fatalf("approve %+v", e)
	}
	got := r.mod.answers
	if len(got) != 1 || got[0].Decision != "once" || got[0].By.Via != subscriptions.ViaVoice || got[0].By.Words != "разрешаю деплой" || got[0].By.Device != r.dev {
		t.Fatalf("module %+v", got)
	}
}

// tell has the question reach the inbox (as afterBusy inbox leaves it), a
// woken turn read the unread block, which tells nothing, then inspect it,
// which tells it on the device by turn
func (r *subRig) tell(ctx context.Context, turn string) {
	r.t.Helper()
	if _, err := r.pool.Exec(ctx, `UPDATE subscription_items SET state = 'inbox'`); err != nil {
		r.t.Fatal(err)
	}
	if note, err := r.hub.Inbox(ctx, r.dev, turn); err != nil || !strings.Contains(note, "tok-a") {
		r.t.Fatalf("block %q %v", note, err)
	}
	it, err := r.hub.Question(ctx, r.dev, "tok-a")
	if err != nil {
		r.t.Fatal(err)
	}
	if told, _ := r.hub.ToldBefore(ctx, it, r.dev, turn+"-later"); told {
		r.t.Fatal("the block told the question")
	}
	out, e := r.call("subscription_inspect", wokenTrig, turn, "", `{"token":"tok-a"}`)
	if e != nil || !strings.Contains(string(out), "Deploy the release to production?") {
		r.t.Fatalf("inspect %s %+v", out, e)
	}
}

// inspect reads the outside words, untrusted, by token or topic, never in an
// event turn
func TestSubscriptionInspect(t *testing.T) {
	r := newSubRig(t)
	ctx := context.Background()
	if _, err := r.hub.Subscribe(ctx, subscriptions.Request{Topic: "t3code:work:*", Device: r.dev, CreatedBy: subscriptions.ByAPI}); err != nil {
		t.Fatal(err)
	}
	r.ask("tok-c", subscriptions.QuestionClarify)
	if r.byName["subscription_inspect"].Output != agent.Untrusted || r.byName["subscription_inspect"].Effects != agent.Read {
		t.Fatal("inspect must be an untrusted read")
	}
	if _, err := r.pool.Exec(ctx, `UPDATE subscription_items SET state = 'delivered'`); err != nil {
		t.Fatal(err)
	}
	out, e := r.call("subscription_inspect", wokenTrig, "t1", "", `{"topic":"t3code:work:thread:a"}`)
	if e != nil || !strings.Contains(string(out), `"title":"Deploy"`) || !strings.Contains(string(out), `"label":"Green"`) || !strings.Contains(string(out), `"open":true`) {
		t.Fatalf("by topic %s %+v", out, e)
	}
	if _, e := r.call("subscription_inspect", wokenTrig, "t1", "", `{"token":"nope"}`); e == nil || e.Code != "question_not_found" {
		t.Fatalf("unknown token %+v", e)
	}
	if _, e := r.call("subscription_inspect", wokenTrig, "t1", "", `{}`); e == nil || e.Code != "invalid_arguments" {
		t.Fatalf("neither %+v", e)
	}
	if _, e := r.call("subscription_inspect", cadencev2.Trigger_TRIGGER_EVENT, "t1", "", `{"token":"tok-c"}`); e == nil || e.Code != "not_in_event_turn" {
		t.Fatalf("event turn %+v", e)
	}
}
