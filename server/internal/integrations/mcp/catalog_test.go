package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func TestCatalogNamesAndChecksTools(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	f.AddTool(&sdk.Tool{Name: "search.web", Description: "Searches\n\tthe web.", Title: "Web", InputSchema: schema(`{"type":"object","properties":{"q":{"type":"string"}}}`)})
	f.AddTool(&sdk.Tool{Name: "deep", Description: "secret-desc-deep", InputSchema: schema(nestedProps(9))})
	f.AddTool(&sdk.Tool{Name: "big", Description: "B.", InputSchema: schema(sized(9000))})
	f.AddTool(&sdk.Tool{Name: "ref", Description: "R.", InputSchema: schema(`{"type":"object","properties":{"a":{"$ref":"https://schemas.example/a.json"}}}`)})
	f.AddTool(&sdk.Tool{Name: "wide", Description: "W.", InputSchema: schema(props(65))})
	f.AddTool(&sdk.Tool{Name: "regex", Description: "P.", InputSchema: schema(`{"type":"object","properties":{"a":{"type":"string","pattern":"` + strings.Repeat("a", 513) + `"}}}`)})
	r.cat.Apply([]mcp.Server{r.server("Git.Hub", f)})
	e := r.listed("Git.Hub", "search.web")
	if e.Local != "mcp_git_hub_search_web" || e.Skip != "" || e.Description != "[MCP git_hub] Searches the web." ||
		e.Tool.Name != e.Local || e.Tool.Description != e.Description || !strings.Contains(e.Tool.Input, `"q"`) {
		t.Fatalf("entry %+v", e)
	}
	if verr := agent.Validate(e.Tool, json.RawMessage(`{"q":5}`)); verr == nil || verr.Code != "invalid_arguments" {
		t.Fatalf("the compiled schema let a bad argument through: %v", verr)
	}
	for remote, skip := range map[string]string{
		"deep": mcp.SkipSchemaDepth, "big": mcp.SkipSchemaSize, "ref": mcp.SkipSchemaRef, "wide": mcp.SkipSchemaProperties,
		"regex": mcp.SkipSchema,
	} {
		if got := r.entry("Git.Hub", remote); got.Skip != skip || got.Local == "" {
			t.Errorf("%s: %+v", remote, got)
		}
	}
	want := []string{
		"mcp_git_hub_echo", "mcp_git_hub_fail", "mcp_git_hub_huge", "mcp_git_hub_image", "mcp_git_hub_search_web", "mcp_git_hub_slow",
	}
	if got := offeredNames(r.cat); !slices.Equal(got, want) {
		t.Fatalf("offered %v", got)
	}
	eventually(t, "the summary logged", func() bool {
		return strings.Contains(r.logs.String(), "msg=mcp_catalog instance=Git.Hub tools=11 offered=6 changed=0 skipped=5\n")
	})
	for _, reason := range []string{"schema", "schema_depth", "schema_properties", "schema_ref", "schema_size"} {
		if !strings.Contains(r.logs.String(), "msg=mcp_tools_skipped instance=Git.Hub reason="+reason+" count=1\n") {
			t.Fatalf("no %s line:\n%s", reason, r.logs)
		}
	}
	r.private("search.web", "Searches", "secret-desc-deep", "schemas.example", `"q"`)
}

func TestCatalogSkipsBadNamesAndTypes(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	long := strings.Repeat("n", 129)
	f.ListRaw(`[{"name":"str","inputSchema":{"type":"string"}},{"name":"ctl\u0007x","inputSchema":{"type":"object"}},` +
		`{"name":"","inputSchema":{"type":"object"}},{"name":"dup","inputSchema":{"type":"object"}},` +
		`{"name":"dup","description":"second","inputSchema":{"type":"object"}},{"name":"` + long + `","inputSchema":{"type":"object"}}]`)
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	r.listed("srv", "str")
	var got []string
	for _, e := range r.cat.Entries() {
		got = append(got, fmt.Sprintf("%q:%s:%s", e.Remote, e.Local, e.Skip))
	}
	// named tools by local name, then the unnamed by remote name
	want := []string{
		`"dup":mcp_srv_dup:`, `"str":mcp_srv_str:schema_type`,
		`""::name`, `"ctl\ax"::name`, `"dup"::duplicate`, `"` + long + `"::name`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("entries\n%v\nwant\n%v", got, want)
	}
	r.private("ctl", long, "second")
}

func TestCatalogNamesStayAcrossRefreshesAndRestarts(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	f.Add("a.b", "A dot.")
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	if e := r.listed("srv", "a.b"); e.Local != "mcp_srv_a_b" {
		t.Fatalf("first %+v", e)
	}
	// a clean name sanitized onto it turns up: the earlier tool keeps its name
	f.Add("a_b", "A clean one.")
	r.cat.Refresh("srv")
	clean := hashed("mcp_srv_", "a_b", "a_b")
	if e := r.listed("srv", "a_b"); e.Local != clean || r.entry("srv", "a.b").Local != "mcp_srv_a_b" {
		t.Fatalf("after a refresh %v", r.cat.Entries())
	}
	// a restart: another catalog on the same table names them the same
	r.cat.Close()
	again := r.open()
	again.Apply([]mcp.Server{r.server("srv", f)})
	eventually(t, "listed after a restart", func() bool { return len(again.Entries()) == 7 })
	for _, e := range again.Entries() {
		if (e.Remote == "a.b" && e.Local != "mcp_srv_a_b") || (e.Remote == "a_b" && e.Local != clean) {
			t.Fatalf("after a restart %+v", e)
		}
	}
	r.private("a.b", "A dot.", "A clean one.")
}

// a local name never passes to another server's tool, even while the server
// that holds it has not listed since a restart (I1)
func TestCatalogNeverGivesOneNameToTwoServers(t *testing.T) {
	r := newCatalog(t)
	fa, fb := fake(t, mcptest.Options{}), fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("a", fa, withPrefix("x"))})
	if e := r.listed("a", "echo"); e.Local != "mcp_x_echo" {
		t.Fatalf("a %+v", e)
	}
	// a restart with a held up: b, named alike, lists first
	r.cat.Close()
	r.cat = r.open()
	fa.Hold(true)
	r.cat.Apply([]mcp.Server{r.server("a", fa, withPrefix("x")), r.server("b", fb, withPrefix("x"))})
	b := r.listed("b", "echo")
	if b.Local != hashed("mcp_x_", "echo", "echo") || b.Skip != "" {
		t.Fatalf("b took %+v", b)
	}
	for _, e := range r.cat.Offered() {
		if e.Local == "mcp_x_echo" {
			t.Fatalf("a's name offered for %s/%s", e.Instance, e.Remote)
		}
	}
	fa.Hold(false)
	if a := r.listed("a", "echo"); a.Local != "mcp_x_echo" || a.Skip != "" {
		t.Fatalf("a after b %+v", a)
	}
	if r.entry("b", "echo").Local != b.Local {
		t.Fatal("b's name moved")
	}
}

func TestCatalogLeavesAToolWithoutAFreeNameOut(t *testing.T) {
	r := newCatalog(t)
	fa, fb := fake(t, mcptest.Options{}), fake(t, mcptest.Options{})
	// a holds both forms b's echo could take
	clash := hashed("mcp_x_", "echo", "echo")[len("mcp_x_"):]
	fa.Add(clash, "Clash.")
	a := r.server("a", fa, withPrefix("x"))
	r.cat.Apply([]mcp.Server{a})
	r.listed("a", clash)
	r.cat.Apply([]mcp.Server{a, r.server("b", fb, withPrefix("x"))})
	if b := r.listed("b", "echo"); b.Skip != mcp.SkipDuplicate || b.Local != "" {
		t.Fatalf("b %+v", b)
	}
	if r.entry("b", "fail").Local != hashed("mcp_x_", "fail", "fail") {
		t.Fatalf("b's fail %+v", r.entry("b", "fail"))
	}
	// a built-in tool of the same name: the registry leaves the catalog's out
	run := func(context.Context, agent.Call) (json.RawMessage, *agent.ToolError) { return nil, nil }
	reg := agent.NewRegistry(agent.Tool{Name: "mcp_x_echo", Description: "Built in.", Run: run})
	reg.AddSource("mcp", agent.SourceFunc(func() []agent.Tool {
		var out []agent.Tool
		for _, e := range r.cat.Offered() {
			tl := e.Tool
			tl.Run = run
			out = append(out, tl)
		}
		return out
	}))
	if skips := reg.Skips(); !slices.Equal(skips, []agent.Skip{{Source: "mcp", Name: "mcp_x_echo", Reason: agent.SkipDuplicate}}) {
		t.Fatalf("skips %v", skips)
	}
}

func TestCatalogCapsOfferedTools(t *testing.T) {
	r := newCatalog(t)
	var servers []mcp.Server
	for _, name := range []string{"a", "b"} {
		f := fake(t, mcptest.Options{})
		for i := range 70 {
			f.Add(fmt.Sprintf("t%02d", i), "T.")
		}
		servers = append(servers, r.server(name, f))
	}
	servers = append(servers, r.server("c", fake(t, mcptest.Options{})))
	r.cat.Apply(servers)
	eventually(t, "all listed", func() bool { return len(r.cat.Entries()) == 75+75+5 })
	// each of a and b offers echo…slow and t00…t58 by local name; c's would be past 128
	if n := len(r.cat.Offered()); n != 128 {
		t.Fatalf("offered %d", n)
	}
	for _, name := range []string{"a", "b"} {
		if r.entry(name, "t58").Skip != "" || r.entry(name, "t59").Skip != mcp.SkipCap || r.entry(name, "t69").Skip != mcp.SkipCap {
			t.Fatalf("%s: t58 %q t59 %q", name, r.entry(name, "t58").Skip, r.entry(name, "t59").Skip)
		}
	}
	for _, remote := range builtins {
		if e := r.entry("c", remote); e.Skip != mcp.SkipCap {
			t.Fatalf("c %+v", e)
		}
	}
	// the owner's enabledDefault tools rank first in both caps
	on := true
	for _, tool := range [][2]string{{"a", "t69"}, {"c", "echo"}} {
		if _, err := r.cat.Override(context.Background(), tool[0], tool[1], mcp.Patch{EnabledDefault: &on}); err != nil {
			t.Fatal(err)
		}
	}
	for tool, skip := range map[[2]string]string{
		{"a", "t69"}: "", {"a", "t58"}: mcp.SkipCap, {"a", "t57"}: "", {"c", "echo"}: "", {"b", "t58"}: mcp.SkipCap, {"b", "t57"}: "",
	} {
		if e := r.entry(tool[0], tool[1]); e.Skip != skip {
			t.Errorf("%v: %q, want %q", tool, e.Skip, skip)
		}
	}
	if n := len(r.cat.Offered()); n != 128 {
		t.Fatalf("offered %d", n)
	}
}

// an instance pointed at another server under its name: every tool changes
func TestCatalogWithdrawsEveryToolOnAnotherEndpoint(t *testing.T) {
	r := newCatalog(t)
	fa, fb := fake(t, mcptest.Options{}), fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("srv", fa)})
	r.listed("srv", "echo")
	trusted := mcp.TrustTrusted
	if _, err := r.cat.Override(context.Background(), "srv", "echo", mcp.Patch{Trust: &trusted}); err != nil {
		t.Fatal(err)
	}
	moved := r.server("srv", fb)
	moved.ID = "srv-2"
	r.cat.Apply([]mcp.Server{moved})
	eventually(t, "every tool changed", func() bool {
		n := 0
		for _, e := range r.cat.Entries() {
			if e.Skip == mcp.SkipChanged {
				n++
			}
		}
		return n == len(builtins)
	})
	e := r.entry("srv", "echo")
	if e.Override.Trust != mcp.TrustTrusted || e.Local != "mcp_srv_echo" {
		t.Fatalf("echo %+v", e)
	}
	if _, err := r.cat.Override(context.Background(), "srv", "echo", mcp.Patch{Ack: e.Override.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := offeredNames(r.cat); !slices.Equal(got, []string{"mcp_srv_echo"}) {
		t.Fatalf("offered %v", got)
	}
}

func TestCatalogSurvivesAPanickingListing(t *testing.T) {
	r := newCatalog(t)
	// a store without a database panics on its first query
	r.store = mcp.NewStore(db.New(nil))
	r.cat = r.open()
	f := fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	panics := func() int {
		return strings.Count(r.logs.String(), "msg=provider_panic module=mcp-http code=mcp_protocol")
	}
	eventually(t, "the connect listing guarded", func() bool { return panics() >= 1 })
	for n := range 2 {
		before := panics()
		r.cat.Refresh("srv")
		eventually(t, fmt.Sprintf("refresh %d guarded", n), func() bool { return panics() > before })
	}
	if st, _ := r.cat.State("srv"); st.Code != mcp.CodeProtocol || st.FailingSince.IsZero() {
		t.Fatalf("state %+v", st)
	}
}

// an owner's change waits for a listing under way, so the listing's older
// overrides never put back what it revoked
func TestOverrideWaitsForAListing(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	r.listed("srv", "echo")
	unlock := mcp.LockListing(r.cat)
	done := make(chan error, 1)
	off := false
	go func() {
		_, err := r.cat.Override(context.Background(), "srv", "echo", mcp.Patch{AnyTurn: &off})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("override ran during a listing: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCatalogWithdrawsChangedToolsUntilAcknowledged(t *testing.T) {
	r := newCatalog(t)
	ctx := context.Background()
	f := fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	e := r.listed("srv", "echo")
	o := e.Override
	if o.Effects != mcp.EffectsWrite || o.Trust != mcp.TrustUntrusted || o.EnabledDefault || o.AnyTurn || o.Changed ||
		o.Hash != o.AckedHash || o.Local != "mcp_srv_echo" || o.FirstSeen.IsZero() {
		t.Fatalf("defaults %+v", o)
	}
	read, trusted, on := mcp.EffectsRead, mcp.TrustTrusted, true
	if _, err := r.cat.Override(ctx, "srv", "echo", mcp.Patch{Effects: &read, Trust: &trusted, EnabledDefault: &on, AnyTurn: &on}); err != nil {
		t.Fatal(err)
	}
	owned := func(o mcp.Override) bool {
		return o.Effects == mcp.EffectsRead && o.Trust == mcp.TrustTrusted && o.EnabledDefault && o.AnyTurn
	}
	if e := r.entry("srv", "echo"); !owned(e.Override) || e.Skip != "" {
		t.Fatalf("overrides not applied at once: %+v", e)
	}
	newSchema := schema(`{"type":"object","properties":{"text":{"type":"string"},"n":{"type":"integer"}}}`)
	for _, step := range []struct {
		name   string
		change func()
	}{
		{"description", func() { f.Add("echo", "Echoes, and more.") }},
		{"schema", func() { f.AddTool(&sdk.Tool{Name: "echo", Description: "Echoes, and more.", InputSchema: newSchema}) }},
		{"annotations", func() {
			f.AddTool(&sdk.Tool{Name: "echo", Description: "Echoes, and more.", InputSchema: newSchema, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}})
		}},
	} {
		name := step.name
		before := r.entry("srv", "echo").Override.Hash
		step.change()
		r.cat.Refresh("srv")
		eventually(t, name+" withdraws", func() bool { return r.entry("srv", "echo").Skip == mcp.SkipChanged })
		e := r.entry("srv", "echo")
		if !e.Override.Changed || !owned(e.Override) || e.Override.AckedHash != before || slices.Contains(offeredNames(r.cat), "mcp_srv_echo") {
			t.Fatalf("%s: %+v", name, e)
		}
		if _, err := r.cat.Override(ctx, "srv", "echo", mcp.Patch{Ack: before}); !errors.Is(err, mcp.ErrStaleAck) {
			t.Fatalf("%s: a stale ack gave %v", name, err)
		}
		if _, err := r.cat.Override(ctx, "srv", "echo", mcp.Patch{Ack: e.Override.Hash}); err != nil {
			t.Fatal(err)
		}
		if e := r.entry("srv", "echo"); e.Skip != "" || e.Override.Changed || !owned(e.Override) {
			t.Fatalf("%s: acknowledged %+v", name, e)
		}
	}
	if !strings.Contains(r.logs.String(), "msg=mcp_catalog instance=srv tools=5 offered=4 changed=1 skipped=0\n") {
		t.Fatalf("log:\n%s", r.logs)
	}
	bad := "maybe"
	for _, tc := range []struct {
		remote string
		p      mcp.Patch
		err    error
	}{{"nope", mcp.Patch{AnyTurn: &on}, mcp.ErrToolNotFound}, {"echo", mcp.Patch{Effects: &bad}, mcp.ErrBadPatch}, {"echo", mcp.Patch{Trust: &bad}, mcp.ErrBadPatch}} {
		if _, err := r.cat.Override(ctx, "srv", tc.remote, tc.p); !errors.Is(err, tc.err) {
			t.Fatalf("%s %+v: %v", tc.remote, tc.p, err)
		}
	}
	r.private("Echoes, and more.")
}

func TestCatalogRefreshTriggers(t *testing.T) {
	r := newCatalog(t)
	// the SDK may cache a modern listing an hour: every re-list must get past it
	f := fake(t, mcptest.Options{ListTTL: time.Hour})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	r.listed("srv", "echo")
	quiet := func(what string, n int) {
		t.Helper()
		time.Sleep(50 * time.Millisecond)
		if got := f.Count("tools/list"); got != n {
			t.Fatalf("%s listed %d times more", what, got-n)
		}
	}
	f.Add("timed", "Timed.")
	n := f.Count("tools/list")
	r.clock.Advance(mcp.RefreshEvery - time.Second)
	quiet("before 10 minutes", n)
	r.clock.Advance(time.Second)
	r.listed("srv", "timed")
	f.Add("forced", "Forced.")
	if !r.cat.Refresh("srv") || r.cat.Refresh("nope") {
		t.Fatal("refresh of a running or unknown server")
	}
	r.listed("srv", "forced")
	f.Add("unknown", "Unknown.")
	n = f.Count("tools/list")
	r.cat.CallFailed("srv", mcp.CodeToolError)
	quiet("another call failure", n)
	r.cat.CallFailed("srv", mcp.CodeUnknownTool)
	r.listed("srv", "unknown")
	// pokes that come while a re-list waits make one more, not one each
	f.Hold(true)
	n, hits := f.Count("tools/list"), f.Hits()
	r.cat.Refresh("srv")
	eventually(t, "a re-list waiting", func() bool { return f.Hits() > hits })
	r.cat.Refresh("srv")
	r.cat.CallFailed("srv", mcp.CodeUnknownTool)
	r.cat.Refresh("srv")
	f.Hold(false)
	eventually(t, "two re-lists", func() bool { return f.Count("tools/list") == n+2 })
	quiet("merged pokes", n+2)
}

func TestCatalogKeepsAFailingServersToolsTenMinutes(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := newCatalog(t)
			f := fake(t, mcptest.Options{})
			srv := r.server("srv", f)
			r.cat.Apply([]mcp.Server{srv})
			r.listed("srv", "echo")
			t0 := r.clock.Now()
			f.Fail(status)
			hits := f.Hits()
			r.cat.Refresh("srv")
			eventually(t, "the failure recorded", func() bool { st, _ := r.cat.State("srv"); return st.Code == mcp.CodeUnavailable })
			// the session stays up through a busy server: the catalog alone knows
			if st, _ := r.cat.State("srv"); !st.FailingSince.Equal(t0) || st.Withdrawn || srv.Client.Status().State != mcp.StateConnected {
				t.Fatalf("state %+v, client %+v", st, srv.Client.Status())
			}
			// a failed re-list is tried again a minute later
			eventually(t, "the refresh tried", func() bool { return f.Hits() > hits })
			hits = f.Hits()
			r.clock.Advance(mcp.RetryEvery)
			eventually(t, "a retry", func() bool { return f.Hits() > hits })
			r.clock.Advance(mcp.KeepFor - mcp.RetryEvery - time.Second)
			if n := len(r.cat.Offered()); n != 5 {
				t.Fatalf("withdrawn early: %d offered", n)
			}
			r.clock.Advance(time.Second)
			if st, _ := r.cat.State("srv"); !st.Withdrawn || len(r.cat.Offered()) != 0 {
				t.Fatalf("not withdrawn: %+v", st)
			}
			for _, e := range r.cat.Entries() {
				if e.Skip != mcp.SkipUnavailable {
					t.Fatalf("entry %+v", e)
				}
			}
			if !strings.Contains(r.logs.String(), "msg=mcp_tools_withdrawn instance=srv code=mcp_unavailable\n") ||
				!strings.Contains(r.logs.String(), "msg=mcp_refresh_failed instance=srv code=mcp_unavailable\n") {
				t.Fatalf("log:\n%s", r.logs)
			}
			f.Fail(0)
			r.cat.Refresh("srv")
			eventually(t, "back", func() bool { return len(r.cat.Offered()) == 5 })
			if st, _ := r.cat.State("srv"); st.Withdrawn || !st.FailingSince.IsZero() || st.Code != "" || st.ListedAt.IsZero() {
				t.Fatalf("recovered %+v", st)
			}
			r.private()
		})
	}
}

func TestCatalogWithdrawsWhenTheSessionStaysDown(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	srv := r.server("srv", f)
	r.cat.Apply([]mcp.Server{srv})
	r.listed("srv", "echo")
	// the SDK fails the connection: the session breaks and cannot come back
	f.Fail(http.StatusBadRequest)
	_, err := srv.Client.CallTool(bg(), "echo", nil)
	wantCode(t, err, mcp.CodeUnavailable)
	eventually(t, "failing", func() bool { st, _ := r.cat.State("srv"); return !st.FailingSince.IsZero() })
	r.clock.Advance(mcp.KeepFor)
	if len(r.cat.Offered()) != 0 {
		t.Fatalf("offered %v", offeredNames(r.cat))
	}
	f.Fail(0)
	eventually(t, "reconnected and listed", func() bool { return len(r.cat.Offered()) == 5 })
	if st, _ := r.cat.State("srv"); st.Withdrawn || !st.FailingSince.IsZero() {
		t.Fatalf("state %+v", st)
	}
}

func TestCatalogAppliesServersLive(t *testing.T) {
	r := newCatalog(t)
	fa, fb := fake(t, mcptest.Options{}), fake(t, mcptest.Options{Mode: mcptest.Legacy})
	a, b := r.server("a", fa), r.server("b", fb)
	start := time.Now()
	r.cat.Apply([]mcp.Server{a})
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Apply waited %v", d)
	}
	r.listed("a", "echo")
	hits := fa.Hits()
	r.cat.Apply([]mcp.Server{a, b})
	r.listed("b", "echo")
	if fa.Hits() != hits {
		t.Fatal("adding b restarted a")
	}
	r.cat.Apply([]mcp.Server{b})
	if _, ok := r.cat.State("a"); ok || slices.ContainsFunc(r.cat.Entries(), func(e mcp.Entry) bool { return e.Instance == "a" }) {
		t.Fatalf("a still runs: %v", r.cat.Entries())
	}
	hits = fa.Hits()
	time.Sleep(50 * time.Millisecond)
	if fa.Hits() != hits || a.Client.Status().State != mcp.StateUnavailable {
		t.Fatalf("a still talks: %+v", a.Client.Status())
	}
	// an instance deleted and created again under its name
	b2 := b
	b2.ID = "b-2"
	inits := fb.Count("initialize")
	r.cat.Apply([]mcp.Server{b2})
	eventually(t, "b restarted", func() bool { return fb.Count("initialize") > inits })
	r.listed("b", "echo")
	// started again, a reuses its client
	r.cat.Apply([]mcp.Server{a, b2})
	r.listed("a", "echo")
	r.cat.Apply(nil)
	if len(r.cat.Entries()) != 0 {
		t.Fatalf("entries %v", r.cat.Entries())
	}
	for _, line := range []string{
		"msg=mcp_server_started instance=a\n", "msg=mcp_server_stopped instance=a\n",
		"msg=mcp_server_started instance=b\n", "msg=mcp_server_stopped instance=b\n",
	} {
		if strings.Count(r.logs.String(), line) != 2 {
			t.Fatalf("%q not twice:\n%s", line, r.logs)
		}
	}
	fa.Close()
	fb.Close()
	eventually(t, "no mcp goroutine left", func() bool { return leftover() == "" })
	r.private()
}

// a schema's own text reaches the model as a description does: every
// description and title one clean line of at most 200 code points, no
// examples; what validates, the caps and the hash stay the server's (M7)
func TestCatalogSanitizesTheSchemaTextTheModelReads(t *testing.T) {
	r := newCatalog(t)
	f := fake(t, mcptest.Options{})
	long := strings.Repeat("é", 300)
	raw := `{"type":"object","description":"Root\u0007 text.","examples":[{"q":"ex-root"}],"properties":{` +
		`"q":{"type":"string","title":"Q\n\tline","description":"` + long + `","examples":["ex-q"]},` +
		`"examples":{"type":"string","description":"A property named examples."},` +
		`"mode":{"enum":[{"description":"kept\u0007"}],"default":{"title":"kept too\u0007"}},` +
		`"nested":{"type":"object","properties":{"deep":{"type":"string","description":"Deep\u001b[31m red"}}}}}`
	f.AddTool(&sdk.Tool{Name: "q", Description: "Q.", InputSchema: schema(raw)})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	e := r.listed("srv", "q")
	var got struct {
		Description string         `json:"description"`
		Examples    any            `json:"examples"`
		Properties  map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(e.Tool.Input), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"q":        map[string]any{"type": "string", "title": "Q line", "description": strings.Repeat("é", 200)},
		"examples": map[string]any{"type": "string", "description": "A property named examples."},
		"mode":     map[string]any{"enum": []any{map[string]any{"description": "kept\u0007"}}, "default": map[string]any{"title": "kept too\u0007"}},
		"nested":   map[string]any{"type": "object", "properties": map[string]any{"deep": map[string]any{"type": "string", "description": "Deep[31m red"}}},
	}
	if got.Description != "Root text." || got.Examples != nil || !reflect.DeepEqual(got.Properties, want) {
		t.Fatalf("schema the model reads %s", e.Tool.Input)
	}
	if verr := agent.Validate(e.Tool, json.RawMessage(`{"q":5}`)); verr == nil || verr.Code != "invalid_arguments" {
		t.Fatalf("validated %v", verr)
	}
	if h := mcp.ToolHash(f.URL(), mcp.RemoteTool{Name: "q", Description: "Q.", InputSchema: schema(raw)}); e.Override.Hash != h {
		t.Fatalf("hash %s, want the server's %s", e.Override.Hash, h)
	}
	r.private("ex-root", "ex-q", "Deep", long)
}
