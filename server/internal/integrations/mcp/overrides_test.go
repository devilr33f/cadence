package mcp_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp/mcptest"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

func (r *catRig) rows(instance string) []string {
	r.t.Helper()
	rows, err := db.New(r.pool).ListMCPTools(context.Background(), instance)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		out = append(out, row.RemoteName)
	}
	return out
}

// age moves a tool's last listing d into the past.
func (r *catRig) age(instance, remote string, d time.Duration) {
	r.t.Helper()
	_, err := r.pool.Exec(context.Background(), `UPDATE mcp_tools SET last_seen = now() - make_interval(secs => $3) WHERE instance = $1 AND remote_name = $2`,
		instance, remote, d.Seconds())
	if err != nil {
		r.t.Fatal(err)
	}
}

func TestGoneToolsKeepTheirOverridesThenArePruned(t *testing.T) {
	r := newCatalog(t)
	ctx := context.Background()
	q := db.New(r.pool)
	if _, err := q.CreateProviderInstance(ctx, db.CreateProviderInstanceParams{Name: "srv", Module: mcp.Module.ID, Config: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	f := fake(t, mcptest.Options{})
	r.cat.Apply([]mcp.Server{r.server("srv", f)})
	r.listed("srv", "fail")
	on := true
	if _, err := r.cat.Override(ctx, "srv", "fail", mcp.Patch{EnabledDefault: &on}); err != nil {
		t.Fatal(err)
	}
	f.Remove("fail", "slow")
	r.cat.Refresh("srv")
	eventually(t, "gone from the catalog", func() bool { return len(r.cat.Entries()) == 3 })
	if got := r.rows("srv"); !slices.Equal(got, builtins) {
		t.Fatalf("rows %v", got)
	}
	// back within the retention: its overrides and its name are as they were
	f.Add("fail", "Fails.")
	r.cat.Refresh("srv")
	e := r.listed("srv", "fail")
	if !e.Override.EnabledDefault || e.Override.Changed || e.Local != "mcp_srv_fail" {
		t.Fatalf("back %+v", e)
	}
	// an instance that exists keeps every row however long its tool is out
	// of the listing, or its server down or switched off (Ruling M5d-R11)
	r.age("srv", "slow", 40*24*time.Hour)
	prune := func(want int64) {
		t.Helper()
		if n, err := r.store.Prune(ctx, mcp.Retention); err != nil || n != want {
			t.Fatalf("pruned %d, want %d: %v", n, want, err)
		}
	}
	prune(0)
	for _, remote := range builtins {
		r.age("srv", remote, 40*24*time.Hour)
	}
	prune(0)
	if got := r.rows("srv"); !slices.Equal(got, builtins) {
		t.Fatalf("rows of srv %v", got)
	}
	// a returning tool is compared with its last acknowledged hash
	f.AddTool(&sdk.Tool{Name: "slow", Description: "Waits for longer.", InputSchema: schema(anyObject)})
	r.cat.Refresh("srv")
	eventually(t, "slow back changed", func() bool { return r.entries()["srv/slow"].Override.Changed })
	// an instance gone 30 days loses its rows; one gone less, or deleted and
	// created again meanwhile, keeps them
	hash := strings.Repeat("a", 64)
	if _, err := q.SeeMCPTools(ctx, db.SeeMCPToolsParams{
		Instance: "old", RemoteNames: []string{"a", "b"}, LocalNames: []string{"mcp_old_a", "mcp_old_b"}, SchemaHashes: []string{hash, hash},
	}); err != nil {
		t.Fatal(err)
	}
	r.age("old", "a", 40*24*time.Hour)
	if n, err := q.DeleteProviderInstance(ctx, "srv"); err != nil || n != 1 {
		t.Fatalf("delete srv: %d %v", n, err)
	}
	prune(0)
	r.gone("old", mcp.Retention-time.Hour)
	r.gone("srv", mcp.Retention+time.Hour)
	if _, err := q.CreateProviderInstance(ctx, db.CreateProviderInstanceParams{Name: "srv", Module: mcp.Module.ID, Config: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	prune(0)
	r.gone("old", mcp.Retention+time.Hour)
	stop := r.store.RunRetention(ctx, slog.New(slog.NewTextHandler(r.logs, nil)), time.Hour)
	eventually(t, "pruned at start", func() bool { return strings.Contains(r.logs.String(), "msg=mcp_tools_pruned count=2\n") })
	stop()
	if got := r.rows("old"); len(got) != 0 {
		t.Fatalf("rows of a gone instance %v", got)
	}
	if got := r.rows("srv"); !slices.Equal(got, builtins) {
		t.Fatalf("rows of srv %v", got)
	}
	r.private()
}

// gone moves the time an instance's rows were found gone d into the past.
func (r *catRig) gone(instance string, d time.Duration) {
	r.t.Helper()
	tag, err := r.pool.Exec(context.Background(), `UPDATE mcp_tools SET gone_since = now() - make_interval(secs => $2) WHERE instance = $1 AND gone_since IS NOT NULL`,
		instance, d.Seconds())
	if err != nil || tag.RowsAffected() == 0 {
		r.t.Fatalf("no row of %s found gone: %v", instance, err)
	}
}
