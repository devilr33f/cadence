package conversation_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

const metricsBearer = "metrics-token"

// verify is apitest.Verify with a metrics token beside the ha one
func verify(ctx context.Context, bearer string) (auth.Grants, error) {
	if bearer == metricsBearer {
		return auth.Grants{Scopes: auth.Scopes{auth.ScopeMetrics}}, nil
	}
	return apitest.Verify(ctx, bearer)
}

// lockedBuffer is a log the handlers write while a test reads it
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (r *rig) get(bearer, path string) (int, string, http.Header) {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, strings.TrimSpace(b.String()), resp.Header
}

func (r *rig) call(bearer, path string) (int, string) {
	r.t.Helper()
	code, body, _ := r.get(bearer, path)
	return code, body
}

type page struct {
	Turns []map[string]any `json:"turns"`
	Next  *string          `json:"next"`
}

// page reads one page of the device's turns as an admin
func (r *rig) page(query string) page {
	r.t.Helper()
	code, body := r.call("x", "/devices/"+r.dev+"/turns"+query)
	if code != 200 {
		r.t.Fatalf("%s: %d %s", query, code, body)
	}
	var p page
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		r.t.Fatalf("%s: %v %s", query, err, body)
	}
	return p
}

func ids(p page) []string {
	out := []string{}
	for _, t := range p.Turns {
		out = append(out, t["turnId"].(string))
	}
	return out
}

// walk reads every page of limit turns, newest first, and returns the turn
// ids in the order they came and the size of each page
func (r *rig) walk(limit string) (got []string, sizes []int) {
	r.t.Helper()
	q := "?limit=" + limit
	for range 100 {
		p := r.page(q)
		got = append(got, ids(p)...)
		sizes = append(sizes, len(p.Turns))
		if p.Next == nil {
			return got, sizes
		}
		q = "?limit=" + limit + "&before=" + url.QueryEscape(*p.Next)
	}
	r.t.Fatal("the cursor never ended")
	return nil, nil
}

// a device's turns come newest first, a page at a time; the cursor walks every
// turn once, two turns of one instant in turn id order, and stays on its device
func TestTurnsNewestFirstAndPaged(t *testing.T) {
	r := newRig(t)
	day := 24 * time.Hour
	r.turn("t1", r.now.Add(-3*day))
	r.turn("t2", r.now.Add(-2*day))
	r.turn("t3a", r.now.Add(-day))
	r.turn("t3b", r.now.Add(-day))
	r.turn("t4", r.now.Add(-time.Hour))
	r.turn("t5", r.now.Add(-time.Minute))
	other, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "other", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.s.Append(context.Background(), other.ID.String(), "x1", conversation.TurnMeta{}, []providers.Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"t5", "t4", "t3b", "t3a", "t2", "t1"}
	for limit, sizes := range map[string][]int{"1": {1, 1, 1, 1, 1, 1}, "2": {2, 2, 2}, "4": {4, 2}, "6": {6}, "100": {6}} {
		got, n := r.walk(limit)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(n, sizes) {
			t.Fatalf("limit %s: %v in pages %v", limit, got, n)
		}
	}
	// a turn that lands while the owner pages through goes before the first page
	p := r.page("?limit=3")
	r.turn("t6", r.now)
	if got := ids(r.page("?limit=3&before=" + url.QueryEscape(*p.Next))); !reflect.DeepEqual(got, want[3:]) {
		t.Fatalf("after a new turn: %v", got)
	}
	if got := ids(r.page("?limit=1")); !reflect.DeepEqual(got, []string{"t6"}) {
		t.Fatalf("the new turn: %v", got)
	}
	// the id in another spelling is the same device
	if code, b := r.call("x", "/devices/"+strings.ToUpper(r.dev)+"/turns?limit=1"); code != 200 || !strings.Contains(b, `"turnId":"t6"`) {
		t.Fatalf("upper case id: %d %s", code, b)
	}
}

func TestTurnsLimitAndCursor(t *testing.T) {
	r := newRig(t)
	for i := range 21 {
		r.turn(string(rune('a'+i)), r.now.Add(time.Duration(i)*time.Minute))
	}
	if p := r.page(""); len(p.Turns) != 20 || p.Next == nil || p.Turns[0]["turnId"] != "u" {
		t.Fatalf("default: %v next %v", ids(p), p.Next)
	}
	if p := r.page("?limit=100"); len(p.Turns) != 21 || p.Next != nil {
		t.Fatalf("100: %d next %v", len(p.Turns), p.Next)
	}
	cursor := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	dev := "/devices/" + r.dev + "/turns"
	for _, c := range []struct{ query, want string }{
		{"?limit=0", `{"error":"invalid","path":"/query/limit"}`},
		{"?limit=101", `{"error":"invalid","path":"/query/limit"}`},
		{"?limit=-1", `{"error":"invalid","path":"/query/limit"}`},
		{"?before=%21%21", `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("nocolon"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("x:t1"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("1700000000000000:"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("1700000000000000:a\x00b"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("1700000000000000:a\xffb"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("1700000000000000:"+strings.Repeat("a", 400)), `{"error":"invalid_cursor","path":"/query/before"}`},
		// an instant postgres cannot hold, or before 1970
		{"?before=" + cursor("-300000000000000000:A"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("-9223372036854775808:A"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("-1:A"), `{"error":"invalid_cursor","path":"/query/before"}`},
		{"?before=" + cursor("253402300800000000:A"), `{"error":"invalid_cursor","path":"/query/before"}`},
	} {
		if code, b := r.call("x", dev+c.query); code != 422 || b != c.want {
			t.Errorf("%s: %d %s", c.query, code, b)
		}
	}
	for _, path := range []string{"/devices/00000000-0000-0000-0000-000000000009/turns", "/devices/nope/turns"} {
		if code, b := r.call("x", path); code != 404 || b != `{"error":"not_found"}` {
			t.Errorf("%s: %d %s", path, code, b)
		}
	}
	// a device with no turns is an empty page
	other, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "other", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if code, b := r.call("x", "/devices/"+other.ID.String()+"/turns"); code != 200 || b != `{"turns":[]}` {
		t.Fatalf("no turns: %d %s", code, b)
	}
}

// the list shows what a turn heard and said, its tools by name with the code
// their result carries, and none of their arguments or results
func TestTurnsShowWhatATurnSaidAndCalled(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("привет ", 300)
	calls := func(cs ...providers.ToolCall) []providers.ToolCall { return cs }
	call := func(id, name string) providers.ToolCall {
		return providers.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{"secret":"argument-xyz"}`)}
	}
	tool := func(id, content string) providers.Message {
		return providers.Message{Role: "tool", ToolCallID: id, Content: content}
	}
	r.append("rich", conversation.TurnMeta{Trigger: "wake", Mode: "smart", Usage: &providers.Usage{PromptTokens: 270, CompletionTokens: 10}},
		providers.Message{Role: "user", Content: long},
		providers.Message{Role: "assistant", Content: "Let me check.", ToolCalls: calls(call("c1", "weather"), call("c2", "alarm"))},
		tool("c1", `{"ok":true,"data":{"result":"result-xyz"}}`),
		tool("c2", `{"ok":false,"error":{"code":"refused","message":"Wake me first."}}`),
		providers.Message{Role: "assistant", ToolCalls: calls(call("c3", "mcp_x_echo"), call("c4", "web_search"), call("c5", "timer"), call("c6", "timer"), call("c7", "alarm"), call("c8", "mcp_x_long"))},
		tool("c3", `{"code":"timeout"}`),
		tool("c4", `{"error":"rate_limited"}`),
		tool("c5", `sunny`),
		tool("c6", `{"code":5,"error":["x"]}`),
		tool("c8", `{"code":"`+strings.Repeat("x", 70)+`"}`),
		providers.Message{Role: "assistant", Content: "Sunny, 3 degrees."},
	)
	r.backdate("rich", r.now.Add(-time.Hour))
	r.append("bare", conversation.TurnMeta{}, providers.Message{Role: "user", Content: "hi"})
	r.backdate("bare", r.now.Add(-2*time.Hour))
	cut := string([]rune(long)[:2000])
	var want []map[string]any
	if err := json.Unmarshal([]byte(`[
		{"turnId":"rich","at":"`+r.now.Add(-time.Hour).Format(time.RFC3339)+`","trigger":"wake","mode":"smart",
		 "user":`+quote(cut)+`,"reply":"Sunny, 3 degrees.",
		 "tools":[{"name":"weather"},{"name":"alarm","code":"refused"},{"name":"mcp_x_echo","code":"timeout"},
		          {"name":"web_search","code":"rate_limited"},{"name":"timer"},{"name":"timer"},{"name":"alarm"},
		          {"name":"mcp_x_long","code":"`+strings.Repeat("x", 64)+`"}],
		 "tokens":{"prompt":270,"completion":10}},
		{"turnId":"bare","at":"`+r.now.Add(-2*time.Hour).Format(time.RFC3339)+`","user":"hi","tools":[]}
	]`), &want); err != nil {
		t.Fatal(err)
	}
	code, body, header := r.get("x", "/devices/"+r.dev+"/turns")
	var got page
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if !reflect.DeepEqual(got.Turns, want) || got.Next != nil {
		t.Fatalf("got %s", body)
	}
	if strings.Contains(body, "xyz") || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("arguments or results in the list, or cacheable (%q): %s", header.Get("Cache-Control"), body)
	}
	// the detail's tool messages carry the codes the list read, read the same way
	code, body = r.call("x", "/devices/"+r.dev+"/turns/rich")
	var detail struct {
		Messages []struct {
			Role       string  `json:"role"`
			ToolCallID string  `json:"toolCallId"`
			Code       *string `json:"code"`
		} `json:"messages"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &detail) != nil {
		t.Fatalf("detail %d %s", code, body)
	}
	codes := map[string]string{}
	for _, m := range detail.Messages {
		switch {
		case m.Role == "tool" && m.Code != nil:
			codes[m.ToolCallID] = *m.Code
		case m.Code != nil:
			t.Fatalf("a %s message with a code: %s", m.Role, body)
		}
	}
	if want := map[string]string{"c2": "refused", "c3": "timeout", "c4": "rate_limited", "c8": strings.Repeat("x", 64)}; !reflect.DeepEqual(codes, want) {
		t.Fatalf("detail codes %v", codes)
	}
	if logs := r.logs.String(); logs != "" {
		t.Fatalf("a read logged %s", logs)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// the detail is the turn as stored, its tool calls' arguments and results too
func TestTurnDetailAsStored(t *testing.T) {
	r := newRig(t)
	r.append("d1", conversation.TurnMeta{Trigger: "button", Mode: "fast", Usage: &providers.Usage{PromptTokens: 12, CompletionTokens: 3},
		Skills: []string{"weather", "tv"}},
		providers.Message{Role: "user", Content: "weather?"},
		providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "c1", Name: "weather", Arguments: json.RawMessage(`{"days":1,"place":"Бишкек"}`)}, {ID: "c2", Name: "ping"},
		}},
		providers.Message{Role: "tool", ToolCallID: "c1", Content: `{"ok":true,"data":{"t":1}}`},
		providers.Message{Role: "tool", ToolCallID: "c2", Content: `{"ok":true}`},
		providers.Message{Role: "assistant", Content: "Sunny."},
	)
	r.backdate("d1", r.now.Add(-time.Minute))
	at := r.now.Add(-time.Minute).Format(time.RFC3339)
	want := `{"turnId":"d1","at":"` + at + `","trigger":"button","mode":"fast","tokens":{"prompt":12,"completion":3},"skills":["weather","tv"],"messages":[` +
		`{"seq":0,"role":"user","content":"weather?"},` +
		`{"seq":1,"role":"assistant","content":"","toolCalls":[{"id":"c1","name":"weather","arguments":{"days":1,"place":"Бишкек"}},{"id":"c2","name":"ping"}]},` +
		`{"seq":2,"role":"tool","content":"{\"ok\":true,\"data\":{\"t\":1}}","toolCallId":"c1"},` +
		`{"seq":3,"role":"tool","content":"{\"ok\":true}","toolCallId":"c2"},` +
		`{"seq":4,"role":"assistant","content":"Sunny."}]}`
	code, body, header := r.get("x", "/devices/"+r.dev+"/turns/d1")
	if code != 200 || body != want || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q %s", code, header.Get("Cache-Control"), body)
	}
	// a turn of no row of its own: a bare user message, nothing recorded
	r.append("d2", conversation.TurnMeta{}, providers.Message{Role: "user", Content: "hi"})
	r.backdate("d2", r.now)
	if code, body := r.call("x", "/devices/"+r.dev+"/turns/d2"); code != 200 ||
		body != `{"turnId":"d2","at":"`+r.now.Format(time.RFC3339)+`","messages":[{"seq":0,"role":"user","content":"hi"}]}` {
		t.Fatalf("bare: %d %s", code, body)
	}
	other, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "other", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/devices/" + r.dev + "/turns/nope", "/devices/" + other.ID.String() + "/turns/d1",
		"/devices/00000000-0000-0000-0000-000000000009/turns/d1", "/devices/nope/turns/d1", "/devices/" + r.dev + "/turns/d%00",
	} {
		if code, b := r.call("x", path); code != 404 || b != `{"error":"not_found"}` {
			t.Errorf("%s: %d %s", path, code, b)
		}
	}
	if logs := r.logs.String(); logs != "" {
		t.Fatalf("a read logged %s", logs)
	}
}

// a turn stored before the turn table came is read from its messages: its
// start, and no trigger, mode or tokens
func TestTurnsBackfilled(t *testing.T) {
	r := newRig(t)
	at := r.now.Add(-2 * time.Hour)
	r.exec(`INSERT INTO conversation_messages (device_id, turn_id, seq, role, content, created_at) VALUES
		($1::uuid, 'legacy', 0, 'user', '{"role":"user","content":"old question"}', $2),
		($1::uuid, 'legacy', 1, 'assistant', '{"role":"assistant","content":"old answer"}', $2::timestamptz + interval '1 second')`, r.dev, at)
	raw, err := os.ReadFile("../store/migrations/00028_conversation_turns.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(raw), "-- +goose Up", 2)[1], "-- +goose Down")
	r.exec(down)
	r.exec(up)
	// the current schema also has 00035's skills and 00037's failure columns
	r.exec(`ALTER TABLE conversation_turns ADD COLUMN skills text[], ADD COLUMN failure jsonb`)
	want := `{"turns":[{"turnId":"legacy","at":"` + at.Format(time.RFC3339) + `","user":"old question","reply":"old answer","tools":[]}]}`
	if code, b := r.call("x", "/devices/"+r.dev+"/turns"); code != 200 || b != want {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := r.call("x", "/devices/"+r.dev+"/turns/legacy"); code != 200 || !strings.HasPrefix(b, `{"turnId":"legacy","at":"`+at.Format(time.RFC3339)+`","messages":[`) {
		t.Fatalf("detail %d %s", code, b)
	}
}

// a device's delete takes its turns with their messages
func TestDeviceDeleteTakesItsTurns(t *testing.T) {
	r := newRig(t)
	r.turn("t1", r.now)
	r.exec(`DELETE FROM devices WHERE id = $1::uuid`, r.dev)
	if r.count() != 0 || r.rows("conversation_turns") != 0 {
		t.Fatalf("count %d turns %d", r.count(), r.rows("conversation_turns"))
	}
}

// the history is the dashboard's alone (rule 8): ha and metrics tokens reach
// neither route
func TestTurnsNeedAdmin(t *testing.T) {
	r := newRig(t)
	r.turn("t1", r.now)
	for _, bearer := range []string{apitest.HABearer, metricsBearer} {
		for _, path := range []string{"/devices/" + r.dev + "/turns", "/devices/" + r.dev + "/turns/t1"} {
			if code, b := r.call(bearer, path); code != 403 || b != `{"error":"forbidden"}` {
				t.Errorf("%s %s: %d %s", bearer, path, code, b)
			}
		}
	}
}

// a failed read answers a code and logs its operation alone
func TestTurnsReadFailure(t *testing.T) {
	r := newRig(t)
	r.turn("secret-turn", r.now)
	r.exec(`ALTER TABLE conversation_messages RENAME TO gone`)
	for path, op := range map[string]string{"/turns": "list", "/turns/secret-turn": "get"} {
		if code, b := r.call("x", "/devices/"+r.dev+path); code != 500 || b != `{"error":"internal"}` {
			t.Fatalf("%s: %d %s", path, code, b)
		}
		if logs := r.logs.String(); !strings.Contains(logs, "msg=conversation_read_failed op="+op) || strings.Contains(logs, "secret") ||
			strings.Contains(logs, "gone") {
			t.Fatalf("%s logs %s", path, logs)
		}
	}
}

// countingDB counts the bytes of the rows its queries return.
type countingDB struct {
	db.DBTX
	bytes atomic.Int64
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := c.DBTX.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &countingRows{Rows: rows, n: &c.bytes}, nil
}

type countingRows struct {
	pgx.Rows
	n *atomic.Int64
}

func (r *countingRows) Next() bool {
	if !r.Rows.Next() {
		return false
	}
	for _, v := range r.RawValues() {
		r.n.Add(int64(len(v)))
	}
	return true
}

// the list reads of each message only what it shows: a turn holding a 1 MB
// tool result lists in a few hundred bytes, which its detail does not
func TestTurnsListReadsNoMessageWhole(t *testing.T) {
	r := newRig(t)
	huge := `{"ok":true,"data":"` + strings.Repeat("x", 1<<20) + `"}`
	// an error envelope past 4 KiB carries no code to the list
	bigError := `{"ok":false,"error":{"code":"too_big","message":"` + strings.Repeat("m", 5000) + `"}}`
	r.append("big", conversation.TurnMeta{Trigger: "wake"},
		providers.Message{Role: "user", Content: "read it"},
		providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "c1", Name: "mcp_x_read", Arguments: json.RawMessage(`{"q":"` + strings.Repeat("a", 1<<16) + `"}`)},
			{ID: "c2", Name: "mcp_x_fail"},
		}},
		providers.Message{Role: "tool", ToolCallID: "c1", Content: huge},
		providers.Message{Role: "tool", ToolCallID: "c2", Content: bigError},
		providers.Message{Role: "assistant", Content: "Done."},
	)
	counter := &countingDB{DBTX: r.pool}
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: verify,
		Admin: []func(huma.API){func(a huma.API) { conversation.RegisterAdmin(a, conversation.New(db.New(counter)), nil, nil) }},
	})
	get := func(path string) string {
		t.Helper()
		code, body := apitest.Call(t, app, "x", http.MethodGet, "/api/v1/devices/"+r.dev+path, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		return body
	}
	body := get("/turns")
	if n := counter.bytes.Load(); n > 4096 {
		t.Fatalf("the list read %d bytes", n)
	}
	var p page
	if err := json.Unmarshal([]byte(body), &p); err != nil || len(p.Turns) != 1 ||
		!reflect.DeepEqual(p.Turns[0]["tools"], []any{map[string]any{"name": "mcp_x_read"}, map[string]any{"name": "mcp_x_fail"}}) ||
		p.Turns[0]["user"] != "read it" || p.Turns[0]["reply"] != "Done." {
		t.Fatalf("%v %s", err, body)
	}
	counter.bytes.Store(0)
	detail := get("/turns/big")
	if !strings.Contains(detail, strings.Repeat("x", 1<<20)) || counter.bytes.Load() < 1<<20 {
		t.Fatalf("the detail read %d bytes", counter.bytes.Load())
	}
	// nor to the detail, which reads codes as the list does
	if strings.Contains(detail, `"code":"too_big"`) {
		t.Fatalf("a code past 4 KiB in the detail")
	}
}
