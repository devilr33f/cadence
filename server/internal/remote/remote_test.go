package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/remote"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// world is the household the service reads: devices, groups, sessions and levels, and what it was asked to do
type world struct {
	mu       sync.Mutex
	devs     []remote.Device
	grps     []remote.Group
	sess     map[string]media.MediaSession
	levels   map[string]int
	cmds     []string
	groupOps []string
	cmdErr   error
	groupErr error
	// the caller screen's card features, nil for none
	features []string
}

func (w *world) set(dev string, s *media.MediaSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s == nil {
		delete(w.sess, dev)
		return
	}
	w.sess[dev] = *s
}

func (w *world) done() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.cmds...), append([]string(nil), w.groupOps...)
}

type rig struct {
	t      *testing.T
	w      *world
	svc    *remote.Service
	bus    *events.Bus
	router *calls.Router
	logs   *syncBuf

	mu   sync.Mutex
	sent []*cadencev2.Envelope
}

func playing(title, artist string, actions ...string) *media.MediaSession {
	vol := uint32(35)
	return &media.MediaSession{SessionID: "s-" + title, Source: "soloist", State: "playing", PositionMs: 10_000,
		PositionAt: time.UnixMilli(1_800_000_000_000).UTC().Format(time.RFC3339Nano), Rate: 1, DurationMs: 200_000,
		Volume: &vol, Metadata: media.MediaMetadata{Title: title, Artist: artist, ArtworkURL: "https://img.example/" + title},
		Actions: actions}
}

func newRig(t *testing.T) *rig {
	w := &world{
		devs: []remote.Device{
			{ID: "tv", Name: "Телевизор", Room: "Гостиная"},
			{ID: "kitchen", Name: "Кухня", Room: "Кухня"},
			{ID: "lr", Name: "Колонка", Room: "Гостиная"},
			{ID: "a", Name: "Зал А", Room: "Зал"},
			{ID: "b", Name: "Зал Б", Room: "Зал"},
			{ID: "idle", Name: "Спальня", Room: "Спальня"},
		},
		grps:   []remote.Group{{ID: "g1", Name: "Зал", Kind: "group", Leader: "a", Members: []string{"a", "b"}}},
		sess:   map[string]media.MediaSession{},
		levels: map[string]int{"b": 60, "kitchen": 20},
	}
	w.sess["kitchen"] = *playing("Song", "Artist", "pause", "resume", "next")
	paused := playing("Film", "", "pause", "resume", "seek")
	paused.State, paused.Volume = "paused", nil
	w.sess["lr"] = *paused
	w.sess["a"] = *playing("Party", "DJ", "pause", "resume", "next", "previous")
	w.sess["b"] = media.MediaSession{SessionID: "grp", Source: "group", State: "playing"}
	w.sess["tv"] = *playing("Own", "Self", "pause")
	w.sess["idle"] = media.MediaSession{SessionID: "x", State: "idle"}
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	r := &rig{t: t, w: w, bus: events.NewBus(0), logs: logs}
	cs := cards.New(cards.Deps{Lang: func(context.Context, string) string { return "ru" }, Log: log})
	r.svc = remote.New(remote.Deps{
		Devices: func(context.Context) ([]remote.Device, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return append([]remote.Device(nil), w.devs...), nil
		},
		Groups: func(context.Context) ([]remote.Group, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			return append([]remote.Group(nil), w.grps...), nil
		},
		Session: func(dev string) (media.MediaSession, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			s, ok := w.sess[dev]
			return s, ok
		},
		Level: func(_ context.Context, dev string) (int, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			l, ok := w.levels[dev]
			return l, ok
		},
		Groupable: func(context.Context, string) bool { return true },
		CardFeatures: func(_ context.Context, dev string) []string {
			w.mu.Lock()
			defer w.mu.Unlock()
			if dev != "tv" {
				return nil
			}
			return append([]string(nil), w.features...)
		},
		Now: func() time.Time { return time.UnixMilli(1_800_000_100_000) },
		Art: func(_ string, s media.MediaSession) string {
			if s.Metadata.ArtworkURL == "" {
				return ""
			}
			return "img:" + strings.Repeat("A", 20) + strings.ToUpper(s.Metadata.Title[:1]) + "xyz"
		},
		Command: func(_ context.Context, dev, capability, name string, args []byte) (json.RawMessage, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.cmds = append(w.cmds, dev+" "+capability+" "+name+" "+string(args))
			return json.RawMessage(`{}`), w.cmdErr
		},
		Join: func(_ context.Context, lead string, others []string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.groupOps = append(w.groupOps, "join "+lead+" "+strings.Join(others, ","))
			return w.groupErr
		},
		Attach: func(_ context.Context, member, dev string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.groupOps = append(w.groupOps, "attach "+member+" "+dev)
			return w.groupErr
		},
		Leave: func(_ context.Context, dev string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.groupOps = append(w.groupOps, "leave "+dev)
			return w.groupErr
		},
		Cards: cs, Log: log,
	})
	r.router = calls.New(func(_ string, env *cadencev2.Envelope) error {
		r.mu.Lock()
		r.sent = append(r.sent, env)
		r.mu.Unlock()
		return nil
	}, log)
	r.router.Register(rpc.DevicesMedia, r.svc.MediaMethod())
	r.router.Register(rpc.DevicesPanel, r.svc.PanelMethod())
	r.router.Register(rpc.CardAction, cs.ActionMethod())
	stop := r.svc.Run(context.Background(), r.bus)
	t.Cleanup(func() { r.router.Close(); stop() })
	return r
}

func (r *rig) call(id, method string, args any, subscribe bool) *cadencev2.RpcResponse {
	r.t.Helper()
	b, _ := json.Marshal(args)
	r.router.Handle("tv", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: id, Method: method, Json: string(b), Subscribe: subscribe,
	}}})
	var got *cadencev2.RpcResponse
	r.waitFor("the answer to "+id, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, e := range r.sent {
			if resp := e.GetRpcResponse(); resp != nil && resp.GetId() == id {
				got = resp
				return true
			}
		}
		return false
	})
	return got
}

func (r *rig) events(id string) []*cadencev2.RpcEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*cadencev2.RpcEvent
	for _, e := range r.sent {
		if ev := e.GetRpcEvent(); ev != nil && ev.GetId() == id && !ev.GetLast() {
			out = append(out, ev)
		}
	}
	return out
}

func (r *rig) waitFor(what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *rig) act(id string, c *cardsv1.Card, a *cardsv1.Action, value any) *cadencev2.RpcResponse {
	args := map[string]any{"cardId": c.GetId(), "revision": c.GetRevision(), "actionId": a.GetId()}
	if value != nil {
		args["value"] = value
	}
	return r.call(id, rpc.CardAction, args, false)
}

func (r *rig) changed() { r.bus.Publish(events.Event{Type: events.MediaState, DeviceID: "x"}) }

func list(t *testing.T, s string) []map[string]any {
	t.Helper()
	var l struct{ Items []map[string]any }
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return l.Items
}

// the list: the caller's own session never, an idle device never, a group as one item named after it with its
// leader's session, the caller's room first; exact fields as the contract names them
func TestDevicesMediaListsWhatOthersPlay(t *testing.T) {
	r := newRig(t)
	resp := r.call("m1", rpc.DevicesMedia, map[string]any{}, false)
	if resp.GetStatus() != rpc.StatusOK {
		t.Fatalf("%+v", resp)
	}
	want := `{"items":[` +
		`{"id":"lr","deviceId":"lr","name":"Колонка","room":"Гостиная","sameRoom":true,"source":"soloist","state":"paused","title":"Film","art":"img:AAAAAAAAAAAAAAAAAAAAFxyz","positionMs":10000,"positionAtMs":1800000000000,"durationMs":200000,"here":false},` +
		`{"id":"g1","deviceId":"a","name":"Зал","room":"Зал","sameRoom":false,"source":"soloist","state":"playing","title":"Party","artist":"DJ","art":"img:AAAAAAAAAAAAAAAAAAAAPxyz","positionMs":10000,"positionAtMs":1800000000000,"durationMs":200000,"here":false},` +
		`{"id":"kitchen","deviceId":"kitchen","name":"Кухня","room":"Кухня","sameRoom":false,"source":"soloist","state":"playing","title":"Song","artist":"Artist","art":"img:AAAAAAAAAAAAAAAAAAAASxyz","positionMs":10000,"positionAtMs":1800000000000,"durationMs":200000,"here":false}]}`
	if resp.GetJson() != want {
		t.Fatalf("list\n%s\nwant\n%s", resp.GetJson(), want)
	}
	// the caller joined the group: it is one item, here
	r.w.mu.Lock()
	r.w.grps[0].Members = append(r.w.grps[0].Members, "tv")
	r.w.mu.Unlock()
	items := list(t, r.call("m2", rpc.DevicesMedia, nil, false).GetJson())
	if len(items) != 3 || items[0]["id"] != "g1" || items[0]["here"] != true || items[0]["sameRoom"] != true || items[0]["room"] != nil {
		t.Fatalf("grouped %v", items)
	}
	if r.call("m3", rpc.DevicesMedia, map[string]any{"x": 1}, false).GetStatus() != rpc.StatusInvalid {
		t.Fatal("args taken")
	}
}

// subscribed: the list again after a change, coalesced, and only when more than a position moving on changed
func TestDevicesMediaPushesRealChangesOnly(t *testing.T) {
	r := newRig(t)
	r.call("m1", rpc.DevicesMedia, nil, true)
	// the kitchen's position moved on as it plays: nothing to send
	s := playing("Song", "Artist", "pause", "resume", "next")
	s.PositionMs, s.PositionAt = 15_000, time.UnixMilli(1_800_000_005_000).UTC().Format(time.RFC3339Nano)
	r.w.set("kitchen", s)
	r.changed()
	time.Sleep(600 * time.Millisecond)
	if n := len(r.events("m1")); n != 0 {
		t.Fatalf("%d events for a position moving on", n)
	}
	// a seek is news, and a burst is one event
	s.PositionMs, s.PositionAt = 90_000, time.UnixMilli(1_800_000_006_000).UTC().Format(time.RFC3339Nano)
	r.w.set("kitchen", s)
	r.changed()
	r.changed()
	r.waitFor("the seek's event", func() bool { return len(r.events("m1")) == 1 })
	// the kitchen stops: gone from the list
	r.w.set("kitchen", nil)
	r.changed()
	r.waitFor("the stop's event", func() bool { return len(r.events("m1")) == 2 })
	ev := r.events("m1")
	if items := list(t, ev[1].GetJson()); len(items) != 2 || items[1]["id"] != "g1" {
		t.Fatalf("after the stop %v", items)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(r.events("m1")); n != 2 {
		t.Fatalf("%d events", n)
	}
}

func cardOf(t *testing.T, p *cardsv1.Page, id string) *cardsv1.Card {
	t.Helper()
	for _, c := range p.GetCards() {
		if c.GetId() == id {
			return c
		}
	}
	t.Fatalf("no card %s in %v", id, p)
	return nil
}

type found struct {
	buttons map[string]*cardsv1.Button
	sliders []*cardsv1.Slider
	texts   []string
	images  []string
}

func walk(n *cardsv1.Node, f *found) {
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Card:
		for _, c := range k.Card.GetChildren() {
			walk(c, f)
		}
		for _, b := range k.Card.GetActions() {
			f.buttons[b.GetIcon()] = b
		}
	case *cardsv1.Node_Row:
		for _, c := range k.Row.GetChildren() {
			walk(c, f)
		}
	case *cardsv1.Node_Column:
		for _, c := range k.Column.GetChildren() {
			walk(c, f)
		}
	case *cardsv1.Node_Button:
		f.buttons[k.Button.GetIcon()] = k.Button
	case *cardsv1.Node_Slider:
		f.sliders = append(f.sliders, k.Slider)
	case *cardsv1.Node_Text:
		f.texts = append(f.texts, k.Text.GetText())
	case *cardsv1.Node_Image:
		f.images = append(f.images, k.Image.GetRef())
	}
}

func parts(c *cardsv1.Card) found {
	f := found{buttons: map[string]*cardsv1.Button{}}
	walk(c.GetRoot(), &f)
	return f
}

// the panel: a card per item, the caller's room first; the transport the session lists, a slider per device with its
// level, «play here too»; the focused device's card first
func TestDevicesPanelCards(t *testing.T) {
	r := newRig(t)
	resp := r.call("p1", rpc.DevicesPanel, map[string]any{}, false)
	p := resp.GetPage()
	if resp.GetStatus() != rpc.StatusOK || len(p.GetCards()) != 3 || p.GetCards()[0].GetId() != "remote-lr" {
		t.Fatalf("%+v %s", resp, r.logs.String())
	}
	g := cardOf(t, p, "remote-g1")
	if blk := g.GetRoot().GetCard(); blk.GetTitle() != "Зал" || blk.GetSubtitle() != "Зал · играет" {
		t.Fatalf("group head %v", blk)
	}
	gp := parts(g)
	for _, icon := range []string{"previous", "pause", "next", "share_to_speaker"} {
		if gp.buttons[icon] == nil {
			t.Fatalf("group card lacks %s: %v", icon, gp.buttons)
		}
	}
	if gp.buttons["play"] != nil || gp.buttons["share_to_speaker"].GetLabel() != "Играть и здесь" {
		t.Fatalf("buttons %v", gp.buttons)
	}
	// a slider per member: a's level from its session, b's from its audio.output
	if len(gp.sliders) != 2 || gp.sliders[0].GetValue() != 35 || gp.sliders[0].GetLabel() != "Зал А" ||
		gp.sliders[1].GetValue() != 60 || gp.sliders[1].GetStep() != 5 || gp.sliders[1].GetMax() != 100 {
		t.Fatalf("sliders %v", gp.sliders)
	}
	if len(gp.images) != 1 || gp.texts[0] != "Party" || gp.texts[1] != "DJ" {
		t.Fatalf("now playing %v %v", gp.images, gp.texts)
	}
	// paused: resume, no pause; no next it does not list
	lp := parts(cardOf(t, p, "remote-lr"))
	if lp.buttons["play"] == nil || lp.buttons["pause"] != nil || lp.buttons["next"] != nil || len(lp.sliders) != 0 {
		t.Fatalf("paused card %v %v", lp.buttons, lp.sliders)
	}
	// focus: the group's card first, named by a member
	if p := r.call("p2", rpc.DevicesPanel, map[string]any{"focus": "b"}, false).GetPage(); p.GetCards()[0].GetId() != "remote-g1" {
		t.Fatalf("focus %v", p.GetCards()[0].GetId())
	}
	// nothing plays elsewhere: one card saying so
	r.w.mu.Lock()
	r.w.sess = map[string]media.MediaSession{}
	r.w.mu.Unlock()
	if p := r.call("p3", rpc.DevicesPanel, nil, false).GetPage(); len(p.GetCards()) != 1 || p.GetCards()[0].GetId() != "remote-empty" {
		t.Fatalf("empty %v", p)
	}
}

// the card actions: the transport to the leader, a slider's level to its own device, «play here too» as an attach to a
// group and a join of a lone device, «stop here» as a leave; a transport the session no longer lists sends nothing
func TestDevicesPanelActions(t *testing.T) {
	r := newRig(t)
	p := r.call("p1", rpc.DevicesPanel, nil, true).GetPage()
	g := cardOf(t, p, "remote-g1")
	res := r.act("a1", g, parts(g).buttons["next"].GetAction(), nil).GetCardAction()
	if res.GetRefusal() != nil || res.GetCard() == nil {
		t.Fatalf("next %+v", res)
	}
	g = r.act("a2", res.GetCard(), parts(res.GetCard()).sliders[1].GetAction(), 25).GetCardAction().GetCard()
	r.act("a3", g, parts(g).buttons["share_to_speaker"].GetAction(), nil)
	k := cardOf(t, p, "remote-kitchen")
	k = r.act("a4", k, parts(k).buttons["share_to_speaker"].GetAction(), nil).GetCardAction().GetCard()
	cmds, ops := r.w.done()
	if strings.Join(cmds, "|") != `a media.player next {}|b audio.output set_level {"level":25}` {
		t.Fatalf("commands %q", cmds)
	}
	if strings.Join(ops, "|") != "attach a tv|join kitchen tv" {
		t.Fatalf("group ops %q", ops)
	}
	// the kitchen's session no longer lists next: its press sends nothing and is refused
	r.w.set("kitchen", playing("Song", "Artist", "pause"))
	res = r.act("a5", k, parts(k).buttons["next"].GetAction(), nil).GetCardAction()
	if res.GetRefusal().GetReason() != "unsupported" {
		t.Fatalf("stale next %+v", res)
	}
	if cmds, _ := r.w.done(); len(cmds) != 2 {
		t.Fatalf("sent %q", cmds)
	}
	// redrawn: no next any more
	if parts(res.GetRefusal().GetCard()).buttons["next"] != nil {
		t.Fatalf("redrawn %v", res.GetRefusal().GetCard())
	}
	// the caller in the group: its card says so and offers «stop here»
	r.w.mu.Lock()
	r.w.grps[0].Members = append(r.w.grps[0].Members, "tv")
	r.w.mu.Unlock()
	p = r.call("p2", rpc.DevicesPanel, nil, false).GetPage()
	g = cardOf(t, p, "remote-g1")
	gp := parts(g)
	if g.GetRoot().GetCard().GetSubtitle() != "Играет и здесь" || gp.buttons["close"].GetLabel() != "Отключить здесь" {
		t.Fatalf("here card %v", g)
	}
	r.act("a6", g, gp.buttons["close"].GetAction(), nil)
	if _, ops := r.w.done(); ops[len(ops)-1] != "leave tv" {
		t.Fatalf("ops %q", ops)
	}
	// an offline device's refusal
	r.w.mu.Lock()
	r.w.cmdErr = commands.ErrOffline
	r.w.mu.Unlock()
	k = cardOf(t, p, "remote-kitchen")
	if res := r.act("a7", k, parts(k).buttons["pause"].GetAction(), nil).GetCardAction(); res.GetRefusal().GetReason() != "offline" {
		t.Fatalf("offline %+v", res)
	}
	// subscribed: a level change is a new page
	r.w.mu.Lock()
	r.w.levels["b"] = 80
	r.w.mu.Unlock()
	r.changed()
	r.waitFor("a new page", func() bool {
		for _, ev := range r.events("p1") {
			if ev.GetPage() != nil {
				return true
			}
		}
		return false
	})
}

// a stereo pair takes no one: «play here too» on its card is refused without a call
func TestDevicesPanelRefusesAStereoPair(t *testing.T) {
	r := newRig(t)
	r.w.mu.Lock()
	r.w.grps[0].Kind = groups.KindStereoPair
	r.w.mu.Unlock()
	g := cardOf(t, r.call("p1", rpc.DevicesPanel, nil, false).GetPage(), "remote-g1")
	res := r.act("a1", g, parts(g).buttons["share_to_speaker"].GetAction(), nil).GetCardAction()
	if res.GetRefusal().GetReason() != "stereo_pair" {
		t.Fatalf("pair %+v", res)
	}
	// the store's own refusal maps alike
	r.w.mu.Lock()
	r.w.grps[0].Kind = groups.KindGroup
	r.w.groupErr = groups.ErrStereoPair
	r.w.mu.Unlock()
	g = cardOf(t, r.call("p2", rpc.DevicesPanel, nil, false).GetPage(), "remote-g1")
	if res := r.act("a2", g, parts(g).buttons["share_to_speaker"].GetAction(), nil).GetCardAction(); res.GetRefusal().GetReason() != "stereo_pair" {
		t.Fatalf("store pair %+v", res)
	}
	if _, ops := r.w.done(); len(ops) != 1 {
		t.Fatalf("ops %q", ops)
	}
}

// rule 8: the lines carry ids, codes and counts; no title, artist, cover or name
func TestDevicesLogsNoMetadata(t *testing.T) {
	r := newRig(t)
	r.call("m1", rpc.DevicesMedia, nil, false)
	p := r.call("p1", rpc.DevicesPanel, nil, false).GetPage()
	g := cardOf(t, p, "remote-g1")
	g = r.act("a1", g, parts(g).buttons["pause"].GetAction(), nil).GetCardAction().GetCard()
	g = r.act("a2", g, parts(g).sliders[0].GetAction(), 40).GetCardAction().GetCard()
	r.act("a3", g, parts(g).buttons["share_to_speaker"].GetAction(), nil)
	logs := r.logs.String()
	if !strings.Contains(logs, "devices_panel_action") || !strings.Contains(logs, "devices_media") {
		t.Fatalf("no lines: %s", logs)
	}
	for _, secret := range []string{"Party", "DJ", "Song", "Artist", "Film", "img.example", "img:", "Зал", "Кухня", "Колонка", "Гостиная", "Телевизор"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("%q logged: %s", secret, logs)
		}
	}
}
