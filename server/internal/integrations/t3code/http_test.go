package t3code_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

// the status lists the setting's names in its order: a running one's
// connection and counts, a left-out one's code; never a title; admin only
func TestStatusRoute(t *testing.T) {
	srv := httptest.NewServer(t3codetest.New(t3codetest.Config{Token: testToken}))
	t.Cleanup(srv.Close)
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: testToken, HTTP: srv.Client(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	src := t3code.NewSource(t3code.SourceDeps{})
	src.Apply([]t3code.Want{{Instance: "work", Client: c}})
	t.Cleanup(src.Close)
	waitFor(t, "synced", func() bool { st, _ := src.Status("work"); return st.Synced })
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: func(_ context.Context, b string) (auth.Grants, error) {
			switch b {
			case "tok-admin":
				return auth.Grants{Scopes: auth.AdminScopes}, nil
			case "tok-ha":
				return auth.Grants{Scopes: auth.Scopes{auth.ScopeHA}}, nil
			}
			return auth.Grants{}, auth.ErrUnauthorized
		},
		Admin: []func(huma.API){func(a huma.API) {
			t3code.RegisterAdmin(a, t3code.AdminDeps{
				Source: src,
				Listed: func(context.Context) ([]string, error) { return []string{"work", "gone"}, nil },
				Disabled: func(name string) string {
					if name == "gone" {
						return "instance_not_found"
					}
					return ""
				},
			})
		}},
	})
	if st, _ := apitest.Call(t, app, "tok-ha", "GET", "/api/v1/t3code/status", ""); st != 403 {
		t.Fatalf("ha token: %d", st)
	}
	want := `{"instances":[{"instance":"work","running":true,"connected":true,"synced":true,"threads":5,"pending":0,"followed":0},` +
		`{"instance":"gone","running":false,"connected":false,"synced":false,"disabled":"instance_not_found","threads":0,"pending":0,"followed":0}]}`
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, b := apitest.Call(t, app, "tok-admin", "GET", "/api/v1/t3code/status", "")
		if st == 200 && b == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status: %d %s", st, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, b := apitest.Call(t, app, "tok-admin", "GET", "/api/v1/t3code/status", ""); strings.Contains(b, "ask") {
		t.Fatalf("a title in %s", b)
	}
}
