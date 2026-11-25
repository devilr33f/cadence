package t3code

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// AdminDeps are what the status route reads.
type AdminDeps struct {
	Source *Source
	// Listed reads the t3code setting's names now
	Listed func(ctx context.Context) ([]string, error)
	// Disabled is why a listed name does not run ("" when it runs or is
	// starting)
	Disabled func(name string) string
	Log      *slog.Logger
}

// InstanceState is one instance the t3code setting names: counts and codes,
// never a title, a question or the token.
type InstanceState struct {
	Instance  string `json:"instance"`
	Running   bool   `json:"running" doc:"the module follows it"`
	Connected bool   `json:"connected"`
	Synced    bool   `json:"synced" doc:"its shell snapshot arrived, so its threads are known"`
	LastError string `json:"lastError,omitempty" doc:"the latest connection failure's code, kept across reconnects"`
	Disabled  string `json:"disabled,omitempty" doc:"why a named instance does not run: instance_not_found, instance_wrong_kind, instance_unknown_module, instance_build_failed"`
	Threads   int    `json:"threads"`
	Pending   int    `json:"pending" doc:"threads waiting on a question or an approval"`
	Followed  int    `json:"followed" doc:"threads followed in detail for a subscription"`
}

// StatusReport is the t3code setting's instances, in its order.
type StatusReport struct {
	Instances []InstanceState `json:"instances" nullable:"false"`
}

type statusOut struct{ Body StatusReport }

var docs = map[string]api.Doc{
	"get-t3code-status": {Summary: "Get the T3 Code instances the t3code setting names, with their connection and counts"},
}

// RegisterAdmin mounts GET /t3code/status, admin's alone.
func RegisterAdmin(a huma.API, d AdminDeps) {
	defer api.Describe(a, docs)
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	huma.Register(a, huma.Operation{
		OperationID: "get-t3code-status", Method: http.MethodGet, Path: "/t3code/status", Tags: []string{"t3code"},
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		names, err := d.Listed(ctx)
		if err != nil {
			if ctx.Err() == nil {
				d.Log.Error("t3code_api_failed", "op", "status")
			}
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		out := StatusReport{Instances: []InstanceState{}}
		for _, name := range names {
			st := InstanceState{Instance: name}
			if s, ok := d.Source.Status(name); ok {
				st.Running, st.Connected, st.Synced, st.LastError = true, s.Connected, s.Synced, s.LastError
				st.Threads, st.Pending, st.Followed = s.Threads, s.Pending, s.Followed
			} else if d.Disabled != nil {
				st.Disabled = d.Disabled(name)
			}
			out.Instances = append(out.Instances, st)
		}
		return &statusOut{Body: out}, nil
	})
}
