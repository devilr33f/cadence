package devices_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// linkRig is the real gateway, enrolment and devices service on one store,
// publishing presence onto bus as the app wires them: the last-seen write's
// name and model go to the service unless forget is set
type linkRig struct {
	enrolled *enroll.Service
	hub      *gateway.Hub
	svc      *devices.Service
	store    *flakyStore
	bus      *events.Bus
	admin    *fiber.App
	url      string
	closed   chan string
	forget   atomic.Bool
}

func newLinkRig(t *testing.T) *linkRig {
	t.Helper()
	pool := storetest.Pool(t)
	log := slog.New(slog.DiscardHandler)
	r := &linkRig{enrolled: enroll.NewService(db.New(pool), time.Now), hub: gateway.NewHub(), store: &flakyStore{Pool: pool},
		bus: events.NewBus(64), closed: make(chan string, 4)}
	svc := devices.NewService(r.store, r.hub, nil, log)
	r.svc = svc
	svc.PublishTo(r.bus)
	r.enrolled.PublishTo(r.bus)
	srv := fiber.New()
	gateway.Mount(srv.Group("/v2"), r.hub, r.enrolled.AuthDevice, gateway.Handlers{
		OnHello: func(ctx context.Context, c gateway.Conn, h *cadencev2.Hello) *cadencev2.Welcome {
			w := svc.Hello(ctx, c.DeviceID(), h)
			svc.Linked(c.DeviceID())
			return w
		},
		OnMessage: func(ctx context.Context, c gateway.Conn, env *cadencev2.Envelope) { svc.Handle(ctx, c.DeviceID(), env) },
		OnClose: func(id string) {
			svc.Closed(id)
			svc.Unlinked(id)
			r.closed <- id
		},
		Seen: func(ctx context.Context, id string) {
			if name, model, err := r.enrolled.Seen(ctx, id); err == nil && !r.forget.Load() {
				svc.Remember(id, name, model)
			}
		},
	}, log)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	r.url = "ws://" + ln.Addr().String() + "/v2/device"
	r.admin = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { devices.RegisterAdmin(a, svc) }},
	})
	return r
}

// enrol creates a device and exchanges its code, as cadenced enroll does
func (r *linkRig) enrol(t *testing.T) (id, token string) {
	t.Helper()
	ctx := context.Background()
	_, code, err := r.enrolled.CreateDevice(ctx, "kitchen", "sim")
	if err != nil {
		t.Fatal(err)
	}
	id, token, err = r.enrolled.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func (r *linkRig) dial(t *testing.T, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, r.url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

// link dials and greets: the device is online once its welcome is read
func (r *linkRig) link(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	c, _, err := r.dial(t, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b, err := wire.Encode(&cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{Hello: &cadencev2.Hello{Protocol: 2, Board: "sim"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
	if _, b, err = c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if env, err := wire.Decode(b); err != nil || env.GetWelcome() == nil {
		t.Fatalf("first frame %v %v", env, err)
	}
	return c
}

// a deleted device's live link closes with 1008, its token opens none again,
// and the same hardware enrolled again is a new device
func TestDeleteEndsTheLinkAndTheToken(t *testing.T) {
	r := newLinkRig(t)
	id, token := r.enrol(t)
	c := r.link(t, token)
	if !r.hub.Online(id) {
		t.Fatal("not online")
	}
	if code, body := call(t, r.admin, http.MethodDelete, "/api/v1/devices/"+id, ""); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("close %v (%v)", got, err)
	}
	select {
	case got := <-r.closed:
		if got != id {
			t.Fatalf("OnClose(%s)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no OnClose")
	}
	if r.hub.Online(id) {
		t.Fatal("online after the delete")
	}
	if _, resp, err := r.dial(t, token); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("redial %v", err)
	}
	if _, err := r.enrolled.AuthDevice(context.Background(), token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("token %v", err)
	}
	again, token := r.enrol(t)
	if again == id {
		t.Fatal("the new enrolment reused the id")
	}
	r.link(t, token)
	if !r.hub.Online(again) {
		t.Fatal("the new enrolment is not online")
	}
}
