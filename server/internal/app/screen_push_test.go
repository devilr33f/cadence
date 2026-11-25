package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type pushRig struct {
	mu      sync.Mutex
	sent    []string
	fields  *cadencev2.MediaSessionState
	configs map[string]string
	lang    string
	sums    int
	push    *screenPush
}

func newPushRig() *pushRig {
	r := &pushRig{configs: map[string]string{"tv": `{"overscan":5}`}, lang: "ru"}
	r.push = (&screenPush{
		send: func(dev string, env *cadencev2.Envelope) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			kind := "?"
			switch {
			case env.GetMediaSessionState() != nil:
				kind = "fields:" + env.GetMediaSessionState().GetSessionId()
			case env.GetTasksSummary() != nil:
				kind = "summary"
			case env.GetDeviceInfo() != nil:
				kind = "info:" + env.GetDeviceInfo().GetName() + "/" + env.GetDeviceInfo().GetRoomName()
			}
			r.sent = append(r.sent, dev+" "+kind)
			return nil
		},
		isScreen: func(_ context.Context, dev string) bool { return dev != "speaker" },
		fields: []func(string) (*cadencev2.MediaSessionState, bool){func(string) (*cadencev2.MediaSessionState, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.fields, r.fields != nil
		}},
		summary: func(context.Context, string) (*cadencev2.TasksSummary, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sums++
			return &cadencev2.TasksSummary{Running: 1}, nil
		},
		screens: func(context.Context) []string { return []string{"tv", "tv2", "speaker"} },
		info: func(context.Context, string) *cadencev2.DeviceInfo {
			return &cadencev2.DeviceInfo{Name: "Гостиная ТВ", RoomId: "Гостиная", RoomName: "Гостиная"}
		},
		effective: func(context.Context, string) (settings.Values, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return settings.Values{Language: r.lang}, nil
		},
		desired: func(_ context.Context, dev, _ string) (json.RawMessage, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			c, ok := r.configs[dev]
			if !ok {
				return nil, devices.ErrUnknownCapability
			}
			return json.RawMessage(c), nil
		},
		setConfig: func(_ context.Context, dev, capability string, cfg json.RawMessage) (int64, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if capability != capschema.DisplayScreenID {
				return 0, errors.New("capability")
			}
			r.configs[dev] = string(cfg)
			return 1, nil
		},
		log: slog.New(slog.DiscardHandler),
	}).init()
	return r
}

func (r *pushRig) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func (r *pushRig) wait(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %v", r.got())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// a screen gets its session's fields when they change, once each, again after a new link, and never a speaker
func TestScreenPushFields(t *testing.T) {
	r := newPushRig()
	r.push.FieldsChanged("tv")
	r.mu.Lock()
	r.fields = &cadencev2.MediaSessionState{SessionId: "s1", Actions: []string{"next"}}
	r.mu.Unlock()
	r.push.FieldsChanged("tv")
	r.push.FieldsChanged("tv")
	r.push.FieldsChanged("speaker")
	r.mu.Lock()
	r.fields = &cadencev2.MediaSessionState{SessionId: "s1", Actions: []string{"next", "previous"}}
	r.mu.Unlock()
	r.push.FieldsChanged("tv")
	r.push.Linked("tv")
	got := r.got()
	if len(got) < 3 || got[0] != "tv fields:s1" || got[1] != "tv fields:s1" || got[2] != "tv fields:s1" {
		t.Fatalf("sent %v", got)
	}
	for _, g := range got {
		if g == "speaker fields:s1" {
			t.Fatalf("a speaker got fields: %v", got)
		}
	}
}

// task changes in a burst send one summary per linked screen; a device's own listing sends it its own
func TestScreenPushSummaries(t *testing.T) {
	r := newPushRig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := r.push.Run(ctx)
	defer stop()
	for range 5 {
		r.push.SummaryChanged("")
	}
	r.wait(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.sums == 2 })
	time.Sleep(2 * summaryDebounce)
	r.mu.Lock()
	if r.sums != 2 {
		t.Fatalf("%d summaries for a burst", r.sums)
	}
	r.mu.Unlock()
	r.push.SummaryChanged("tv2")
	r.wait(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.sums == 3 })
}

// a rename (or a room set) reaches the device as DeviceInfo
func TestScreenPushRenames(t *testing.T) {
	r := newPushRig()
	bus := events.NewBus(0)
	stop := r.push.WatchRenames(bus)
	defer stop()
	bus.Publish(events.Event{Type: events.DeviceRenamed, DeviceID: "tv", At: time.Now()})
	r.wait(t, func() bool {
		g := r.got()
		return len(g) == 1 && g[0] == "tv info:Гостиная ТВ/Гостиная"
	})
}

// the language setting joins display.screen's config, once, keeping its other keys; a device without the capability
// and a language the screen does not take are left alone
func TestScreenPushLanguage(t *testing.T) {
	r := newPushRig()
	ctx := context.Background()
	r.push.syncLanguage(ctx, "tv")
	if c := r.configs["tv"]; c != `{"language":"ru","overscan":5}` {
		t.Fatalf("config %s", c)
	}
	r.configs["tv"] = `{"language":"ru","overscan":2.5}`
	r.push.syncLanguage(ctx, "tv")
	if c := r.configs["tv"]; c != `{"language":"ru","overscan":2.5}` {
		t.Fatalf("rewritten %s", c)
	}
	r.push.syncLanguage(ctx, "speaker")
	r.lang = "de"
	r.push.syncLanguage(ctx, "tv")
	if _, ok := r.configs["speaker"]; ok || r.configs["tv"] != `{"language":"ru","overscan":2.5}` {
		t.Fatalf("configs %v", r.configs)
	}
}

func TestCurrentSessionRunsThePositionForward(t *testing.T) {
	store := media.NewStore(nil, slog.New(slog.DiscardHandler))
	at := time.Unix(1_800_000_000, 0)
	store.Handle("tv", &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: "v1", State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, PositionMs: 60_000, PositionAtMs: at.UnixMilli(),
	}}})
	id, pos, ok := currentSession(store, func() time.Time { return at.Add(1500 * time.Millisecond) })("tv")
	if !ok || id != "v1" || pos != 61_500 {
		t.Fatalf("%s %d %v", id, pos, ok)
	}
	if _, _, ok := currentSession(store, time.Now)("none"); ok {
		t.Fatal("a device without a session")
	}
}

type musicOwner struct{ owns string }

func (m musicOwner) OwnsSession(_, id string) bool { return id == m.owns }
func (musicOwner) ControlMethod() calls.Method {
	return func(context.Context, calls.Call) calls.Reply { return calls.OK(map[string]string{"by": "music"}) }
}

type catalogOwner struct {
	owns    string
	actions []string
	te      *agent.ToolError
}

func (c *catalogOwner) Owns(_, id string) bool { return id == c.owns }
func (c *catalogOwner) Control(_ context.Context, _, _, action, ref string) (*agent.ToolError, error) {
	c.actions = append(c.actions, action+":"+ref)
	if ref == "ce:none" {
		return nil, screenplay.ErrUnknownRef
	}
	return c.te, nil
}
func (c *catalogOwner) ItemAction(_ context.Context, _, _, id string) error {
	c.actions = append(c.actions, "item:"+id)
	if id == "gone" {
		return screenplay.ErrUnknownAction
	}
	return nil
}

// media.control and media.item_action reach the service that owns the session: the queue's songs, the catalog's
// episodes; another session is not_found
func TestMediaMethodsDispatch(t *testing.T) {
	cat := &catalogOwner{owns: "video-1"}
	m := map[string]calls.Method{}
	mediaMethods{music: musicOwner{owns: "song-1"}, catalog: cat, log: slog.New(slog.DiscardHandler)}.register(
		func(name string, mm calls.Method) { m[name] = mm })
	for args, want := range map[string]string{
		`{"sessionId":"song-1","action":"next"}`:                  rpc.StatusOK,
		`{"sessionId":"video-1","action":"next"}`:                 rpc.StatusOK,
		`{"sessionId":"video-1","action":"jump","ref":"ce:2"}`:    rpc.StatusOK,
		`{"sessionId":"video-1","action":"jump","ref":"ce:none"}`: rpc.StatusExpired,
		`{"sessionId":"video-1","action":"remove","ref":"ce:2"}`:  rpc.StatusInvalid,
		`{"sessionId":"video-1","action":"pause"}`:                rpc.StatusInvalid,
		`{"sessionId":"other","action":"next"}`:                   rpc.StatusNotFound,
		`{"sessionId":"video-1","action":"next","x":1}`:           rpc.StatusInvalid,
	} {
		if rep := callOn(m, "tv", rpc.MediaControl, args); rep.Status != want {
			t.Errorf("%s: %+v, want %s", args, rep, want)
		}
	}
	cat.te = &agent.ToolError{Code: "unplayable"}
	if rep := callOn(m, "tv", rpc.MediaControl, `{"sessionId":"video-1","action":"next"}`); rep.Status != rpc.StatusRefused {
		t.Errorf("a failed play %+v", rep)
	}
	for args, want := range map[string]string{
		`{"sessionId":"video-1","actionId":"a1"}`:   rpc.StatusOK,
		`{"sessionId":"video-1","actionId":"gone"}`: rpc.StatusExpired,
		`{"sessionId":"song-1","actionId":"a1"}`:    rpc.StatusExpired,
		`{"sessionId":"other","actionId":"a1"}`:     rpc.StatusNotFound,
		`{"sessionId":"video-1"}`:                   rpc.StatusInvalid,
	} {
		if rep := callOn(m, "tv", rpc.MediaItemAction, args); rep.Status != want {
			t.Errorf("%s: %+v, want %s", args, rep, want)
		}
	}
}

// the welcome carries the name and room the owner set, and a new link sends the screen what it lacks
func TestWelcomeCarriesTheDeviceInfo(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	var linked []string
	hs := deviceLink(linkDeps{
		caps: zoneCaps{false}, cmds: commands.New(nil, log), sessions: media.NewStore(nil, log), voices: fakeVoices{},
		seen: seenNone, version: "t", log: log, connected: func(id string) { linked = append(linked, id) },
		info: func(_ context.Context, id string) *cadencev2.DeviceInfo {
			return &cadencev2.DeviceInfo{Name: "ТВ " + id, RoomId: "Кухня", RoomName: "Кухня"}
		},
	})
	w := hs.OnHello(context.Background(), &fakeConn{id: "tv"}, &cadencev2.Hello{})
	if w.GetDeviceInfo().GetName() != "ТВ tv" || w.GetDeviceInfo().GetRoomName() != "Кухня" || len(linked) != 1 {
		t.Fatalf("welcome %v, linked %v", w, linked)
	}
}
