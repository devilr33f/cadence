package subscriptions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func (r *rig) api() func(method, path, body string) (int, string) {
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { RegisterAdmin(a, r.hub, nil) }},
	})
	return func(method, path, body string) (int, string) {
		return apitest.Call(r.t, app, "x", method, "/api/v1"+path, body)
	}
}

func TestAPI(t *testing.T) {
	r := newRig(t)
	do := r.api()
	r.mod.entries = []Entry{
		{Topic: "t3code:work:thread:a", Kind: "thread", Title: "A"},
		{Topic: "t3code:work:project:p", Kind: "project", Title: "P"},
	}
	for _, c := range []struct{ body, code string }{
		{`{"topic":"t3code:work:*:x","device":"` + r.dev + `"}`, `{"error":"invalid_topic","path":"/body/topic"}`},
		{`{"topic":"hermes:work:*","device":"` + r.dev + `"}`, `{"error":"unknown_module","path":"/body/topic"}`},
		{`{"topic":"t3code:nope:*","device":"` + r.dev + `"}`, `{"error":"unknown_instance","path":"/body/topic"}`},
		{`{"topic":"t3code:work:*","events":["done"],"device":"` + r.dev + `"}`, `{"error":"unsupported_event","path":"/body/events"}`},
	} {
		if st, b := do("POST", "/subscriptions", c.body); st != 422 || b != c.code {
			t.Fatalf("%s: %d %s", c.body, st, b)
		}
	}
	if st, _ := do("POST", "/subscriptions", `{"topic":"t3code:work:*","device":"00000000-0000-0000-0000-000000000009"}`); st != 404 {
		t.Fatalf("unknown device %d", st)
	}
	st, b := do("POST", "/subscriptions", `{"topic":"t3code:work:thread:a","events":["stopped"],"until":"next_stop","device":"`+r.dev+`"}`)
	var s Subscription
	if st != 201 || json.Unmarshal([]byte(b), &s) != nil || s.Delivery != DeliverySpeaker || s.CreatedBy != ByAPI || s.Until != UntilNextStop {
		t.Fatalf("create %d %s", st, b)
	}
	if st, b := do("GET", "/subscriptions?device="+r.dev, ""); st != 200 || !strings.Contains(b, s.ID) {
		t.Fatalf("list %d %s", st, b)
	}
	if st, b := do("GET", "/subscriptions/topics?prefix=t3code:work", ""); st != 200 || !strings.Contains(b, `"title":"P"`) || !strings.Contains(b, `"title":"A"`) {
		t.Fatalf("topics %d %s", st, b)
	}
	if st, b := do("GET", "/subscriptions/topics?prefix=t3code:work:thread:*", ""); st != 200 || strings.Contains(b, `"P"`) || !strings.Contains(b, `"A"`) {
		t.Fatalf("topics under thread %d %s", st, b)
	}
	if st, b := do("GET", "/subscriptions/topics?prefix=t3code:work:bad", ""); st != 422 || b != `{"error":"invalid_topic","path":"/query/prefix"}` {
		t.Fatalf("bad prefix %d %s", st, b)
	}

	r.busy.set(true)
	r.subscribe("t3code:work:*", "", "")
	r.push(asked("q", "Deploy", "tok-q", QuestionApproval))
	st, b = do("GET", "/subscriptions/inbox?device="+r.dev, "")
	if st != 200 || !strings.Contains(b, `"token":"tok-q"`) || !strings.Contains(b, `"state":"pending"`) || !strings.Contains(b, `"open":true`) {
		t.Fatalf("inbox %d %s", st, b)
	}
	if st, b := do("POST", "/subscriptions/reply", `{"token":"tok-q","message":"yes"}`); st != 422 || b != `{"error":"answer_mismatch"}` {
		t.Fatalf("mismatch %d %s", st, b)
	}
	if st, b := do("POST", "/subscriptions/reply", `{"token":"tok-x","decision":"once"}`); st != 404 {
		t.Fatalf("unknown token %d %s", st, b)
	}
	if st, b := do("POST", "/subscriptions/reply", `{"device":"`+r.dev+`","token":"tok-q","decision":"once"}`); st != 200 || strings.Contains(b, `"open":true`) {
		t.Fatalf("reply %d %s", st, b)
	}
	if got := r.mod.answered(); len(got) != 1 || got[0].A.By.Via != ViaDashboard || got[0].A.Decision != DecisionOnce {
		t.Fatalf("module got %+v", got)
	}
	if st, b := do("DELETE", "/subscriptions/"+s.ID, ""); st != 204 || b != "" {
		t.Fatalf("delete %d %s", st, b)
	}
	if st, _ := do("DELETE", "/subscriptions/"+s.ID, ""); st != 404 {
		t.Fatalf("delete twice %d", st)
	}
}
