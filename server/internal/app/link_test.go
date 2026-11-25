package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

type fakeCaps struct{}

func (fakeCaps) Hello(context.Context, string, *cadencev2.Hello) *cadencev2.Welcome {
	return &cadencev2.Welcome{}
}
func (fakeCaps) Handle(context.Context, string, *cadencev2.Envelope) bool { return false }
func (fakeCaps) Closed(string)                                            {}

type fakeVoices struct{}

func (fakeVoices) Linked(context.Context, gateway.Conn)                           {}
func (fakeVoices) Handle(context.Context, gateway.Conn, *cadencev2.Envelope) bool { return false }
func (fakeVoices) Disconnected(string)                                            {}

type fakeConn struct{ id string }

func (c *fakeConn) DeviceID() string               { return c.id }
func (c *fakeConn) Send(*cadencev2.Envelope) error { return nil }

// regression: a link replaced by a newer one gets no OnClose, so a device
// that came back without a session kept showing the old one as playing
func TestNewLinkForgetsMediaSession(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	sessions := media.NewStore(nil, log)
	hs := deviceLink(linkDeps{
		caps: fakeCaps{}, cmds: commands.New(nil, log), sessions: sessions, voices: fakeVoices{},
		seen: seenNone, version: "t", log: log,
	})
	ctx := context.Background()
	old := &fakeConn{id: "dev-1"}
	hs.OnHello(ctx, old, &cadencev2.Hello{})
	hs.OnMessage(ctx, old, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: "ms-1", State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
	}}})
	if _, ok := sessions.Get("dev-1"); !ok {
		t.Fatal("session not stored")
	}
	hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	if v, ok := sessions.Get("dev-1"); ok {
		t.Fatalf("stale session %+v", v)
	}
}

// a device that links again hears its waiting task updates
func TestLinkTellsTheReporter(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	var linked []string
	hs := deviceLink(linkDeps{
		caps: fakeCaps{}, cmds: commands.New(nil, log), sessions: media.NewStore(nil, log), voices: fakeVoices{},
		seen: seenNone, version: "t", log: log,
		connected: func(id string) { linked = append(linked, id) },
	})
	hs.OnHello(context.Background(), &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	if len(linked) != 1 || linked[0] != "dev-1" {
		t.Fatalf("linked %v", linked)
	}
}

func seenNone(context.Context, string) (string, string, error) { return "", "", nil }

type fakePresence struct{ calls []string }

func (p *fakePresence) Remember(id, name, model string) {
	p.calls = append(p.calls, "remember "+id+" "+name+" "+model)
}
func (p *fakePresence) Linked(id string)   { p.calls = append(p.calls, "linked "+id) }
func (p *fakePresence) Unlinked(id string) { p.calls = append(p.calls, "unlinked "+id) }

// a link's last-seen write tells presence the device's name and model, and
// its hello and close publish the device's presence; a write that failed, or
// found the device deleted, tells it nothing (M8-D2)
func TestLinkPublishesPresence(t *testing.T) {
	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	presence := &fakePresence{}
	seen := map[string]error{"dev-1": nil, "dev-2": errors.New("down"), "dev-3": enroll.ErrUnknownDevice}
	hs := deviceLink(linkDeps{
		caps: fakeCaps{}, cmds: commands.New(nil, log), sessions: media.NewStore(nil, log), voices: fakeVoices{},
		seen: func(_ context.Context, id string) (string, string, error) {
			return "kitchen", "sim", seen[id]
		},
		presence: presence, version: "t", log: log,
	})
	for _, id := range []string{"dev-1", "dev-2", "dev-3"} {
		hs.Seen(context.Background(), id)
	}
	hs.OnHello(context.Background(), &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	hs.OnClose("dev-1")
	if got := strings.Join(presence.calls, ","); got != "remember dev-1 kitchen sim,linked dev-1,unlinked dev-1" {
		t.Fatalf("presence %s", got)
	}
	if got := out.String(); strings.Count(got, "msg=last_seen_failed") != 1 || !strings.Contains(got, "msg=last_seen_failed device_id=dev-2\n") {
		t.Fatalf("log %s", got)
	}
}
