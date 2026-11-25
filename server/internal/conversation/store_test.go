package conversation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	s       *conversation.Store
	app     *fiber.App
	logs    *lockedBuffer
	dev     string
	now     time.Time
	compact *compactSpy
	resets  *conversation.Resetter
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	q := db.New(pool)
	d, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, pool: pool, s: conversation.New(q), logs: &lockedBuffer{}, dev: d.ID.String(), now: time.Now().UTC().Truncate(time.Second), compact: &compactSpy{}}
	r.resets = conversation.NewResetter(r.s, r.compact)
	log := slog.New(slog.NewTextHandler(r.logs, nil))
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: verify,
		Admin: []func(huma.API){func(a huma.API) { conversation.RegisterAdmin(a, r.s, r.resets, log) }},
	})
	return r
}

// turn appends a user+assistant pair, then backdates it to start at `at`.
func (r *rig) turn(id string, at time.Time) {
	r.t.Helper()
	msgs := []providers.Message{{Role: "user", Content: id + " u"}, {Role: "assistant", Content: id + " a"}}
	r.append(id, conversation.TurnMeta{}, msgs...)
	r.backdate(id, at)
}

func (r *rig) append(id string, meta conversation.TurnMeta, msgs ...providers.Message) {
	r.t.Helper()
	if err := r.s.Append(context.Background(), r.dev, id, meta, msgs); err != nil {
		r.t.Fatal(err)
	}
}

// backdate moves a turn to start at `at`, its messages a second apart.
func (r *rig) backdate(id string, at time.Time) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		`UPDATE conversation_messages SET created_at = $1::timestamptz + seq * interval '1 second' WHERE turn_id = $2`, at, id); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.pool.Exec(context.Background(), `UPDATE conversation_turns SET created_at = $1 WHERE turn_id = $2`, at, id); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) count() int {
	r.t.Helper()
	return r.rows("conversation_messages")
}

func (r *rig) rows(table string) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func contents(ms []providers.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Content)
	}
	return out
}

// lastID is the id of a turn's last message.
func (r *rig) lastID(turn string) int64 {
	r.t.Helper()
	var id int64
	if err := r.pool.QueryRow(context.Background(), `SELECT max(id) FROM conversation_messages WHERE turn_id = $1`, turn).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// compaction replaced the idle cut (M5a-D4): history is the raw turns after
// the summary's last message, however old, at most maxTurns of them
func TestHistoryAfterCompaction(t *testing.T) {
	r := newRig(t)
	r.turn("t1", r.now.Add(-48*time.Hour))
	r.turn("t2", r.now.Add(-24*time.Hour))
	r.turn("t3", r.now.Add(-5*time.Minute))
	ctx := context.Background()
	got, err := r.s.History(ctx, r.dev, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"t1 u", "t1 a", "t2 u", "t2 a", "t3 u", "t3 a"}; !reflect.DeepEqual(contents(got), want) {
		t.Fatalf("no compaction: got %v want %v", contents(got), want)
	}
	got, err = r.s.History(ctx, r.dev, r.lastID("t1"), 6)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"t2 u", "t2 a", "t3 u", "t3 a"}; !reflect.DeepEqual(contents(got), want) {
		t.Fatalf("after t1: got %v want %v", contents(got), want)
	}
	if got, err = r.s.History(ctx, r.dev, r.lastID("t3"), 6); err != nil || len(got) != 0 {
		t.Fatalf("after t3: got %v %v", got, err)
	}
}

func TestHistoryMaxTurns(t *testing.T) {
	r := newRig(t)
	for i, id := range []string{"t1", "t2", "t3"} {
		r.turn(id, r.now.Add(-time.Duration(3-i)*time.Minute))
	}
	got, err := r.s.History(context.Background(), r.dev, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"t2 u", "t2 a", "t3 u", "t3 a"}; !reflect.DeepEqual(contents(got), want) {
		t.Fatalf("got %v want %v", contents(got), want)
	}
	if got, err := r.s.History(context.Background(), r.dev, 0, 0); err != nil || len(got) != 0 {
		t.Fatalf("no turns: got %v %v", got, err)
	}
}

// the compaction segment: every message after an id, with its id, oldest
// first; past the limit the newest ones
func TestAfter(t *testing.T) {
	r := newRig(t)
	in := []providers.Message{
		{Role: "user", Content: "weather"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "c1", Name: "weather", Arguments: json.RawMessage(`{"days":1}`)}}},
		{Role: "tool", ToolCallID: "c1", Content: `{"ok":true}`},
		{Role: "assistant", Content: "Sunny."},
	}
	ctx := context.Background()
	r.turn("t1", r.now.Add(-time.Hour))
	if err := r.s.Append(ctx, r.dev, "t2", conversation.TurnMeta{Trigger: conversation.TriggerEvent}, in); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Minute)
	got, err := r.s.After(ctx, r.dev, r.lastID("t1"), soon, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Content != "weather" || got[1].ToolCalls[0].Name != "weather" || got[2].ToolCallID != "c1" ||
		got[3].ID != r.lastID("t2") {
		t.Fatalf("got %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Fatalf("ids out of order: %+v", got)
		}
	}
	// each message carries its turn's trigger, none for a turn that recorded none
	for _, m := range got {
		if m.Trigger != "event" {
			t.Fatalf("trigger %+v", m)
		}
	}
	if all, err := r.s.After(ctx, r.dev, 0, soon, 100); err != nil || len(all) != 6 || all[0].Trigger != "" || all[1].Trigger != "" {
		t.Fatalf("no trigger: %+v %v", all, err)
	}
	got, err = r.s.After(ctx, r.dev, 0, soon, 3)
	if err != nil || len(got) != 3 || len(got[0].ToolCalls) != 1 || got[1].ToolCallID != "c1" || got[2].Content != "Sunny." {
		t.Fatalf("limited: %+v %v", got, err)
	}
	if got, err := r.s.After(ctx, r.dev, r.lastID("t2"), soon, 100); err != nil || len(got) != 0 {
		t.Fatalf("nothing after the newest: %+v %v", got, err)
	}
	// only messages created before the given time: t2 is newer than that
	if got, err := r.s.After(ctx, r.dev, 0, r.now.Add(-time.Minute), 100); err != nil || len(got) != 2 || got[1].ID != r.lastID("t1") {
		t.Fatalf("before: %+v %v", got, err)
	}
}

func TestHistoryRoundTripsToolMessages(t *testing.T) {
	r := newRig(t)
	in := []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "c1", Name: "weather", Arguments: json.RawMessage(`{"days":1}`)}}},
		{Role: "tool", ToolCallID: "c1", Content: "sunny"},
		{Role: "assistant", Content: "sunny"},
	}
	if err := r.s.Append(context.Background(), r.dev, "t1", conversation.TurnMeta{}, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.History(context.Background(), r.dev, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || !jsonEqual(got[1].ToolCalls[0].Arguments, `{"days":1}`) || got[2].ToolCallID != "c1" {
		t.Fatalf("got %+v", got)
	}
}

// retention takes a turn's row with its messages
func TestPrune(t *testing.T) {
	r := newRig(t)
	r.turn("old", r.now.Add(-15*24*time.Hour))
	r.turn("new", r.now.Add(-time.Hour))
	n, err := r.s.Prune(context.Background(), r.now.Add(-14*24*time.Hour))
	if err != nil || n != (conversation.Pruned{Messages: 2, Turns: 1}) {
		t.Fatalf("n=%+v err=%v", n, err)
	}
	if r.count() != 2 || r.rows("conversation_turns") != 1 {
		t.Fatalf("count %d turns %d", r.count(), r.rows("conversation_turns"))
	}
	if code, _ := r.call("x", "/devices/"+r.dev+"/turns/old"); code != 404 {
		t.Fatalf("pruned turn: %d", code)
	}
	if code, b := r.call("x", "/devices/"+r.dev+"/turns"); code != 200 || !strings.Contains(b, `"turnId":"new"`) || strings.Contains(b, `"old"`) {
		t.Fatalf("list after the prune: %d %s", code, b)
	}
}

func jsonEqual(a json.RawMessage, b string) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

func TestAppendAtomic(t *testing.T) {
	r := newRig(t)
	// row 1 is valid jsonb, row 2 is rejected by the database
	err := r.s.AppendRaw(context.Background(), r.dev, "t1", [][]byte{[]byte(`{"role":"user","content":"x"}`), []byte(`{"content":"\u0000"}`)})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "x") && strings.Contains(err.Error(), "content") {
		t.Fatalf("error carries content: %v", err)
	}
	if r.count() != 0 || r.rows("conversation_turns") != 0 {
		t.Fatalf("count %d turns %d", r.count(), r.rows("conversation_turns"))
	}
	ok := []providers.Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "y"}}
	if err := r.s.Append(context.Background(), "00000000-0000-0000-0000-000000000000", "t2", conversation.TurnMeta{}, ok); err == nil {
		t.Fatal("want fk error")
	}
	if r.count() != 0 || r.rows("conversation_turns") != 0 {
		t.Fatalf("count %d turns %d", r.count(), r.rows("conversation_turns"))
	}
	// the turn's row refused: none of its messages lands either
	r.exec(`CREATE FUNCTION refuse_turn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused-xyz'; END $$`)
	r.exec(`CREATE TRIGGER refuse_turn BEFORE INSERT ON conversation_turns FOR EACH ROW EXECUTE FUNCTION refuse_turn()`)
	err = r.s.Append(context.Background(), r.dev, "t3", conversation.TurnMeta{Trigger: "wake", Mode: "fast"}, ok)
	if err == nil || strings.Contains(err.Error(), "refused-xyz") {
		t.Fatalf("refused turn: %v", err)
	}
	if r.count() != 0 || r.rows("conversation_turns") != 0 {
		t.Fatalf("count %d turns %d", r.count(), r.rows("conversation_turns"))
	}
}

func (r *rig) exec(sql string, args ...any) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(), sql, args...); err != nil {
		r.t.Fatal(err)
	}
}

func TestAppendReplacesNul(t *testing.T) {
	r := newRig(t)
	in := []providers.Message{
		{Role: "user", Content: "a\x00b"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "c1", Name: "n", Arguments: json.RawMessage(`{"q":"x\u0000y"}`)}}},
	}
	if err := r.s.Append(context.Background(), r.dev, "t1", conversation.TurnMeta{}, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.s.History(context.Background(), r.dev, 0, 6)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	if got[0].Content != "a\uFFFDb" || !jsonEqual(got[1].ToolCalls[0].Arguments, `{"q":"x`+"\uFFFD"+`y"}`) {
		t.Fatalf("got %+v", got)
	}
}

func (r *rig) seedOld(turn string) {
	r.t.Helper()
	r.turn(turn, r.now.Add(-15*24*time.Hour))
}

func waitCount(t *testing.T, r *rig, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for r.count() != want {
		if time.Now().After(deadline) {
			t.Fatalf("count %d want %d", r.count(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRetentionPrunesAtStartAndOnTick(t *testing.T) {
	r := newRig(t)
	r.seedOld("old1")
	var buf bytes.Buffer
	stop := r.s.RunRetention(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), 50*time.Millisecond)
	waitCount(t, r, 0) // startup prune
	r.seedOld("old2")
	waitCount(t, r, 0) // tick prune
	stop()
	logs := buf.String()
	if !strings.Contains(logs, "conversation_pruned") || !strings.Contains(logs, "count=2 turns=1") || strings.Contains(logs, "old") {
		t.Fatalf("logs %q", logs)
	}
}

func numbers(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range m {
		if n, ok := v.(json.Number); ok {
			out[k] = n.String()
		}
	}
	return out
}

func TestArgumentNumbersSurvive(t *testing.T) {
	r := newRig(t)
	// in order: the nul turn is read back second
	for _, c := range []struct{ turn, args string }{
		{"plain", `{"id":9007199254740993,"x":1.0}`},
		{"nul", `{"id":9007199254740993,"x":1.0,"s":"a\u0000b"}`},
	} {
		in := []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(c.args)}}}}
		if err := r.s.Append(context.Background(), r.dev, c.turn, conversation.TurnMeta{}, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.s.History(context.Background(), r.dev, 0, 6)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	for i, m := range got {
		// jsonb may reorder keys and add spaces; numeric text must be unchanged
		n := numbers(t, m.ToolCalls[0].Arguments)
		if n["id"] != "9007199254740993" || n["x"] != "1.0" {
			t.Fatalf("message %d numbers %v", i, n)
		}
	}
	if !strings.Contains(string(got[1].ToolCalls[0].Arguments), "a\uFFFDb") {
		t.Fatalf("got %s", got[1].ToolCalls[0].Arguments)
	}
}

func TestRetentionStartupPruneOnly(t *testing.T) {
	r := newRig(t)
	r.seedOld("old")
	stop := r.s.RunRetention(context.Background(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), time.Hour)
	defer stop()
	waitCount(t, r, 0)
}
