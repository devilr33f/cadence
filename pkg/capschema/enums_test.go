package capschema_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// names lists an enum's values in lower case without its prefix, the unspecified one and skip left out
func names(values map[int32]string, prefix string, skip ...string) []string {
	var out []string
	for n, name := range values {
		v := strings.ToLower(strings.TrimPrefix(name, prefix))
		if n == 0 || slices.Contains(skip, v) {
			continue
		}
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func enumOf(t *testing.T, schema string, path ...string) []string {
	t.Helper()
	var node map[string]any
	if err := json.Unmarshal([]byte(schema), &node); err != nil {
		t.Fatal(err)
	}
	for _, p := range path {
		node = node["properties"].(map[string]any)[p].(map[string]any)
	}
	var out []string
	for _, v := range node["enum"].([]any) {
		out = append(out, v.(string))
	}
	slices.Sort(out)
	return out
}

// display.screen's route is screen/v1's route, less v2's boot and suspend
func TestRouteMatchesProto(t *testing.T) {
	want := names(cadencev2.ScreenRoute_name, "SCREEN_ROUTE_", "boot", "suspend")
	if got := enumOf(t, capschema.DisplayScreenState, "route"); !slices.Equal(got, want) {
		t.Fatalf("route = %v, want %v", got, want)
	}
}

func TestKeysMatchProto(t *testing.T) {
	want := names(screenv1.Key_name, "KEY_")
	got := slices.Sorted(slices.Values(capschema.Keys))
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if got := enumOf(t, capschema.InputRemoteCommands["key"], "key"); !slices.Equal(got, want) {
		t.Fatalf("key command = %v, want %v", got, want)
	}
}
