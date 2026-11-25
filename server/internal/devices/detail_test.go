package devices_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}

// the detail carries each capability's descriptor as the device reported it:
// its schemas parsed from the stored strings, null where it declared none
func TestDetailCarriesTheDescriptors(t *testing.T) {
	e := newEnv(t)
	player := &cadencev2.Descriptor{
		Id: "media.player", Version: 2, ConfigSchema: levelSchema, StateSchema: stateSchema,
		Commands: map[string]string{"seek": seekSchema, "pause": ""},
		Secrets:  []*cadencev2.SecretSpec{{Name: "token", Description: "the player's token"}, {Name: "pin"}},
	}
	// a boolean schema is no object: the dashboard has nothing to draw a form from
	bare := &cadencev2.Descriptor{Id: "bare", Version: 1, StateSchema: "true"}
	e.sendReport(t, report(player, bare))
	code, body := call(t, e.app(), http.MethodGet, "/api/v1/devices/"+e.id, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		Capabilities []struct {
			ID           string
			ConfigSchema json.RawMessage
			StateSchema  json.RawMessage
			Commands     []struct {
				Name       string
				ArgsSchema json.RawMessage
			}
			SecretSpecs []map[string]string
			Secrets     []struct {
				Name string
				Set  bool
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Capabilities) != 2 {
		t.Fatalf("%v %s", err, body)
	}
	b, p := got.Capabilities[0], got.Capabilities[1]
	if b.ID != "bare" || string(b.ConfigSchema) != "null" || string(b.StateSchema) != "null" || len(b.Commands) != 0 ||
		len(b.SecretSpecs) != 0 || len(b.Secrets) != 0 {
		t.Fatalf("bare %s", body)
	}
	if p.ID != "media.player" || !sameJSON(t, p.ConfigSchema, levelSchema) || !sameJSON(t, p.StateSchema, stateSchema) {
		t.Fatalf("player schemas %s", body)
	}
	if len(p.Commands) != 2 || p.Commands[0].Name != "pause" || string(p.Commands[0].ArgsSchema) != "null" ||
		p.Commands[1].Name != "seek" || !sameJSON(t, p.Commands[1].ArgsSchema, seekSchema) {
		t.Fatalf("player commands %s", body)
	}
	if !strings.Contains(body, `"secretSpecs":[{"name":"token","description":"the player's token"},{"name":"pin"}]`) {
		t.Fatalf("secret specs %s", body)
	}
	if len(p.Secrets) != 2 || p.Secrets[0].Name != "token" || p.Secrets[0].Set || p.Secrets[1].Name != "pin" {
		t.Fatalf("secrets %s", body)
	}
}
