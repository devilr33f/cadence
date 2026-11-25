package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

type devs struct{}

func (devs) Canonical(_ context.Context, id string) (string, error) {
	if id == "dev-1" || id == "Kitchen" {
		return "dev-1", nil
	}
	return "", devices.ErrNotFound
}

type rig struct {
	svc  *Service
	app  *fiber.App
	bus  *events.Bus
	feed <-chan events.Event

	mu        sync.Mutex
	turns     []agent.Turn
	announced []string
	// hold blocks a turn until it is closed
	hold   chan struct{}
	result agent.Result
	spoken string
	code   string
}

func newRig(t *testing.T) *rig {
	r := &rig{bus: events.NewBus(64), hold: make(chan struct{}), spoken: "It is sunny."}
	close(r.hold)
	var stop func()
	r.feed, stop = r.bus.Subscribe(nil)
	t.Cleanup(stop)
	r.svc = New(context.Background(), Deps{
		Run: func(_ context.Context, tr agent.Turn) (agent.Result, string) {
			r.mu.Lock()
			r.turns = append(r.turns, tr)
			hold := r.hold
			r.mu.Unlock()
			<-hold
			return r.result, r.spoken
		},
		Announce: func(_ context.Context, dev, text, lang string) string {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.announced = append(r.announced, dev+"|"+text+"|"+lang)
			return r.code
		},
		Bus: r.bus,
	})
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { RegisterAdmin(a, r.svc, devs{}) }},
	})
	return r
}

func (r *rig) post(t *testing.T, bearer, path, body string) (int, map[string]string) {
	t.Helper()
	status, raw := apitest.Call(t, r.app, bearer, "POST", path, body)
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return status, out
}

// a typed turn starts at once, runs as a press on the canonical device and
// is answered by its id
func TestStartRunsAWokenTurn(t *testing.T) {
	r := newRig(t)
	status, body := r.post(t, "admin", "/api/v1/devices/Kitchen/assistant", `{"text":"  what's the weather? ","language":"en"}`)
	if status != 202 || !strings.HasPrefix(body["turnId"], "dash_") {
		t.Fatalf("%d %v", status, body)
	}
	r.svc.Wait()
	if len(r.turns) != 1 {
		t.Fatalf("turns %v", r.turns)
	}
	tr := r.turns[0]
	if tr.ID != body["turnId"] || tr.DeviceID != "dev-1" || tr.Trigger != cadencev2.Trigger_TRIGGER_BUTTON ||
		tr.Text != "what's the weather?" || tr.Language != "en" {
		t.Fatalf("turn %+v", tr)
	}
	if len(r.announced) != 0 {
		t.Fatalf("spoke without speak: %v", r.announced)
	}
}

// with speak, the reply plays on the device and its code reaches the feed
func TestSpeakAnnouncesTheReply(t *testing.T) {
	r := newRig(t)
	status, body := r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"weather","speak":true}`)
	if status != 202 {
		t.Fatalf("%d %v", status, body)
	}
	r.svc.Wait()
	if len(r.announced) != 1 || r.announced[0] != "dev-1|It is sunny.|" {
		t.Fatalf("announced %v", r.announced)
	}
	select {
	case e := <-r.feed:
		if e.Type != events.AssistantSpeech || e.TurnID != body["turnId"] || e.Data.(map[string]any)["code"] != "ok" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no assistant_speech")
	}
}

// a failed or silent turn speaks nothing
func TestSpeakSkipsFailedAndSilentTurns(t *testing.T) {
	r := newRig(t)
	r.result = agent.Result{Code: agent.CodeTimeout}
	r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"weather","speak":true}`)
	r.svc.Wait()
	r.result, r.spoken = agent.Result{Silent: true}, ""
	r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"quiet","speak":true}`)
	r.svc.Wait()
	if len(r.announced) != 0 {
		t.Fatalf("announced %v", r.announced)
	}
}

// one typed turn a device at a time
func TestOneTurnADevice(t *testing.T) {
	r := newRig(t)
	r.hold = make(chan struct{})
	if status, _ := r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"one"}`); status != 202 {
		t.Fatal(status)
	}
	if status, body := r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"two"}`); status != 409 || body["error"] != "busy" {
		t.Fatalf("%d %v", status, body)
	}
	close(r.hold)
	r.svc.Wait()
	if status, _ := r.post(t, "admin", "/api/v1/devices/dev-1/assistant", `{"text":"three"}`); status != 202 {
		t.Fatal(status)
	}
	r.svc.Wait()
}

func TestRefusals(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		bearer, path, body string
		status             int
	}{
		{"admin", "/api/v1/devices/nope/assistant", `{"text":"hi"}`, 404},
		{"admin", "/api/v1/devices/dev-1/assistant", `{"text":"   "}`, 422},
		{"admin", "/api/v1/devices/dev-1/assistant", `{"text":"` + strings.Repeat("я", 1001) + `"}`, 422},
		{"admin", "/api/v1/devices/dev-1/assistant", `{"text":"hi","language":"de"}`, 422},
		{"admin", "/api/v1/devices/dev-1/assistant", `{"text":"hi","extra":1}`, 422},
		{apitest.HABearer, "/api/v1/devices/dev-1/assistant", `{"text":"hi"}`, 403},
	} {
		if status, body := r.post(t, c.bearer, c.path, c.body); status != c.status {
			t.Errorf("%s %s: %d %v", c.path, c.body, status, body)
		}
	}
	r.svc.Wait()
	if len(r.turns) != 0 {
		t.Fatalf("turns %v", r.turns)
	}
}
