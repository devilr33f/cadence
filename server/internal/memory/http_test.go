package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

const (
	adminToken = "good-token"
	ghostID    = "00000000-0000-4000-8000-000000000001"
)

type adminRig struct {
	*rig
	adm  *memory.Admin
	app  *fiber.App
	emb  *textEmbed
	bus  *events.Bus
	logs *bytes.Buffer
}

func (r *adminRig) mount(adm *memory.Admin) {
	r.adm = adm
	r.t.Cleanup(adm.Close)
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Only(adminToken),
		Admin:       []func(huma.API){func(h huma.API) { memory.RegisterAdmin(h, adm) }},
	})
}

func newAdminRig(t *testing.T) *adminRig {
	r := &adminRig{rig: newRig(t), emb: newTextEmbed(), bus: events.NewBus(0), logs: &bytes.Buffer{}}
	r.mount(memory.NewAdmin(memory.AdminDeps{
		Store: r.s, Bus: r.bus, Log: slog.New(slog.NewTextHandler(r.logs, nil)),
		Embedder: func(context.Context) (providers.Embedder, error) { return r.emb, nil },
	}))
	return r
}

func (r *adminRig) do(method, path, body string) (int, string) {
	r.t.Helper()
	return r.doAs(adminToken, method, path, body)
}

func (r *adminRig) doAs(token, method, path, body string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type memJSON struct {
	ID         string  `json:"id"`
	Scope      string  `json:"scope"`
	DeviceID   *string `json:"deviceId"`
	Kind       string  `json:"kind"`
	Text       string  `json:"text"`
	Pinned     bool    `json:"pinned"`
	Hits       int     `json:"hits"`
	LastHitAt  *string `json:"lastHitAt"`
	Confidence float64 `json:"confidence"`
	Evidence   []int64 `json:"evidence"`
	Source     string  `json:"source"`
	Status     string  `json:"status"`
	Replaces   string  `json:"replaces"`
	Proposal   string  `json:"proposal"`
	Embedded   bool    `json:"embedded"`
}

func (r *adminRig) create(body string) memJSON {
	r.t.Helper()
	code, out := r.do(http.MethodPost, "/api/v1/memories", body)
	if code != http.StatusCreated {
		r.t.Fatalf("%d %s", code, out)
	}
	var m memJSON
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *adminRig) list(query string) []memJSON {
	r.t.Helper()
	code, out := r.do(http.MethodGet, "/api/v1/memories"+query, "")
	if code != http.StatusOK {
		r.t.Fatalf("%d %s", code, out)
	}
	var ms []memJSON
	if err := json.Unmarshal([]byte(out), &ms); err != nil {
		r.t.Fatal(err)
	}
	return ms
}

func jtexts(ms []memJSON) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.Text)
	}
	return out
}

func (r *adminRig) stored(id string) memory.Memory {
	r.t.Helper()
	m, err := r.s.Get(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *adminRig) gone(id string) bool {
	_, err := r.s.Get(context.Background(), id)
	return err != nil
}

func (r *adminRig) patch(id, body string) (int, string) {
	return r.do(http.MethodPatch, "/api/v1/memories/"+id, body)
}

func (r *adminRig) post(id, action string) (int, string) {
	return r.do(http.MethodPost, "/api/v1/memories/"+id+"/"+action, "")
}

func (r *adminRig) learnedPending(text string) memory.Memory {
	return r.add(memory.NewMemory{Text: text, Source: memory.SourceLearned, Status: memory.StatusPending, Confidence: 0.7, Evidence: []int64{5}})
}

func (r *adminRig) proposal(target memory.Memory, kind, text string) memory.Memory {
	return r.add(memory.NewMemory{
		Scope: target.Scope, DeviceID: target.DeviceID, Text: text, Kind: memory.KindPreference, Source: memory.SourceLearned,
		Status: memory.StatusPending, Replaces: target.ID, Proposal: kind, Confidence: 0.6, Evidence: []int64{1, 9},
	})
}

func TestAdminAuth(t *testing.T) {
	r := newAdminRig(t)
	for _, tok := range []string{"", "bad"} {
		for _, rt := range [][2]string{
			{http.MethodGet, "/api/v1/memories"}, {http.MethodPost, "/api/v1/memories"},
			{http.MethodPatch, "/api/v1/memories/" + r.d1}, {http.MethodDelete, "/api/v1/memories/" + r.d1},
			{http.MethodPost, "/api/v1/memories/" + r.d1 + "/approve"}, {http.MethodPost, "/api/v1/memories/" + r.d1 + "/reject"},
			{http.MethodGet, "/api/v1/devices/" + r.d1 + "/compactions"}, {http.MethodGet, "/api/v1/devices/" + r.d1 + "/learning"},
		} {
			if code, _ := r.doAs(tok, rt[0], rt[1], "{}"); code != http.StatusUnauthorized {
				t.Fatalf("%q %v: %d", tok, rt, code)
			}
		}
	}
}

func TestAdminCreateGetsListed(t *testing.T) {
	r := newAdminRig(t)
	g := r.create(`{"scope":"global","kind":"fact","text":"likes tea"}`)
	d := r.create(fmt.Sprintf(`{"scope":"device","deviceId":%q,"kind":"preference","text":"kitchen is loud"}`, r.d1))
	if g.Scope != "global" || g.DeviceID != nil || g.Source != "manual" || g.Status != "active" || g.Pinned || g.Confidence != 1 ||
		g.Evidence == nil || g.Hits != 0 || g.LastHitAt != nil {
		t.Fatalf("%+v", g)
	}
	if d.Scope != "device" || d.DeviceID == nil || *d.DeviceID != r.d1 {
		t.Fatalf("%+v", d)
	}
	if got := jtexts(r.list("")); !eq(got, []string{"kitchen is loud", "likes tea"}) {
		t.Fatalf("newest first: %v", got)
	}
}

func TestAdminListFiltersAndPages(t *testing.T) {
	r := newAdminRig(t)
	r.add(memory.NewMemory{Text: "g-fact"})
	r.add(memory.NewMemory{Text: "g-pref", Kind: memory.KindPreference, Pinned: true})
	r.add(memory.NewMemory{Text: "d1", Scope: memory.ScopeDevice, DeviceID: r.d1})
	r.add(memory.NewMemory{Text: "d2", Scope: memory.ScopeDevice, DeviceID: r.d2})
	r.add(memory.NewMemory{Text: "pend", Status: memory.StatusPending, Source: memory.SourceLearned})
	cases := []struct {
		q    string
		want []string
	}{
		{"?scope=global", []string{"pend", "g-pref", "g-fact"}},
		{"?scope=device", []string{"d2", "d1"}},
		{"?scope=device&deviceId=" + r.d1, []string{"d1"}},
		{"?kind=preference", []string{"g-pref"}},
		{"?status=pending", []string{"pend"}},
		{"?status=active&scope=global", []string{"g-pref", "g-fact"}},
		{"?pinned=true", []string{"g-pref"}},
		{"?pinned=false&scope=global&status=active", []string{"g-fact"}},
		{"?limit=2", []string{"pend", "d2"}},
		{"?limit=2&offset=2", []string{"d1", "g-pref"}},
		{"?limit=2&offset=4", []string{"g-fact"}},
		{"?limit=100000&offset=99", []string{}},
	}
	for _, c := range cases {
		if got := jtexts(r.list(c.q)); !eq(got, c.want) {
			t.Fatalf("%s: %v", c.q, got)
		}
	}
	for _, q := range []string{"?scope=x", "?kind=x", "?status=x", "?pinned=maybe", "?deviceId=nope", "?limit=a", "?limit=0", "?offset=-1"} {
		code, body := r.do(http.MethodGet, "/api/v1/memories"+q, "")
		name, _, _ := strings.Cut(strings.TrimPrefix(q, "?"), "=")
		if code != 422 || !strings.Contains(body, `"path":"`+name+`"`) {
			t.Fatalf("%s: %d %s", q, code, body)
		}
	}
}

func TestAdminListCapsAtTwoHundred(t *testing.T) {
	r := newAdminRig(t)
	for i := range 205 {
		r.add(memory.NewMemory{Text: fmt.Sprintf("m%03d", i)})
	}
	if n := len(r.list("?limit=1000")); n != 200 {
		t.Fatalf("%d", n)
	}
}

func TestAdminCreateValidation(t *testing.T) {
	r := newAdminRig(t)
	long := strings.Repeat("x", 501)
	cases := []struct{ body, path string }{
		{`{"scope":"other","kind":"fact","text":"a"}`, "scope"},
		{`{"kind":"fact","text":"a"}`, "scope"},
		{`{"scope":"global","kind":"nope","text":"a"}`, "kind"},
		{`{"scope":"global","text":"a"}`, "kind"},
		{`{"scope":"global","kind":"fact","text":""}`, "text"},
		{`{"scope":"global","kind":"fact","text":"   "}`, "text"},
		{`{"scope":"global","kind":"fact"}`, "text"},
		{`{"scope":"global","kind":"fact","text":"` + long + `"}`, "text"},
		{`{"scope":"global","deviceId":"` + r.d1 + `","kind":"fact","text":"a"}`, "deviceId"},
		{`{"scope":"device","kind":"fact","text":"a"}`, "deviceId"},
		{`{"scope":"device","deviceId":"` + ghostID + `","kind":"fact","text":"a"}`, "deviceId"},
		{`{"scope":"device","deviceId":"nope","kind":"fact","text":"a"}`, "deviceId"},
		{`{"scope":"global","kind":"fact","text":"a","bogus":1}`, ""},
		{`not json`, ""},
	}
	for _, c := range cases {
		code, body := r.do(http.MethodPost, "/api/v1/memories", c.body)
		if code != 422 || !strings.HasPrefix(body, `{"error":"invalid","path":"`+c.path+`"`) {
			t.Fatalf("%s: %d %s", c.body, code, body)
		}
	}
	// 500 characters, counted as characters and not bytes, is fine
	r.create(`{"scope":"global","kind":"fact","text":"` + strings.Repeat("я", 500) + `"}`)
	if n := len(r.list("")); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestAdminSecretFilterGuardsCreateAndEdit(t *testing.T) {
	r := newAdminRig(t)
	code, body := r.do(http.MethodPost, "/api/v1/memories", `{"scope":"global","kind":"fact","text":"the password is hunter22"}`)
	if code != 422 || body != `{"error":"looks_like_secret","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	m := r.create(`{"scope":"global","kind":"fact","text":"likes tea"}`)
	code, body = r.patch(m.ID, `{"text":"mail me at someone@example.com"}`)
	if code != 422 || body != `{"error":"looks_like_secret","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	if r.stored(m.ID).Text != "likes tea" {
		t.Fatal("edit applied")
	}
	// the admin can still store a non-secret
	r.create(`{"scope":"global","kind":"fact","text":"works at a bakery"}`)
}

func TestAdminManualCreateEmbedsAsyncAndIsRecalled(t *testing.T) {
	r := newAdminRig(t)
	r.emb.set("likes oolong", unit(20))
	r.emb.set("what tea", unit(20))
	rc := memory.NewRecaller(memory.RecallerDeps{
		Store: r.s, Embeds: embeds{byName: map[string]providers.Embedder{"emb": r.emb}},
		GlobalEmbed: func(context.Context) (string, error) { return "emb", nil },
	})
	recall := func() []string {
		return recalledTexts(rc.Recall(context.Background(), memory.Query{DeviceID: r.d1, Input: "what tea", Embed: "emb"}))
	}
	gate := make(chan struct{})
	r.mount(memory.NewAdmin(memory.AdminDeps{
		Store: r.s,
		Embedder: func(context.Context) (providers.Embedder, error) {
			<-gate
			return r.emb, nil
		},
	}))
	m := r.create(`{"scope":"global","kind":"fact","text":"likes oolong"}`)
	if m.Embedded || r.stored(m.ID).HasEmbedding {
		t.Fatal("embedded before the embedder answered")
	}
	if got := recall(); len(got) != 0 {
		t.Fatalf("an unpinned memory without a vector was recalled: %v", got)
	}
	close(gate)
	r.adm.Wait()
	if st := r.stored(m.ID); !st.HasEmbedding || st.EmbedModel != model {
		t.Fatalf("%+v", st)
	}
	if got := recall(); !eq(got, []string{"likes oolong"}) {
		t.Fatalf("%v", got)
	}
}

func TestAdminPatch(t *testing.T) {
	r := newAdminRig(t)
	m := r.create(`{"scope":"global","kind":"fact","text":"likes tea"}`)
	r.adm.Wait()
	if !r.stored(m.ID).HasEmbedding {
		t.Fatal("no embedding")
	}
	if code, body := r.patch(m.ID, `{"pinned":true}`); code != 200 || !strings.Contains(body, `"pinned":true`) {
		t.Fatalf("%d %s", code, body)
	}
	if !r.stored(m.ID).HasEmbedding {
		t.Fatal("a pin dropped the embedding")
	}
	if code, body := r.patch(m.ID, `{"text":"likes coffee","kind":"preference"}`); code != 200 || !strings.Contains(body, `"text":"likes coffee"`) {
		t.Fatalf("%d %s", code, body)
	}
	if st := r.stored(m.ID); st.Kind != "preference" || !st.Pinned {
		t.Fatalf("%+v", st)
	}
	r.adm.Wait()
	if st := r.stored(m.ID); !st.HasEmbedding || st.EmbedModel != model {
		t.Fatalf("re-queued embedding never landed: %+v", st)
	}
	if got := r.emb.called(); len(got) != 2 || got[1][0] != "likes coffee" {
		t.Fatalf("embed calls: %v", got)
	}
	// scope: global -> device -> global, the vector stays
	if code, body := r.patch(m.ID, fmt.Sprintf(`{"scope":"device","deviceId":%q}`, r.d1)); code != 200 || !strings.Contains(body, r.d1) {
		t.Fatalf("%d %s", code, body)
	}
	if st := r.stored(m.ID); st.Scope != "device" || st.DeviceID != r.d1 || !st.HasEmbedding {
		t.Fatalf("%+v", st)
	}
	if code, _ := r.patch(m.ID, `{"scope":"global"}`); code != 200 {
		t.Fatal(code)
	}
	if st := r.stored(m.ID); st.Scope != "global" || st.DeviceID != "" {
		t.Fatalf("%+v", st)
	}
	for body, path := range map[string]string{
		`{"text":""}`: "text", `{"kind":"x"}`: "kind", `{"scope":"x"}`: "scope", `{"deviceId":"` + r.d1 + `"}`: "deviceId",
		`{"scope":"device"}`: "deviceId", `{"scope":"global","deviceId":"` + r.d1 + `"}`: "deviceId",
		`{"scope":"device","deviceId":"` + ghostID + `"}`: "deviceId", `{"nope":1}`: "",
	} {
		code, out := r.patch(m.ID, body)
		if code != 422 || !strings.HasPrefix(out, `{"error":"invalid","path":"`+path+`"`) {
			t.Fatalf("%s: %d %s", body, code, out)
		}
	}
	for _, id := range []string{ghostID, "nope"} {
		if code, body := r.patch(id, `{"pinned":true}`); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
}

func TestAdminEditMakesLearnedMemoryManual(t *testing.T) {
	r := newAdminRig(t)
	learned := func(text string) memory.Memory {
		return r.add(memory.NewMemory{Text: text, Source: memory.SourceLearned})
	}
	edit := func(id, body string) {
		t.Helper()
		if code, out := r.patch(id, body); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
	}
	a, b, c, d := learned("a"), learned("b"), learned("c"), learned("d")
	edit(a.ID, `{"text":"a2"}`)
	edit(b.ID, `{"kind":"alias"}`)
	edit(c.ID, `{"pinned":true}`)
	edit(d.ID, `{"text":"d","kind":"fact"}`)
	for _, id := range []string{a.ID, b.ID} {
		st := r.stored(id)
		if st.Source != memory.SourceManual {
			t.Fatalf("%+v", st)
		}
		// learning treats a manual memory as immutable
		if memory.Untouchable(st) == "" {
			t.Fatal("learning may still change it")
		}
	}
	if st := r.stored(c.ID); !st.Pinned || memory.Untouchable(st) == "" {
		t.Fatalf("%+v", st)
	}
	if r.stored(d.ID).Source != memory.SourceLearned {
		t.Fatal("an edit that changes nothing changed the source")
	}
}

func TestAdminEditDropsStaleProposals(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned})
	r.proposal(target, memory.ProposalUpdate, "likes coffee")
	if code, out := r.patch(target.ID, `{"text":"likes mate"}`); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if got := r.list("?status=pending"); len(got) != 0 {
		t.Fatalf("stale proposal stays: %v", jtexts(got))
	}
}

func TestAdminEditProposalRefused(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned})
	p := r.proposal(target, memory.ProposalUpdate, "likes coffee")
	if code, body := r.patch(p.ID, `{"text":"x"}`); code != 409 || body != `{"error":"conflict"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAdminDelete(t *testing.T) {
	r := newAdminRig(t)
	m := r.create(`{"scope":"global","kind":"fact","text":"likes tea"}`)
	if code, body := r.do(http.MethodDelete, "/api/v1/memories/"+m.ID, ""); code != 204 || body != "" {
		t.Fatalf("%d %s", code, body)
	}
	for _, id := range []string{m.ID, "nope"} {
		if code, body := r.do(http.MethodDelete, "/api/v1/memories/"+id, ""); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%d %s", code, body)
		}
	}
}

func TestAdminApproveAdd(t *testing.T) {
	r := newAdminRig(t)
	p := r.learnedPending("likes tea")
	code, body := r.post(p.ID, "approve")
	if code != 200 || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("%d %s", code, body)
	}
	if st := r.stored(p.ID); st.Status != memory.StatusActive || st.Source != memory.SourceLearned {
		t.Fatalf("%+v", st)
	}
	if code, body := r.post(p.ID, "approve"); code != 409 || body != `{"error":"conflict"}` {
		t.Fatalf("already active: %d %s", code, body)
	}
	if code, _ := r.post(p.ID, "reject"); code != 409 {
		t.Fatalf("reject of an active one: %d", code)
	}
}

func TestAdminApproveAddEvictsPastCap(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	ids := make([]string, 0, 300)
	for i := range 300 {
		ids = append(ids, r.add(memory.NewMemory{Text: fmt.Sprintf("m%03d", i), Source: memory.SourceLearned}).ID)
	}
	// a hit and a pin protect two of the oldest
	if err := r.s.Hit(ctx, ids[1:2]); err != nil {
		t.Fatal(err)
	}
	pin := true
	if _, err := r.s.Update(ctx, ids[0], memory.Patch{Pinned: &pin}); err != nil {
		t.Fatal(err)
	}
	p := r.learnedPending("one more")
	if code, body := r.post(p.ID, "approve"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	n, err := r.s.CountByScope(ctx, memory.ScopeGlobal, "")
	if err != nil || n != 300 {
		t.Fatalf("%d %v", n, err)
	}
	if r.gone(ids[0]) || r.gone(ids[1]) {
		t.Fatal("pinned or hit memory evicted")
	}
	if !r.gone(ids[2]) {
		t.Fatal("lowest-scored memory kept")
	}
}

func TestAdminApproveAddFullOfManualIsConflict(t *testing.T) {
	r := newAdminRig(t)
	for i := range 300 {
		r.add(memory.NewMemory{Text: fmt.Sprintf("m%03d", i)})
	}
	p := r.learnedPending("one more")
	if code, body := r.post(p.ID, "approve"); code != 409 || body != `{"error":"conflict"}` {
		t.Fatalf("%d %s", code, body)
	}
	if r.stored(p.ID).Status != memory.StatusPending {
		t.Fatal("approved anyway")
	}
}

func TestAdminCreateEvictsLearnedPastCap(t *testing.T) {
	r := newAdminRig(t)
	for i := range 300 {
		r.add(memory.NewMemory{Text: fmt.Sprintf("m%03d", i), Source: memory.SourceLearned})
	}
	r.create(`{"scope":"global","kind":"fact","text":"manual one"}`)
	n, err := r.s.CountByScope(context.Background(), memory.ScopeGlobal, "")
	if err != nil || n != 300 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestAdminApproveUpdateProposal(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned, Evidence: []int64{1}})
	if err := r.s.Hit(context.Background(), []string{target.ID}); err != nil {
		t.Fatal(err)
	}
	p := r.proposal(target, memory.ProposalUpdate, "likes green tea")
	other := r.proposal(target, memory.ProposalUpdate, "likes black tea")
	code, body := r.post(p.ID, "approve")
	if code != 200 || !strings.Contains(body, `"id":"`+target.ID+`"`) || !strings.Contains(body, `"text":"likes green tea"`) {
		t.Fatalf("%d %s", code, body)
	}
	st := r.stored(target.ID)
	if st.Text != "likes green tea" || st.Kind != memory.KindPreference || st.Hits != 1 || st.Source != memory.SourceLearned ||
		!slices.Equal(st.Evidence, []int64{1, 9}) {
		t.Fatalf("%+v", st)
	}
	if got := r.list("?status=pending"); len(got) != 0 {
		t.Fatalf("proposals left: %v", jtexts(got))
	}
	if !r.gone(p.ID) || !r.gone(other.ID) {
		t.Fatal("proposal row survived")
	}
	r.adm.Wait()
	if !r.stored(target.ID).HasEmbedding {
		t.Fatal("applied text never re-embedded")
	}
}

func TestAdminApproveUpdateConflicts(t *testing.T) {
	r := newAdminRig(t)
	for name, mk := range map[string]memory.NewMemory{
		"manual": {Text: "a", Source: memory.SourceManual},
		"pinned": {Text: "b", Source: memory.SourceLearned, Pinned: true},
	} {
		target := r.add(mk)
		p := r.proposal(target, memory.ProposalUpdate, "changed "+name)
		if code, body := r.post(p.ID, "approve"); code != 409 || body != `{"error":"conflict"}` {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if r.stored(target.ID).Text != mk.Text {
			t.Fatalf("%s: applied", name)
		}
		if r.stored(p.ID).Status != memory.StatusPending {
			t.Fatalf("%s: proposal consumed", name)
		}
		// the admin can still turn it down
		if code, _ := r.post(p.ID, "reject"); code != 204 {
			t.Fatal(code)
		}
	}
}

func TestAdminApproveProposalOfDeletedTargetIsGone(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned})
	p := r.proposal(target, memory.ProposalUpdate, "likes coffee")
	if code, _ := r.do(http.MethodDelete, "/api/v1/memories/"+target.ID, ""); code != 204 {
		t.Fatal(code)
	}
	if code, body := r.post(p.ID, "approve"); code != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAdminApproveDeleteProposal(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned})
	p := r.proposal(target, memory.ProposalDelete, "likes tea")
	if code, body := r.post(p.ID, "approve"); code != 204 || body != "" {
		t.Fatalf("%d %s", code, body)
	}
	if got := r.list(""); len(got) != 0 {
		t.Fatalf("%v", jtexts(got))
	}
	manual := r.add(memory.NewMemory{Text: "keep", Source: memory.SourceManual})
	p2 := r.proposal(manual, memory.ProposalDelete, "keep")
	if code, body := r.post(p2.ID, "approve"); code != 409 || body != `{"error":"conflict"}` {
		t.Fatalf("%d %s", code, body)
	}
	if r.gone(manual.ID) {
		t.Fatal("deleted a manual memory")
	}
}

func TestAdminReject(t *testing.T) {
	r := newAdminRig(t)
	add := r.learnedPending("likes tea")
	target := r.add(memory.NewMemory{Text: "t", Source: memory.SourceLearned})
	upd := r.proposal(target, memory.ProposalUpdate, "t2")
	for _, id := range []string{add.ID, upd.ID} {
		if code, body := r.post(id, "reject"); code != 204 || body != "" {
			t.Fatalf("%d %s", code, body)
		}
		if !r.gone(id) {
			t.Fatal("kept")
		}
	}
	if r.stored(target.ID).Text != "t" {
		t.Fatal("target changed")
	}
	if code, _ := r.post(target.ID, "reject"); code != 409 {
		t.Fatalf("reject of an active memory: %d", code)
	}
	for _, a := range []string{"approve", "reject"} {
		if code, body := r.post(ghostID, a); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %s", a, code, body)
		}
	}
}

func TestAdminCompactionsAndLearning(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	c1, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d1, UptoMessageID: 10, Summary: "s1", OpenThreads: []byte(`["call mum"]`), Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d1, UptoMessageID: 20, Summary: "s2", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.AddCompaction(ctx, memory.NewCompaction{DeviceID: r.d2, UptoMessageID: 5, Summary: "other", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	mem := r.add(memory.NewMemory{Text: "x"})
	if _, err := r.s.AddRun(ctx, memory.NewRun{CompactionID: c1.ID, DeviceID: r.d1, Applied: 1, Dropped: 2, Pending: 3, MemoryIDs: []string{mem.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.AddRun(ctx, memory.NewRun{CompactionID: c2.ID, DeviceID: r.d1}); err != nil {
		t.Fatal(err)
	}

	code, body := r.do(http.MethodGet, "/api/v1/devices/"+r.d1+"/compactions", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var cs []struct {
		ID            string   `json:"id"`
		UptoMessageID int64    `json:"uptoMessageId"`
		Summary       string   `json:"summary"`
		OpenThreads   []string `json:"openThreads"`
		Model         string   `json:"model"`
		CreatedAt     string   `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(body), &cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != c2.ID || cs[0].UptoMessageID != 20 || cs[0].Summary != "s2" || cs[0].Model != "m" ||
		cs[0].OpenThreads == nil || len(cs[0].OpenThreads) != 0 || cs[1].ID != c1.ID || !slices.Equal(cs[1].OpenThreads, []string{"call mum"}) ||
		cs[1].CreatedAt == "" {
		t.Fatalf("%s", body)
	}
	_, body = r.do(http.MethodGet, "/api/v1/devices/"+r.d1+"/compactions?limit=1", "")
	if strings.Count(body, `"summary"`) != 1 {
		t.Fatalf("%s", body)
	}

	code, body = r.do(http.MethodGet, "/api/v1/devices/"+r.d1+"/learning", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var ls []struct {
		ID           string   `json:"id"`
		CompactionID string   `json:"compactionId"`
		Applied      int      `json:"applied"`
		Dropped      int      `json:"dropped"`
		Pending      int      `json:"pending"`
		MemoryIDs    []string `json:"memoryIds"`
		CreatedAt    string   `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(body), &ls); err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].CompactionID != c2.ID || ls[0].MemoryIDs == nil || len(ls[0].MemoryIDs) != 0 ||
		ls[1].CompactionID != c1.ID || ls[1].Applied != 1 || ls[1].Dropped != 2 || ls[1].Pending != 3 ||
		!slices.Equal(ls[1].MemoryIDs, []string{mem.ID}) {
		t.Fatalf("%s", body)
	}

	for _, p := range []string{"compactions", "learning"} {
		for _, id := range []string{ghostID, "nope"} {
			if code, body := r.do(http.MethodGet, "/api/v1/devices/"+id+"/"+p, ""); code != 404 || body != `{"error":"not_found"}` {
				t.Fatalf("%s %s: %d %s", p, id, code, body)
			}
		}
		if code, body := r.do(http.MethodGet, "/api/v1/devices/"+r.d1+"/"+p+"?limit=x", ""); code != 422 || !strings.Contains(body, `"path":"limit"`) {
			t.Fatalf("%d %s", code, body)
		}
	}
}

func TestAdminChangedEventsCarryNoText(t *testing.T) {
	r := newAdminRig(t)
	ch, stop := r.bus.Subscribe(func(e events.Event) bool { return e.Type == events.MemoryChanged })
	defer stop()
	m := r.create(`{"scope":"device","deviceId":"` + r.d1 + `","kind":"fact","text":"secret-ish phrase"}`)
	r.patch(m.ID, `{"pinned":true}`)
	r.do(http.MethodDelete, "/api/v1/memories/"+m.ID, "")
	r.post(r.learnedPending("pending phrase").ID, "approve")
	r.post(r.learnedPending("another phrase").ID, "reject")
	var got []string
	for range 5 {
		select {
		case e := <-ch:
			d, ok := e.Data.(map[string]any)
			if !ok || len(d) != 2 || d["id"] == "" {
				t.Fatalf("%+v", e)
			}
			if raw, _ := json.Marshal(e); strings.Contains(string(raw), "phrase") {
				t.Fatalf("text in event: %s", raw)
			}
			got = append(got, fmt.Sprint(d["action"]))
		case <-time.After(2 * time.Second):
			t.Fatalf("got %v", got)
		}
	}
	if !eq(got, []string{"created", "updated", "deleted", "approved", "rejected"}) {
		t.Fatalf("%v", got)
	}
}

func TestAdminLogsCarryNoText(t *testing.T) {
	r := newAdminRig(t)
	m := r.create(`{"scope":"global","kind":"fact","text":"private-phrase-one"}`)
	r.patch(m.ID, `{"text":"private-phrase-two"}`)
	r.do(http.MethodPost, "/api/v1/memories", `{"scope":"global","kind":"fact","text":"the token is private-phrase-three-x9"}`)
	r.post(r.learnedPending("private-phrase-four").ID, "approve")
	r.adm.Wait()
	r.do(http.MethodDelete, "/api/v1/memories/"+m.ID, "")
	if strings.Contains(r.logs.String(), "private-phrase") {
		t.Fatalf("memory text in logs:\n%s", r.logs)
	}
}

func TestAdminEmbedFailureIsLoggedByCodeOnly(t *testing.T) {
	r := newAdminRig(t)
	r.emb.failFrom(1)
	m := r.create(`{"scope":"global","kind":"fact","text":"private-phrase-five"}`)
	r.adm.Wait()
	if r.stored(m.ID).HasEmbedding {
		t.Fatal("embedded")
	}
	logs := r.logs.String()
	if strings.Contains(logs, "private-phrase") || !strings.Contains(logs, m.ID) {
		t.Fatalf("%s", logs)
	}
}

func TestAdminTextWithNulOrBadUTF8IsInvalid(t *testing.T) {
	r := newAdminRig(t)
	code, body := r.do(http.MethodPost, "/api/v1/memories", `{"scope":"global","kind":"fact","text":"a\u0000b"}`)
	if code != 422 || body != `{"error":"invalid","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	m := r.create(`{"scope":"global","kind":"fact","text":"likes tea"}`)
	if code, body := r.patch(m.ID, `{"text":"a\u0000b"}`); code != 422 || body != `{"error":"invalid","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	// json decoding repairs bad bytes, so only a direct call carries them
	_, err := r.adm.Create(context.Background(), memory.Create{Scope: memory.ScopeGlobal, Kind: memory.KindFact, Text: "a\xffb"})
	var bad *memory.InvalidError
	if !errors.As(err, &bad) || bad.Path != "text" {
		t.Fatalf("%v", err)
	}
}

func TestAdminApproveRechecksProposalText(t *testing.T) {
	r := newAdminRig(t)
	target := r.add(memory.NewMemory{Text: "likes tea", Source: memory.SourceLearned})
	secret := r.proposal(target, memory.ProposalUpdate, "the password is hunter22")
	if code, body := r.post(secret.ID, "approve"); code != 422 || body != `{"error":"looks_like_secret","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	long := r.proposal(target, memory.ProposalUpdate, strings.Repeat("x", 100))
	if _, err := r.pool.Exec(context.Background(), `UPDATE memories SET text = repeat('x', 500) WHERE id = $1`, long.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := r.post(long.ID, "approve"); code != 200 {
		t.Fatalf("500 chars is allowed: %d", code)
	}
	pend := r.learnedPending("contact me at someone@example.com")
	if code, body := r.post(pend.ID, "approve"); code != 422 || body != `{"error":"looks_like_secret","path":"text"}` {
		t.Fatalf("%d %s", code, body)
	}
	if r.stored(pend.ID).Status != memory.StatusPending || r.stored(target.ID).Text == "the password is hunter22" {
		t.Fatal("applied")
	}
}

// concurrent creates in one scope take turns, so its cap holds
func TestAdminConcurrentCreatesKeepTheCap(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `INSERT INTO memories (scope, kind, text, confidence, source, status)
		SELECT 'global', 'fact', 'filler ' || g, 1, 'manual', 'active' FROM generate_series(1, 299) g`); err != nil {
		t.Fatal(err)
	}
	adm := memory.NewAdmin(memory.AdminDeps{Store: r.s})
	t.Cleanup(adm.Close)
	const n = 12
	var wg sync.WaitGroup
	// open the pool's connections first, so that their setup does not space the creates out
	for range r.pool.Config().MaxConns {
		wg.Go(func() { _, _ = r.pool.Exec(ctx, `SELECT pg_sleep(0.1)`) })
	}
	wg.Wait()
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = adm.Create(ctx, memory.Create{Scope: memory.ScopeGlobal, Kind: memory.KindFact, Text: fmt.Sprintf("new %d", i)})
		})
	}
	close(start)
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, memory.ErrConflict):
			t.Fatal(err)
		}
	}
	if count, err := r.s.CountByScope(ctx, memory.ScopeGlobal, ""); err != nil || count != 300 || created != 1 {
		t.Fatalf("count %d %v, created %d", count, err, created)
	}
}

func TestAdminScopeMoveIntoFullScope(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	fill := func(source string) {
		for i := range 300 {
			r.add(memory.NewMemory{Text: fmt.Sprintf("d%03d", i), Scope: memory.ScopeDevice, DeviceID: r.d1, Source: source})
		}
	}
	move := fmt.Sprintf(`{"scope":"device","deviceId":%q}`, r.d1)
	fill(memory.SourceLearned)
	m := r.create(`{"scope":"global","kind":"fact","text":"mover"}`)
	if code, body := r.patch(m.ID, move); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if n, err := r.s.CountByScope(ctx, memory.ScopeDevice, r.d1); err != nil || n != 300 {
		t.Fatalf("%d %v", n, err)
	}
	if r.stored(m.ID).Scope != memory.ScopeDevice {
		t.Fatal("not moved")
	}
	if _, err := r.pool.Exec(ctx, `UPDATE memories SET source = 'manual' WHERE scope = 'device'`); err != nil {
		t.Fatal(err)
	}
	m2 := r.create(`{"scope":"global","kind":"fact","text":"mover two"}`)
	if code, body := r.patch(m2.ID, move); code != 409 || body != `{"error":"conflict"}` {
		t.Fatalf("%d %s", code, body)
	}
	if st := r.stored(m2.ID); st.Scope != memory.ScopeGlobal {
		t.Fatalf("%+v", st)
	}
}

// a malformed escape in the path or the query is a 422, never a 500
func TestMalformedEscapesAreNot500(t *testing.T) {
	r := newAdminRig(t)
	for _, c := range []struct{ target, body string }{
		{"/api/v1/memories?limit=%zz", `{"error":"invalid","path":"limit"}`},
		{"/api/v1/memories?bogus=%zz&limit=2", `{"error":"invalid","path":"bogus"}`},
		{"/api/v1/memories?limit=1;2", `{"error":"invalid","path":"limit"}`},
	} {
		if status, body := apitest.Get(t, r.app, c.target, adminToken); status != 422 || body != c.body {
			t.Errorf("%s: %d %s", c.target, status, body)
		}
	}
	for _, target := range []string{"/api/v1/devices/%zz/compactions", "/api/v1/devices/%/learning"} {
		if status, _ := apitest.Get(t, r.app, target, adminToken); status >= 500 {
			t.Errorf("%s: %d", target, status)
		}
	}
}
