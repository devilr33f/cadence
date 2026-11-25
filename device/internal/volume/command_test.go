package volume_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type sessions map[string]cadencev2.Trigger

func (s sessions) Trigger(id string) (cadencev2.Trigger, bool) {
	t, ok := s[id]
	return t, ok
}

var known = sessions{
	"wake": cadencev2.Trigger_TRIGGER_WAKE, "press": cadencev2.Trigger_TRIGGER_BUTTON,
	"follow": cadencev2.Trigger_TRIGGER_FOLLOW_UP, "none": cadencev2.Trigger_TRIGGER_UNSPECIFIED,
	"event": cadencev2.Trigger_TRIGGER_EVENT,
}

func at(t *testing.T, level int) *volume.Setter {
	t.Helper()
	s := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	if _, err := s.Set(level, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	return s
}

func command(s *volume.Setter, name, args string) (string, error) {
	res, err := volume.Command(context.Background(), s, known, name, json.RawMessage(args))
	return string(res), err
}

func code(err error) string {
	var ce *capability.CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return fmt.Sprint(err)
}

func TestCommandResults(t *testing.T) {
	s := at(t, 50)
	r := follow(s)
	for _, c := range []struct{ name, args, want string }{
		{"set_level", `{"level":30}`, `{"level":30}`},
		{"change_level", `{"delta":-10}`, `{"level":20}`},
		{"change_level", `{"delta":100}`, `{"level":100}`},
		{"change_level", `{"delta":-100}`, `{"level":0}`},
		{"set_level", `{"level":100}`, `{"level":100}`},
		{"change_level", `{"delta":5}`, `{"level":100}`},
	} {
		if got, err := command(s, c.name, c.args); err != nil || got != c.want {
			t.Fatalf("%s %s = %s, %v", c.name, c.args, got, err)
		}
	}
	if len(r.heard) == 0 || r.heard[0] != (change{30, volume.OriginCommand}) {
		t.Fatalf("heard %v", r.heard)
	}
}

func TestCommandRefusesInvalidArgs(t *testing.T) {
	s := at(t, 50)
	long := strings.Repeat("a", 64)
	for _, c := range []struct{ name, args string }{
		{"set_level", `{}`}, {"set_level", `{"level":101}`}, {"set_level", `{"level":-1}`}, {"set_level", `{"level":1.5}`},
		{"set_level", `{"level":"30"}`}, {"set_level", `{"level":30,"x":1}`}, {"set_level", `{"delta":5}`},
		{"set_level", `{"level":30,"sessionId":""}`}, {"set_level", `{"level":30,"sessionId":"` + long + `"}`},
		{"set_level", `{"level":30,"sessionId":7}`}, {"set_level", `nope`}, {"set_level", `{"level":30} {}`},
		// an explicit null is no session id, never the api call a missing one is
		{"set_level", `{"level":60,"sessionId":null}`}, {"change_level", `{"delta":5,"sessionId":null}`},
		{"set_level", `{"level":null}`}, {"set_level", `{"level":30,"delta":null}`}, {"change_level", `{"delta":5,"level":null}`},
		{"change_level", `{}`}, {"change_level", `{"delta":101}`}, {"change_level", `{"delta":-101}`}, {"change_level", `{"level":5}`},
	} {
		if _, err := command(s, c.name, c.args); code(err) != "invalid" {
			t.Fatalf("%s %s: %v", c.name, c.args, err)
		}
	}
	if s.Level() != 50 {
		t.Fatalf("level moved to %d", s.Level())
	}
	if _, err := command(s, "mute", `{}`); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("unknown command: %v", err)
	}
}

// rule 7: a raise in a session needs one the user began with the wake word or a press
func TestCommandRaiseNeedsWokenSession(t *testing.T) {
	for _, c := range []struct {
		name, args string
		want       string
	}{
		{"set_level", `{"level":60,"sessionId":"follow"}`, "wake_required"},
		{"change_level", `{"delta":1,"sessionId":"follow"}`, "wake_required"},
		{"set_level", `{"level":60,"sessionId":"none"}`, "wake_required"},
		{"set_level", `{"level":60,"sessionId":"event"}`, "wake_required"},
		{"set_level", `{"level":60,"sessionId":"gone"}`, "wake_required"},
		{"set_level", `{"level":60,"sessionId":"wake"}`, ""},
		{"change_level", `{"delta":10,"sessionId":"press"}`, ""},
		// no session: an authenticated api call
		{"set_level", `{"level":60}`, ""},
		{"change_level", `{"delta":10}`, ""},
		// lower or equal: always
		{"set_level", `{"level":40,"sessionId":"follow"}`, ""},
		{"change_level", `{"delta":-10,"sessionId":"gone"}`, ""},
		{"set_level", `{"level":50,"sessionId":"follow"}`, ""},
		{"change_level", `{"delta":0,"sessionId":"none"}`, ""},
	} {
		s := at(t, 50)
		_, err := command(s, c.name, c.args)
		if c.want == "" && err != nil || c.want != "" && code(err) != c.want {
			t.Fatalf("%s %s: %v", c.name, c.args, err)
		}
		if c.want != "" && s.Level() != 50 {
			t.Fatalf("%s %s: refused, but the level moved to %d", c.name, c.args, s.Level())
		}
	}
	// a board that keeps no sessions knows none of them
	s := at(t, 50)
	if _, err := volume.Command(context.Background(), s, nil, "set_level", json.RawMessage(`{"level":60,"sessionId":"wake"}`)); code(err) != "wake_required" {
		t.Fatalf("no sessions: %v", err)
	}
}

// the raise is judged on the level the change resolves against, at the top of the scale too
func TestCommandRaiseFromTheCurrentLevel(t *testing.T) {
	s := at(t, 100)
	if got, err := command(s, "change_level", `{"delta":5,"sessionId":"follow"}`); err != nil || got != `{"level":100}` {
		t.Fatalf("clamped change = %s, %v", got, err)
	}
}

func TestCommandStoreFailure(t *testing.T) {
	s := volume.NewSetter(volume.NewStore(blockedDir(t)), 50, nil)
	if _, err := command(s, "set_level", `{"level":30}`); code(err) != "store_failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrentChangesNeverLoseAnUpdate(t *testing.T) {
	s := at(t, 0)
	var wg sync.WaitGroup
	for range 60 {
		wg.Go(func() {
			if _, err := command(s, "change_level", `{"delta":1}`); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := s.Level(); got != 60 {
		t.Fatalf("level = %d", got)
	}
}

func TestCommandSchemas(t *testing.T) {
	cmds := volume.Commands()
	required := map[string]string{"set_level": "level", "change_level": "delta"}
	if len(cmds) != len(required) {
		t.Fatalf("commands %v", cmds)
	}
	for name, field := range required {
		var sch struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties *bool                      `json:"additionalProperties"`
		}
		if err := json.Unmarshal([]byte(cmds[name]), &sch); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sch.Type != "object" || sch.AdditionalProperties == nil || *sch.AdditionalProperties ||
			len(sch.Required) != 1 || sch.Required[0] != field || len(sch.Properties) != 2 || sch.Properties["sessionId"] == nil {
			t.Fatalf("%s schema: %s", name, cmds[name])
		}
	}
	cmds["set_level"] = "changed"
	if volume.Commands()["set_level"] == "changed" {
		t.Fatal("Commands shares its map")
	}
}

// a session of another device the server vouched for in this command raises the level as a woken one of this
// device does; one it vouched for is never another session, and a lowering needs none
func TestVouchedSessionRaises(t *testing.T) {
	woken := capability.WithVouched(context.Background(), capability.Vouched{
		CallerDeviceID: "dev-a", SessionID: "remote", Trigger: cadencev2.Trigger_TRIGGER_WAKE,
	})
	s := at(t, 30)
	if res, err := volume.Command(woken, s, known, "set_level", json.RawMessage(`{"level":60,"sessionId":"remote"}`)); err != nil || string(res) != `{"level":60}` {
		t.Fatalf("vouched raise = %s %v", res, err)
	}
	s = at(t, 30)
	if _, err := volume.Command(woken, s, known, "change_level", json.RawMessage(`{"delta":10,"sessionId":"other"}`)); code(err) != "wake_required" {
		t.Fatalf("raise for another session = %v, want wake_required", err)
	}
	if _, err := volume.Command(context.Background(), s, known, "change_level", json.RawMessage(`{"delta":10,"sessionId":"remote"}`)); code(err) != "wake_required" {
		t.Fatalf("raise without the grant = %v, want wake_required", err)
	}
	if res, err := volume.Command(context.Background(), s, known, "set_level", json.RawMessage(`{"level":10,"sessionId":"remote"}`)); err != nil || string(res) != `{"level":10}` {
		t.Fatalf("lowering without a grant = %s %v", res, err)
	}
}
