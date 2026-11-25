package modes_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	fastView  = `{"name":"fast","model":"deepseek/deepseek-v4.1-flash-1017","reasoning":"none","maxSteps":8,"maxSeconds":20,"interim":"drop","tools":null,"aliases":{"en":["fast","quick"],"ru":["быстрый"]}}`
	smartView = `{"name":"smart","model":"","reasoning":"medium","maxSteps":25,"maxSeconds":90,"interim":"speak","tools":null,"aliases":{"en":["smart","clever"],"ru":["умный"]}}`
)

func TestListModes(t *testing.T) {
	r := newRig(t)
	if b := r.must(200, "GET", "/modes", ""); b != "["+fastView+","+smartView+"]" {
		t.Fatalf("seeded: %s", b)
	}
	r.must(200, "PUT", "/modes/zeta", turbo)
	r.must(200, "PUT", "/modes/alpha", turbo)
	r.must(200, "PUT", "/modes/fast-b", turbo)
	b := r.must(200, "GET", "/modes", "")
	var order []string
	for _, part := range strings.Split(b, `"name":"`)[1:] {
		order = append(order, part[:strings.IndexByte(part, '"')])
	}
	if want := []string{"alpha", "fast", "fast-b", "smart", "zeta"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
}

func TestGetMode(t *testing.T) {
	r := newRig(t)
	if b := r.must(200, "GET", "/modes/fast", ""); b != fastView {
		t.Fatalf("%s", b)
	}
	// a name no put could take is only unknown
	for _, name := range []string{"turbo", "Fast", "%20"} {
		if code, b := r.call("GET", "/modes/"+name, ""); code != 404 || b != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %s", name, code, b)
		}
	}
}

func TestPutModeCreatesThenReplaces(t *testing.T) {
	r := newRig(t)
	want := `{"name":"turbo","model":"some/model","reasoning":"low","maxSteps":3,"maxSeconds":30,"interim":"speak","tools":null,"aliases":{}}`
	if b := r.must(200, "PUT", "/modes/turbo", turbo); b != want {
		t.Fatalf("create: %s", b)
	}
	if b := r.must(200, "GET", "/modes/turbo", ""); b != want {
		t.Fatalf("get: %s", b)
	}
	got := r.get("turbo")
	if !reflect.DeepEqual(got, agent.Mode{
		Name: "turbo", Model: "some/model", Reasoning: "low", MaxSteps: 3, MaxDuration: 30 * time.Second,
		Interim: agent.InterimSpeak, Aliases: map[string][]string{},
	}) {
		t.Fatalf("runtime %+v", got)
	}
	body := `{"model":"","reasoning":"high","maxSteps":50,"maxSeconds":600,"interim":"drop","tools":["weather","agent_mode"],"aliases":{"en":["  turbo  ","Very Fast"],"ru":["турбо"]}}`
	want = `{"name":"turbo","model":"","reasoning":"high","maxSteps":50,"maxSeconds":600,"interim":"drop","tools":["weather","agent_mode"],"aliases":{"en":["turbo","Very Fast"],"ru":["турбо"]}}`
	if b := r.must(200, "PUT", "/modes/turbo", body); b != want {
		t.Fatalf("replace: %s", b)
	}
	got = r.get("turbo")
	if got.MaxSteps != 50 || got.MaxDuration != 600*time.Second || got.Reasoning != "high" || got.Model != "" ||
		got.Interim != agent.InterimDrop || !reflect.DeepEqual(got.Tools, []string{"weather", "agent_mode"}) ||
		!reflect.DeepEqual(got.Aliases, map[string][]string{"en": {"turbo", "Very Fast"}, "ru": {"турбо"}}) {
		t.Fatalf("runtime after replace %+v", got)
	}
	// a seeded mode takes new budgets the same way, for the next turn's Get
	r.must(200, "PUT", "/modes/fast", `{"model":"m","reasoning":"none","maxSteps":2,"maxSeconds":10,"interim":"drop","aliases":{"en":["quick"]}}`)
	if f := r.get("fast"); f.MaxSteps != 2 || f.MaxDuration != 10*time.Second || f.Model != "m" {
		t.Fatalf("fast %+v", f)
	}
	// tools: an empty list offers none, null or absent every tool
	r.must(200, "PUT", "/modes/turbo", strings.Replace(turbo, `}`, `,"tools":[]}`, 1))
	if tools := r.get("turbo").Tools; tools == nil || len(tools) != 0 {
		t.Fatalf("empty list stored as %#v", tools)
	}
	r.must(200, "PUT", "/modes/turbo", strings.Replace(turbo, `}`, `,"tools":null}`, 1))
	if tools := r.get("turbo").Tools; tools != nil {
		t.Fatalf("null stored as %#v", tools)
	}
	logs := r.logs.String()
	for _, line := range []string{"msg=mode_saved name=turbo created=true", "msg=mode_saved name=turbo created=false", "msg=mode_saved name=fast created=false"} {
		if !strings.Contains(logs, line) {
			t.Fatalf("no %q in\n%s", line, logs)
		}
	}
	if strings.Contains(logs, "some/model") || strings.Contains(logs, "Very Fast") || strings.Contains(logs, "турбо") {
		t.Fatalf("model or alias text logged:\n%s", logs)
	}
	if n := r.changes.Load(); n != 5 {
		t.Fatalf("%d changes heard, want 5", n)
	}
}

// a mode's own ceiling on the model's max_tokens: stored and answered back,
// and replaced as a whole, so a put that leaves it out returns to the default
func TestPutModeMaxTokens(t *testing.T) {
	r := newRig(t)
	capped := strings.Replace(turbo, `}`, `,"maxTokens":2048}`, 1)
	want := `{"name":"turbo","model":"some/model","reasoning":"low","maxSteps":3,"maxSeconds":30,"maxTokens":2048,"interim":"speak","tools":null,"aliases":{}}`
	if b := r.must(200, "PUT", "/modes/turbo", capped); b != want {
		t.Fatalf("create: %s", b)
	}
	if b := r.must(200, "GET", "/modes/turbo", ""); b != want {
		t.Fatalf("get: %s", b)
	}
	if got := r.get("turbo"); got.MaxTokens != 2048 {
		t.Fatalf("runtime %+v", got)
	}
	r.must(200, "PUT", "/modes/turbo", turbo)
	if got := r.get("turbo"); got.MaxTokens != 0 {
		t.Fatalf("runtime after a replace without it %+v", got)
	}
	// the seeded modes start at the default, left out of the answer
	if b := r.must(200, "GET", "/modes/fast", ""); b != fastView {
		t.Fatalf("seeded fast: %s", b)
	}
	if got := r.get("fast"); got.MaxTokens != 0 {
		t.Fatalf("seeded fast %+v", got)
	}
}

// a mode's first-token timeout: stored and answered back, 0 or left out keeps
// the built-in wait
func TestPutModeFirstTokenTimeout(t *testing.T) {
	r := newRig(t)
	slow := strings.Replace(turbo, `}`, `,"firstTokenTimeoutMs":20000}`, 1)
	want := `{"name":"turbo","model":"some/model","reasoning":"low","maxSteps":3,"maxSeconds":30,"firstTokenTimeoutMs":20000,"interim":"speak","tools":null,"aliases":{}}`
	if b := r.must(200, "PUT", "/modes/turbo", slow); b != want {
		t.Fatalf("create: %s", b)
	}
	if b := r.must(200, "GET", "/modes/turbo", ""); b != want {
		t.Fatalf("get: %s", b)
	}
	if got := r.get("turbo"); got.FirstTokenTimeout != 20*time.Second {
		t.Fatalf("runtime %+v", got)
	}
	r.must(200, "PUT", "/modes/turbo", strings.Replace(turbo, `}`, `,"firstTokenTimeoutMs":0}`, 1))
	if got := r.get("turbo"); got.FirstTokenTimeout != 0 {
		t.Fatalf("runtime after 0 %+v", got)
	}
}

func TestPutModeRefusals(t *testing.T) {
	r := newRig(t)
	with := func(extra string) string { return strings.Replace(turbo, `}`, ","+extra+"}", 1) }
	set := func(field, value string) string {
		i := strings.Index(turbo, `"`+field+`":`)
		if i < 0 {
			return with(`"` + field + `":` + value)
		}
		end := i + strings.IndexAny(turbo[i:], ",}")
		return turbo[:i] + `"` + field + `":` + value + turbo[end:]
	}
	invalid := func(path string) string { return `{"error":"invalid","path":"` + path + `"}` }
	// a json escape of the code point hex, so the source holds no invisible character
	esc := func(hex string) string { return `\` + "u" + hex }
	names := func(n int, f func(i int) string) string {
		var parts []string
		for i := range n {
			parts = append(parts, `"`+f(i)+`"`)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	for _, c := range []struct {
		name, body string
		code       int
		want       string
	}{
		// the name in the path
		{"Turbo", turbo, 422, invalid("/path/name")},
		{"1turbo", turbo, 422, invalid("/path/name")},
		{"_turbo", turbo, 422, invalid("/path/name")},
		{"tur.bo", turbo, 422, invalid("/path/name")},
		{"a" + strings.Repeat("b", 32), turbo, 422, invalid("/path/name")},
		{"a" + strings.Repeat("b", 31), turbo, 200, ""},
		{"a", turbo, 200, ""},
		{"a_b-9", turbo, 200, ""},
		// the model: 200 bytes, no control or format character
		{"m1", set("model", `"`+strings.Repeat("x", 200)+`"`), 200, ""},
		{"m2", set("model", `"`+strings.Repeat("ё", 100)+`"`), 200, ""},
		{"m3", set("model", `"`+strings.Repeat("x", 201)+`"`), 422, invalid("/body/model")},
		{"m4", set("model", `"`+strings.Repeat("ё", 100)+`x"`), 422, invalid("/body/model")},
		{"m5", set("model", `"a\u0000b"`), 422, invalid("/body/model")},
		{"m6", set("model", `"a\nb"`), 422, invalid("/body/model")},
		{"m7", set("model", `"a`+esc("200b")+`b"`), 422, invalid("/body/model")},
		{"m8", set("model", `""`), 200, ""},
		// the enums and the budgets
		{"e1", set("reasoning", `"max"`), 422, invalid("/body/reasoning")},
		{"e2", set("interim", `"loud"`), 422, invalid("/body/interim")},
		{"e3", set("maxSteps", `0`), 422, invalid("/body/maxSteps")},
		{"e4", set("maxSteps", `1`), 200, ""},
		{"e5", set("maxSteps", `50`), 200, ""},
		{"e6", set("maxSteps", `51`), 422, invalid("/body/maxSteps")},
		{"e7", set("maxSeconds", `4`), 422, invalid("/body/maxSeconds")},
		{"e8", set("maxSeconds", `5`), 200, ""},
		{"e9", set("maxSeconds", `600`), 200, ""},
		{"e10", set("maxSeconds", `601`), 422, invalid("/body/maxSeconds")},
		// the token ceiling: left out or null keeps the built-in default
		{"k1", set("maxTokens", `63`), 422, invalid("/body/maxTokens")},
		{"k2", set("maxTokens", `64`), 200, ""},
		{"k3", set("maxTokens", `32768`), 200, ""},
		{"k4", set("maxTokens", `32769`), 422, invalid("/body/maxTokens")},
		{"k5", set("maxTokens", `null`), 200, ""},
		// the first-token timeout: 0 or null keeps the built-in 10 s, else 2–120 s
		{"f1", set("firstTokenTimeoutMs", `0`), 200, ""},
		{"f2", set("firstTokenTimeoutMs", `1999`), 422, invalid("/body/firstTokenTimeoutMs")},
		{"f3", set("firstTokenTimeoutMs", `2000`), 200, ""},
		{"f4", set("firstTokenTimeoutMs", `120000`), 200, ""},
		{"f5", set("firstTokenTimeoutMs", `120001`), 422, invalid("/body/firstTokenTimeoutMs")},
		{"f6", set("firstTokenTimeoutMs", `-1`), 422, invalid("/body/firstTokenTimeoutMs")},
		{"f7", set("firstTokenTimeoutMs", `null`), 200, ""},
		{"e11", `{"model":"","maxSteps":3,"maxSeconds":30,"interim":"speak"}`, 422, invalid("/body")},
		{"e12", with(`"name":"turbo"`), 422, invalid("/body/name")},
		// tools: null, or up to 64 distinct names
		{"t1", with(`"tools":` + names(64, func(i int) string { return fmt.Sprintf("tool_%d", i) })), 200, ""},
		{"t2", with(`"tools":` + names(65, func(i int) string { return fmt.Sprintf("tool_%d", i) })), 422, invalid("/body/tools")},
		{"t3", with(`"tools":["weather","bad name"]`), 422, invalid("/body/tools/1")},
		{"t4", with(`"tools":[""]`), 422, invalid("/body/tools/0")},
		{"t5", with(`"tools":["` + strings.Repeat("a", 64) + `"]`), 200, ""},
		{"t6", with(`"tools":["` + strings.Repeat("a", 65) + `"]`), 422, invalid("/body/tools/0")},
		{"t7", with(`"tools":["weather","weather"]`), 422, invalid("/body/tools")},
		{"t8", with(`"tools":["Some-MCP_tool"]`), 200, ""},
		// aliases: en and ru, up to 10 distinct phrases each of 1–40 characters once trimmed
		{"a1", with(`"aliases":{"de":["schnell"]}`), 422, invalid("/body/aliases/de")},
		{"a2", with(`"aliases":{"en":[]}`), 200, ""},
		{"a3", with(`"aliases":{"en":` + names(10, func(i int) string { return fmt.Sprintf("w%d", i) }) + `}`), 200, ""},
		{"a4", with(`"aliases":{"en":` + names(11, func(i int) string { return fmt.Sprintf("b%d", i) }) + `}`), 422, invalid("/body/aliases/en")},
		{"a5", with(`"aliases":{"en":["ok","   "]}`), 422, invalid("/body/aliases/en/1")},
		{"a6", with(`"aliases":{"ru":[""]}`), 422, invalid("/body/aliases/ru/0")},
		{"a7", with(`"aliases":{"ru":["` + strings.Repeat("я", 40) + `"]}`), 200, ""},
		{"a8", with(`"aliases":{"ru":["` + strings.Repeat("ю", 41) + `"]}`), 422, invalid("/body/aliases/ru/0")},
		// 1–40 once trimmed, as sent longer
		{"a12", with(`"aliases":{"ru":["  ` + strings.Repeat("ж", 40) + ` "]}`), 200, ""},
		{"a9", with(`"aliases":{"en":["Rapid","rapid "]}`), 422, invalid("/body/aliases/en/1")},
		{"a10", with(`"aliases":{"en":["a\u0007b"]}`), 422, invalid("/body/aliases/en/0")},
		{"a11", with(`"aliases":{"en":["a` + esc("2028") + `b"]}`), 422, invalid("/body/aliases/en/0")},
		// another mode's alias in any language, or its name, in any case: the
		// agent_mode tool matches every language's aliases and the names
		{"c1", with(`"aliases":{"en":["swift","QUICK"]}`), 409, `{"error":"alias_taken","path":"/body/aliases/en/1"}`},
		{"c2", with(`"aliases":{"ru":[" Умный "]}`), 409, `{"error":"alias_taken","path":"/body/aliases/ru/0"}`},
		{"c3", with(`"aliases":{"ru":["quick"],"en":["быстрый"]}`), 409, `{"error":"alias_taken","path":"/body/aliases/en/0"}`},
		{"c4", with(`"aliases":{"ru":["ok","Clever"]}`), 409, `{"error":"alias_taken","path":"/body/aliases/ru/1"}`},
		{"c5", with(`"aliases":{"en":["swift"," A "]}`), 409, `{"error":"alias_taken","path":"/body/aliases/en/1"}`},
		{"c6", with(`"aliases":{"en":["c6"],"ru":["c6"]}`), 200, ""},
		// a new mode named as another mode's alias
		{"quick", turbo, 409, `{"error":"alias_taken","path":"/path/name"}`},
		{"clever", turbo, 409, `{"error":"alias_taken","path":"/path/name"}`},
	} {
		code, b := r.call(http.MethodPut, "/modes/"+c.name, c.body)
		if code != c.code || (c.want != "" && b != c.want) {
			t.Errorf("%s %s: %d %s, want %d %s", c.name, c.body, code, b, c.code, c.want)
		}
		if code != 200 {
			if _, err := r.modes.Get(context.Background(), c.name); !errors.Is(err, agent.ErrModeNotFound) {
				t.Errorf("%s: refused yet stored (%v)", c.name, err)
			}
		}
	}
	// a mode keeps its own aliases on a replace, and a clash leaves it as it was
	r.must(200, "PUT", "/modes/fast", `{"model":"","reasoning":"none","maxSteps":8,"maxSeconds":20,"interim":"drop","aliases":{"en":["Quick","fast"]}}`)
	if code, b := r.call("PUT", "/modes/fast", `{"model":"x","reasoning":"none","maxSteps":8,"maxSeconds":20,"interim":"drop","aliases":{"en":["clever"]}}`); code != 409 || b != `{"error":"alias_taken","path":"/body/aliases/en/0"}` {
		t.Fatalf("clash on replace: %d %s", code, b)
	}
	if f := r.get("fast"); f.Model != "" || !reflect.DeepEqual(f.Aliases["en"], []string{"Quick", "fast"}) {
		t.Fatalf("a refused replace changed fast: %+v", f)
	}
}

func TestDeleteMode(t *testing.T) {
	r := newRig(t)
	r.must(200, "PUT", "/modes/turbo", turbo)
	if b := r.must(204, "DELETE", "/modes/turbo", ""); b != "" {
		t.Fatalf("body %q", b)
	}
	if _, err := r.modes.Get(context.Background(), "turbo"); !errors.Is(err, agent.ErrModeNotFound) {
		t.Fatalf("still there: %v", err)
	}
	for _, path := range []string{"/modes/turbo", "/modes/never"} {
		if code, b := r.call("DELETE", path, ""); code != 404 || b != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %s", path, code, b)
		}
	}
	if code, _ := r.call("GET", "/modes/turbo", ""); code != 404 {
		t.Fatalf("get after delete: %d", code)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "msg=mode_deleted name=turbo") {
		t.Fatalf("no mode_deleted in\n%s", logs)
	}
	if n := r.changes.Load(); n != 2 {
		t.Fatalf("%d changes heard, want 2 (a refused delete changes nothing)", n)
	}
}

// a mode the settings name stays: the effective global mode and allowed
// modes (the built-in defaults too) and every device's overrides of either
func TestDeleteModeInUse(t *testing.T) {
	r := newRig(t)
	inUse := func(name string) {
		t.Helper()
		if code, b := r.call("DELETE", "/modes/"+name, ""); code != 409 || b != `{"error":"in_use"}` {
			t.Fatalf("%s: %d %s, want 409 in_use", name, code, b)
		}
		r.get(name)
	}
	// the defaults: mode fast, allowed fast and smart
	inUse("fast")
	inUse("smart")
	for _, name := range []string{"g-mode", "g-allowed", "d-mode", "d-allowed"} {
		r.must(200, "PUT", "/modes/"+name, turbo)
	}
	r.must(200, "PUT", "/settings/global", `{"mode":"g-mode","allowedModes":["fast","smart","g-allowed"]}`)
	inUse("g-mode")
	inUse("g-allowed")
	// a device's mode is among its allowed ones, so the api cannot set it alone
	ctx := context.Background()
	if err := r.q.PutSettings(ctx, db.PutSettingsParams{Scope: r.dev, Data: []byte(`{"mode":"d-mode"}`)}); err != nil {
		t.Fatal(err)
	}
	inUse("d-mode")
	r.must(200, "PUT", "/devices/"+r.dev+"/settings", `{"allowedModes":["d-mode","d-allowed"]}`)
	inUse("d-allowed")
	// settings that name none of them free all four, and smart once the global list leaves it out
	if err := r.q.DeleteSettings(ctx, r.dev); err != nil {
		t.Fatal(err)
	}
	r.must(200, "PUT", "/settings/global", `{"allowedModes":["fast"]}`)
	for _, name := range []string{"g-mode", "g-allowed", "d-mode", "d-allowed", "smart"} {
		r.must(204, "DELETE", "/modes/"+name, "")
	}
	inUse("fast")
	if logs := r.logs.String(); strings.Contains(logs, "mode_deleted name=fast") {
		t.Fatalf("refused delete logged:\n%s", logs)
	}
}

// the modes are admin's alone
func TestModesNeedAdmin(t *testing.T) {
	r := newRig(t)
	for _, c := range [][3]string{
		{"GET", "/modes", ""}, {"GET", "/modes/fast", ""}, {"PUT", "/modes/turbo", turbo}, {"DELETE", "/modes/smart", ""},
	} {
		if code, b := r.as(apitest.HABearer, c[0], c[1], c[2]); code != 403 || b != `{"error":"forbidden"}` {
			t.Fatalf("ha %s %s: %d %s", c[0], c[1], code, b)
		}
	}
	if _, err := r.modes.Get(context.Background(), "turbo"); !errors.Is(err, agent.ErrModeNotFound) {
		t.Fatalf("ha put stored: %v", err)
	}
	r.get("smart")
}
