package app

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
)

type nopPinger struct{}

func (nopPinger) Ping(context.Context) error { return nil }

// every admin operation registers beside the others (no duplicate id, route or
// schema name), with a kebab-case id, its area's tag (M7a-D2) and the scope a
// token needs (M7a-D6: admin unless widened; login and the session state need
// none), and errors are documented as the one model (M7a-D1)
func TestAdminOperations(t *testing.T) {
	app := api.NewApp(api.Options{
		DB: nopPinger{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       adminOps(adminDeps{}),
	})
	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string   `json:"operationId"`
			Tags        []string `json:"tags"`
			Scope       string   `json:"x-cadence-scope"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	kebab := regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*$`)
	got := map[string]string{}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			if !kebab.MatchString(op.OperationID) || len(op.Tags) != 1 {
				t.Errorf("%s %s: id %q tags %v", method, path, op.OperationID, op.Tags)
				continue
			}
			got[op.OperationID] = op.Tags[0] + " " + op.Scope
		}
	}
	// "<tag> <scope>"
	want := map[string]string{
		"acknowledge-mcp-tools":       "mcp admin",
		"act-catalog-item":            "catalog admin",
		"announce-to-device":          "voice ha",
		"apply-group-calibration":     "groups admin",
		"approve-claim":               "claims admin",
		"approve-memory":              "memory admin",
		"approve-task":                "tasks admin",
		"browse-catalog":              "catalog ha",
		"calibrate-group":             "groups admin",
		"cancel-task":                 "tasks admin",
		"compact-device-conversation": "conversations admin",
		"configure-device-capability": "devices admin",
		"create-device":               "enroll admin",
		"create-group":                "groups admin",
		"create-memory":               "memory admin",
		"create-provider":             "providers admin",
		"create-session":              "session ",
		"create-subscription":         "subscriptions admin",
		"create-token":                "tokens admin",
		"decline-claim":               "claims admin",
		"delete-busy":                 "presence presence",
		"delete-device":               "devices admin",
		"delete-device-access":        "ops admin",
		"delete-device-artifact":      "ops admin",
		"delete-device-secret":        "devices admin",
		"delete-group":                "groups admin",
		"delete-memory":               "memory admin",
		"delete-mode":                 "modes admin",
		"delete-play-history":         "history admin",
		"delete-provider":             "providers admin",
		"delete-session":              "session admin",
		"delete-skill":                "skills admin",
		"delete-subscription":         "subscriptions admin",
		"delete-token":                "tokens admin",
		"delete-wake-word":            "wakewords admin",
		"delete-wake-word-dataset":    "wakewords admin",
		"edit-memory":                 "memory admin",
		"fetch-wake-word-negatives":   "wakewords admin",
		"get-busy":                    "presence presence",
		"get-catalog-alternatives":    "catalog ha",
		"get-catalog-image":           "catalog ha",
		"get-catalog-item":            "catalog ha",
		"get-catalog-options":         "catalog ha",
		"get-catalog-panel":           "catalog admin",
		"get-claim":                   "claims admin",
		"get-device":                  "devices ha",
		"get-device-artwork":          "media ha",
		"get-device-catalog-session":  "catalog ha",
		"get-device-media":            "media ha",
		"get-device-music":            "media admin",
		"get-device-ops-run":          "ops admin",
		"get-device-processes":        "telemetry admin",
		"get-device-settings":         "settings admin",
		"get-device-turn":             "conversations admin",
		"get-device-video":            "media admin",
		"get-device-vitals":           "telemetry ha",
		"get-global-settings":         "settings admin",
		"get-group":                   "groups ha",
		"get-homeassistant":           "homeassistant admin",
		"get-mode":                    "modes admin",
		"get-session":                 "session ",
		"get-skill":                   "skills admin",
		"get-subscription-inbox":      "subscriptions admin",
		"get-t3code-status":           "t3code admin",
		"get-task":                    "tasks admin",
		"get-trace":                   "tracing admin",
		"get-trace-stats":             "tracing admin",
		"get-wake-word":               "wakewords admin",
		"get-wake-word-model":         "wakewords admin",
		"get-wake-word-negatives":     "wakewords admin",
		"inspect-task":                "tasks admin",
		"join-device-group":           "groups ha",
		"jump-device-music":           "media admin",
		"leave-device-group":          "groups ha",
		"list-boards":                 "enroll admin",
		"list-catalog-sources":        "catalog ha",
		"list-claims":                 "claims admin",
		"list-device-access":          "ops admin",
		"list-device-artifacts":       "ops admin",
		"list-device-compactions":     "memory admin",
		"list-device-learning":        "memory admin",
		"list-device-metrics":         "telemetry admin",
		"list-device-ops":             "ops admin",
		"list-device-turns":           "conversations admin",
		"list-devices":                "enroll ha",
		"list-groups":                 "groups ha",
		"list-homeassistant-areas":    "homeassistant admin",
		"list-homeassistant-entities": "homeassistant admin",
		"list-homeassistant-usage":    "homeassistant admin",
		"list-logs":                   "telemetry admin",
		"list-mcp-endpoint-tools":     "mcpserver admin",
		"list-mcp-servers":            "mcp admin",
		"list-mcp-tools":              "mcp admin",
		"list-memories":               "memory admin",
		"list-modes":                  "modes admin",
		"list-music-favorites":        "history admin",
		"list-play-history":           "history admin",
		"list-provider-modules":       "providers admin",
		"list-providers":              "providers admin",
		"list-skills":                 "skills admin",
		"list-subscription-topics":    "subscriptions admin",
		"list-subscriptions":          "subscriptions admin",
		"list-task-kinds":             "tasks admin",
		"list-tasks":                  "tasks admin",
		"list-tokens":                 "tokens admin",
		"list-traces":                 "tracing admin",
		"list-wake-words":             "wakewords admin",
		"patch-mcp-tool":              "mcp admin",
		"play-catalog":                "catalog ha",
		"play-device-video":           "media admin",
		"probe-device-access":         "ops admin",
		"put-busy":                    "presence presence",
		"put-device-access":           "ops admin",
		"put-device-config":           "devices admin",
		"put-device-secret":           "devices admin",
		"put-device-settings":         "settings admin",
		"put-global-settings":         "settings admin",
		"put-mode":                    "modes admin",
		"put-skill":                   "skills admin",
		"rate-music-song":             "history admin",
		"refresh-mcp-server":          "mcp admin",
		"reject-memory":               "memory admin",
		"rename-device":               "devices admin",
		"rename-wake-word":            "wakewords admin",
		"reply-to-subscription":       "subscriptions admin",
		"reply-to-task":               "tasks admin",
		"reset-device-conversation":   "conversations admin",
		"retrain-wake-word":           "wakewords admin",
		"rollback-device-ops":         "ops admin",
		"search-catalog":              "catalog ha",
		"select-device-catalog":       "catalog ha",
		"send-device-command":         "commands ha",
		"set-device-music-modes":      "media admin",
		"set-device-room":             "devices admin",
		"start-assistant-turn":        "conversations admin",
		"start-device-ops":            "ops admin",
		"test-provider":               "providers admin",
		"token-tools":                 "tokens admin",
		"train-wake-word":             "wakewords admin",
		"update-device-video":         "media admin",
		"update-group":                "groups admin",
		"update-provider":             "providers admin",
		"upload-wake-word-model":      "wakewords admin",
	}
	if len(got) != len(want) {
		t.Errorf("%d operations, want %d", len(got), len(want))
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	model := doc.Components.Schemas["ErrorBody"]
	props := make([]string, 0, len(model.Properties))
	for p := range model.Properties {
		props = append(props, p)
	}
	slices.Sort(props)
	if !slices.Equal(props, []string{"detail", "error", "path"}) || !slices.Equal(model.Required, []string{"error"}) {
		t.Fatalf("error model %v required %v", props, model.Required)
	}
}

// the live feed's websocket upgrade and the Prometheus exposition are the only
// admin routes that are no huma operation (M7a-D2), both plain routes behind
// their scopes (adminPlain): no package mounts routes on the admin group
func TestNoPlainAdminMounts(t *testing.T) {
	var found []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(src), "AdminRoutes") || strings.Contains(string(src), "MountAdmin") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("plain admin mounts remain: %v", found)
	}
}
