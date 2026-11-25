package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// ModuleSecret is one secret a module takes, by name: never a value.
type ModuleSecret struct {
	Name     string `json:"name"`
	Required bool   `json:"required" doc:"An instance must set it; else it may be left out."`
}

// ModuleInfo is one module an instance can run.
type ModuleInfo struct {
	ID           string         `json:"id"`
	Kind         Kind           `json:"kind"`
	ConfigSchema SchemaObject   `json:"configSchema" doc:"The JSON Schema an instance's config must pass, its keys in the module's order."`
	Secrets      []ModuleSecret `json:"secrets" nullable:"false"`
}

// SchemaObject is a module's JSON Schema, a JSON object sent compact as the
// module wrote it: never through a map, so a form built from it keeps the
// module's order of properties.
type SchemaObject json.RawMessage

func (s SchemaObject) MarshalJSON() ([]byte, error) { return s, nil }

func (SchemaObject) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: &huma.Schema{}}
}

// schemaObject is src compact, or false when it is no JSON object.
func schemaObject(src string) (SchemaObject, bool) {
	var b bytes.Buffer
	if json.Compact(&b, []byte(src)) != nil || b.Len() == 0 || b.Bytes()[0] != '{' {
		return nil, false
	}
	return SchemaObject(b.Bytes()), true
}

type modulesOut struct{ Body []ModuleInfo }

func registerCatalog(a huma.API, s *Instances, log *slog.Logger) {
	huma.Register(a, huma.Operation{
		OperationID: "list-provider-modules", Method: http.MethodGet, Path: "/providers/" + catalogSegment, Tags: []string{tag},
		Description: "Sorted by kind, then id. The dev fakes are listed only on a server that runs them.",
	}, func(context.Context, *struct{}) (*modulesOut, error) {
		mods := s.Modules()
		out := make([]ModuleInfo, 0, len(mods))
		for _, m := range mods {
			info := ModuleInfo{ID: m.ID, Kind: m.Kind, Secrets: make([]ModuleSecret, 0, len(m.Secrets)+len(m.OptionalSecrets))}
			var ok bool
			if info.ConfigSchema, ok = schemaObject(m.ConfigSchema); !ok {
				log.Error("provider_catalog_failed", "module", m.ID)
				return nil, api.Error(http.StatusInternalServerError, "internal")
			}
			for _, name := range m.Secrets {
				info.Secrets = append(info.Secrets, ModuleSecret{Name: name, Required: true})
			}
			for _, name := range m.OptionalSecrets {
				info.Secrets = append(info.Secrets, ModuleSecret{Name: name})
			}
			out = append(out, info)
		}
		return &modulesOut{Body: out}, nil
	})
}
