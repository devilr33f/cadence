package groups_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

// fakeDevices holds each capable device's desired config and key, as devices.Service.Ensure would
type fakeDevices struct {
	mu      sync.Mutex
	capable map[string]bool
	cfg     map[string]capschema.MediaGroup
	key     map[string]string
	sets    int
	state   map[string]json.RawMessage
}

func (f *fakeDevices) Ensure(_ context.Context, dev, capability string, cfg json.RawMessage, name, value string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.capable[dev] || capability != capschema.MediaGroupID || name != capschema.MediaGroupKeySecret {
		return false, devices.ErrUnknownCapability
	}
	var c capschema.MediaGroup
	if err := json.Unmarshal(cfg, &c); err != nil {
		return false, err
	}
	old, had := f.cfg[dev]
	if had && reflect.DeepEqual(old, c) && f.key[dev] == value {
		return false, nil
	}
	f.cfg[dev], f.key[dev] = c, value
	f.sets++
	return true, nil
}

func (f *fakeDevices) Desired(_ context.Context, dev, _ string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.capable[dev] {
		return nil, devices.ErrUnknownCapability
	}
	return nil, nil
}

func (f *fakeDevices) State(_ context.Context, dev, _ string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.state[dev]
	return s, ok
}

func (f *fakeDevices) of(dev string) (capschema.MediaGroup, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg[dev], f.key[dev]
}

func (f *fakeDevices) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

type rig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	devs   *fakeDevices
	svc    *groups.Service
	app    *fiber.App
	bus    *events.Bus
	logs   *bytes.Buffer
	ids    []string
	mu     sync.Mutex
	online map[string]bool
	cmds   *fakeCommands
	// the service's clock: a listener calibration expires by it
	now time.Time
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func newRig(t *testing.T, n int) *rig {
	t.Helper()
	pool := storetest.Pool(t)
	r := &rig{t: t, pool: pool, q: db.New(pool), logs: &bytes.Buffer{}, online: map[string]bool{}, bus: events.NewBus(64),
		cmds: &fakeCommands{}, now: time.Unix(1_800_000_000, 0),
		devs: &fakeDevices{capable: map[string]bool{}, cfg: map[string]capschema.MediaGroup{}, key: map[string]string{},
			state: map[string]json.RawMessage{}}}
	models := []string{"yandex-pickle", "yandex-midi", "sim", "android-tv", "sim"}
	for i := range n {
		d, err := r.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: fmt.Sprintf("dev%d", i), Model: models[i%len(models)]})
		if err != nil {
			t.Fatal(err)
		}
		id := d.ID.String()
		r.ids = append(r.ids, id)
		r.devs.capable[id] = true
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(r.logs, nil))
	r.svc = groups.New(groups.Deps{
		Store: pool, Box: box, Devices: r.devs, Bus: r.bus, Log: log, Commands: r.cmds,
		Now: func() time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.now
		},
		Online: func(id string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.online[id]
		},
		List: func(ctx context.Context) ([]groups.Device, error) {
			rows, err := r.q.ListDevices(ctx)
			if err != nil {
				return nil, err
			}
			var out []groups.Device
			for _, x := range rows {
				out = append(out, groups.Device{ID: x.ID.String(), Name: x.Name, Model: x.Model, Room: x.Room})
			}
			return out, nil
		},
	})
	r.app = api.NewApp(api.Options{DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { groups.RegisterAdmin(a, r.svc) }}})
	return r
}

// link brings the device online with an address, as its hello would
func (r *rig) link(i int, addr string) {
	r.mu.Lock()
	r.online[r.ids[i]] = true
	r.mu.Unlock()
	r.svc.Linked(r.ids[i], []string{addr})
}

func (r *rig) unlink(i int) {
	r.mu.Lock()
	delete(r.online, r.ids[i])
	r.mu.Unlock()
	r.svc.Closed(r.ids[i])
}

func (r *rig) call(bearer, method, path, body string) (int, string) {
	r.t.Helper()
	return apitest.Call(r.t, r.app, bearer, method, "/api/v1"+path, body)
}

func (r *rig) must(code int, method, path, body string) string {
	r.t.Helper()
	got, b := r.call("x", method, path, body)
	if got != code {
		r.t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, got, b, code)
	}
	return b
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return v
}

func (r *rig) groupBody(name string, ids ...int) string {
	var ms []string
	for _, i := range ids {
		ms = append(ms, fmt.Sprintf(`{"deviceId":%q}`, r.ids[i]))
	}
	return fmt.Sprintf(`{"name":%q,"kind":"group","members":[%s]}`, name, strings.Join(ms, ","))
}

// the api's whole path: create pushes each member's config with one key, a member going offline is dropped from the
// leader's list, the leader going offline hands over, update and delete recompute; no key reaches a log or a body
func TestGroupLifecycle(t *testing.T) {
	r := newRig(t, 4)
	ctx := context.Background()
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	r.link(2, "10.0.0.12")
	r.svc.Recompute(ctx)
	if c, _ := r.devs.of(r.ids[3]); c.Enabled || r.devs.count() != 4 {
		t.Fatalf("ungrouped devices: %+v, %d sets", c, r.devs.count())
	}
	feed, stop := r.bus.Subscribe(nil)
	defer stop()
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1, 2)))
	if created.LeaderID != r.ids[0] || !created.Active || created.LatencyMs != 1000 || len(created.Members) != 3 || !created.Members[1].Online {
		t.Fatalf("created %+v", created)
	}
	lead, key := r.devs.of(r.ids[0])
	m1, key1 := r.devs.of(r.ids[1])
	if lead.Role != "leader" || len(lead.Members) != 2 || len(key) != 64 || key1 != key || m1.Role != "member" ||
		m1.Leader.Host != "10.0.0.10" || m1.Leader.Port != 7701 || lead.BudgetUs != 5000 {
		t.Fatalf("leader %+v member %+v", lead, m1)
	}
	if e := <-feed; e.Type != events.GroupChanged || e.Data.(events.GroupData).LeaderID != r.ids[0] {
		t.Fatalf("event %+v", e)
	}
	// a member goes offline: the leader streams to the other alone; no new key
	r.unlink(2)
	r.svc.Recompute(ctx)
	if lead, k := r.devs.of(r.ids[0]); len(lead.Members) != 1 || k != key {
		t.Fatalf("after a member left: %+v", lead)
	}
	// the leader goes offline: the next online member is elected, but with no online member to stream to it is
	// disabled, as is every member
	r.unlink(0)
	r.svc.Recompute(ctx)
	if c, k := r.devs.of(r.ids[1]); c.Enabled || k != "" || r.svc.Leader(created.ID) != r.ids[1] {
		t.Fatalf("handover: %+v", c)
	}
	if e := <-feed; e.Type != events.GroupChanged || e.Data.(events.GroupData).LeaderID != r.ids[1] {
		t.Fatalf("handover event %+v", e)
	}
	r.link(0, "10.0.0.20")
	r.link(2, "10.0.0.12")
	r.svc.Recompute(ctx)
	if m, _ := r.devs.of(r.ids[1]); m.Leader == nil || m.Leader.Host != "10.0.0.20" {
		t.Fatalf("back: %+v", m)
	}
	// an unchanged recompute stores nothing
	before := r.devs.count()
	r.svc.Recompute(ctx)
	if r.devs.count() != before {
		t.Fatal("an unchanged recompute stored configs")
	}
	// update: a new member takes a new key; the latency reaches every member
	body := strings.Replace(r.groupBody("Home", 0, 1, 2, 3), `"kind"`, `"latencyMs":2000,"preferredLeader":"`+r.ids[2]+`","kind"`, 1)
	r.link(3, "10.0.0.13")
	updated := decode[groups.GroupView](t, r.must(200, http.MethodPut, "/groups/"+created.ID, body))
	c3, k3 := r.devs.of(r.ids[3])
	if updated.LeaderID != r.ids[2] || c3.LatencyMs != 2000 || c3.Leader.Host != "10.0.0.12" || k3 == key {
		t.Fatalf("update: %+v %+v", updated, c3)
	}
	// get-group: each member's reported status
	r.devs.state[r.ids[2]] = json.RawMessage(`{"state":"playing","role":"leader","synced":true,"members":[{"deviceId":"x","state":"playing","connected":true,"synced":true,"clockErrorUs":40,"queueMs":900,"rejoins":0,"placementErrorUs":3}]}`)
	got := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+created.ID, ""))
	if st := got.Members[2].Status; st == nil || st.State != "playing" || st.Members[0].ClockErrorUs != 40 || got.Members[0].Status != nil {
		t.Fatalf("status %+v", got.Members)
	}
	list := decode[[]groups.GroupView](t, r.must(200, http.MethodGet, "/groups", ""))
	if len(list) != 1 || list[0].Members[2].Status != nil {
		t.Fatalf("list %+v", list)
	}
	r.must(204, http.MethodDelete, "/groups/"+created.ID, "")
	for i := range 4 {
		if c, k := r.devs.of(r.ids[i]); c.Enabled || k != "" {
			t.Fatalf("after delete dev%d: %+v", i, c)
		}
	}
	r.must(404, http.MethodGet, "/groups/"+created.ID, "")
	for _, k := range []string{key, k3} {
		if strings.Contains(r.logs.String(), k) || strings.Contains(r.logs.String(), "10.0.0.") {
			t.Fatal("a key or an address reached the log")
		}
	}
}

// refusals: 422 with the field's path, 409 in_group and name_taken, 404 for an unknown group
func TestGroupRefusals(t *testing.T) {
	r := newRig(t, 4)
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1)))
	cases := []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodPost, "/groups", r.groupBody("Den", 2), 422, `"path":"/body/members"`},
		{http.MethodPost, "/groups", r.groupBody("Den", 2, 1), 409, `{"error":"in_group","path":"/body/members/1/deviceId"}`},
		{http.MethodPost, "/groups", r.groupBody("home", 2, 3), 409, `"error":"name_taken"`},
		{http.MethodPost, "/groups", r.groupBody(" ", 2, 3), 422, `"path":"/body/name"`},
		{http.MethodPost, "/groups", strings.Replace(r.groupBody("Den", 2, 3), r.ids[3], "00000000-0000-0000-0000-000000000099", 1), 422,
			`{"error":"unknown_device","path":"/body/members/1/deviceId"}`},
		{http.MethodPost, "/groups", strings.Replace(r.groupBody("Den", 2, 3), `"kind":"group"`, `"kind":"stereo_pair"`, 1), 422,
			`"path":"/body/members/1/channel"`},
		{http.MethodPost, "/groups", strings.Replace(r.groupBody("Den", 2, 3), `"kind"`, `"preferredLeader":"`+r.ids[0]+`","kind"`, 1), 422,
			`"path":"/body/preferredLeader"`},
		{http.MethodPut, "/groups/" + created.ID, r.groupBody("Home", 0, 1, 1), 422, `"path":"/body/members/2/deviceId"`},
		{http.MethodPut, "/groups/00000000-0000-0000-0000-000000000099", r.groupBody("X", 2, 3), 404, `"error":"not_found"`},
		{http.MethodGet, "/groups/nope", "", 404, `"error":"not_found"`},
		{http.MethodDelete, "/groups/00000000-0000-0000-0000-000000000099", "", 404, `"error":"not_found"`},
		{http.MethodPost, "/devices/00000000-0000-0000-0000-000000000099/group", `{"members":["` + r.ids[2] + `"]}`, 404, `"error":"not_found"`},
	}
	for _, c := range cases {
		code, body := r.call("x", c.method, c.path, c.body)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s %s %s: %d %s, want %d %s", c.method, c.path, c.body, code, body, c.code, c.want)
		}
	}
	// a stereo pair: one left, one right
	pair := fmt.Sprintf(`{"name":"Pair","kind":"stereo_pair","members":[{"deviceId":%q,"channel":"left"},{"deviceId":%q,"channel":"right"}]}`, r.ids[2], r.ids[3])
	r.must(201, http.MethodPost, "/groups", pair)
	// ha writes groups only through join and leave
	if code, _ := r.call(apitest.HABearer, http.MethodPost, "/groups", r.groupBody("X", 0, 1)); code != 403 {
		t.Fatalf("ha create: %d", code)
	}
	if code, _ := r.call(apitest.HABearer, http.MethodGet, "/groups", ""); code != 200 {
		t.Fatalf("ha list: %d", code)
	}
}

// Home Assistant's join: the device leads its own group or a new one, members move from the groups they were in, a
// group left with one member is deleted; leave takes a device out
func TestJoinAndLeave(t *testing.T) {
	r := newRig(t, 5)
	join := func(lead int, others ...int) groups.GroupView {
		t.Helper()
		var ids []string
		for _, o := range others {
			ids = append(ids, `"`+r.ids[o]+`"`)
		}
		code, body := r.call(apitest.HABearer, http.MethodPost, "/devices/"+r.ids[lead]+"/group", `{"members":[`+strings.Join(ids, ",")+`]}`)
		if code != 200 {
			t.Fatalf("join %d: %d %s", lead, code, body)
		}
		return decode[groups.GroupView](t, body)
	}
	a := join(0, 1)
	if a.Name != "dev0" || a.PreferredLeader != r.ids[0] || len(a.Members) != 2 || a.Members[0].DeviceID != r.ids[0] {
		t.Fatalf("new group %+v", a)
	}
	// a group named alike already: the new one takes a number
	r.must(201, http.MethodPost, "/groups", r.groupBody("dev2", 3, 4))
	b := join(2, 0)
	if b.Name != "dev2 2" || len(b.Members) != 2 {
		t.Fatalf("second group %+v", b)
	}
	// dev0 left a, which kept dev1 alone: deleted
	r.must(404, http.MethodGet, "/groups/"+a.ID, "")
	// extending: dev3 moves out of its group, which keeps dev4 alone and goes
	c := join(2, 3, 2)
	if c.ID != b.ID || len(c.Members) != 3 || c.Members[2].DeviceID != r.ids[3] {
		t.Fatalf("extended %+v", c)
	}
	list := decode[[]groups.GroupView](t, r.must(200, http.MethodGet, "/groups", ""))
	if len(list) != 1 {
		t.Fatalf("groups left %+v", list)
	}
	if code, _ := r.call(apitest.HABearer, http.MethodDelete, "/devices/"+r.ids[3]+"/group", ""); code != 204 {
		t.Fatalf("leave: %d", code)
	}
	if g := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+b.ID, "")); len(g.Members) != 2 {
		t.Fatalf("after leave %+v", g)
	}
	// leaving again, and a device in no group: nothing to do
	r.must(204, http.MethodDelete, "/devices/"+r.ids[3]+"/group", "")
	r.must(204, http.MethodDelete, "/devices/"+r.ids[0]+"/group", "")
	r.must(404, http.MethodGet, "/groups/"+b.ID, "")
	// a stereo pair takes no one more
	pair := fmt.Sprintf(`{"name":"Pair","kind":"stereo_pair","members":[{"deviceId":%q,"channel":"left"},{"deviceId":%q,"channel":"right"}]}`, r.ids[0], r.ids[1])
	r.must(201, http.MethodPost, "/groups", pair)
	if code, body := r.call(apitest.HABearer, http.MethodPost, "/devices/"+r.ids[0]+"/group", `{"members":["`+r.ids[2]+`"]}`); code != 409 ||
		!strings.Contains(body, "stereo_pair") {
		t.Fatalf("pair join: %d %s", code, body)
	}
}

// routing: a member of an active group sends media commands to its leader; a leader, another capability and an
// inactive group's member keep theirs
func TestRouteAndIntercept(t *testing.T) {
	r := newRig(t, 3)
	ctx := context.Background()
	r.link(0, "10.0.0.10")
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1)))
	if _, ok := r.svc.Route(r.ids[1], "media.url"); ok || created.Active {
		t.Fatal("a member of an inactive group routed")
	}
	r.link(1, "10.0.0.11")
	r.svc.Recompute(ctx)
	var sent []string
	icept := r.svc.Intercept(func(_ context.Context, dev, capability, name string, _ []byte) (json.RawMessage, error) {
		sent = append(sent, dev+" "+capability+" "+name)
		return nil, nil
	})
	for _, c := range []struct {
		dev, capability string
		handled         bool
	}{
		{r.ids[1], "media.url", true}, {r.ids[1], "media.player", true}, {r.ids[1], "audio.output", false},
		{r.ids[0], "media.player", false}, {r.ids[2], "media.url", false},
	} {
		if _, handled, _ := icept(ctx, c.dev, c.capability, "play", nil); handled != c.handled {
			t.Errorf("%s %s: handled %v", c.dev, c.capability, handled)
		}
	}
	if len(sent) != 2 || sent[0] != r.ids[0]+" media.url play" {
		t.Fatalf("sent %v", sent)
	}
	// the leader's followers share its session; a member and an ungrouped device lead nobody
	if f := r.svc.Followers(r.ids[0]); len(f) != 1 || f[0] != r.ids[1] {
		t.Fatalf("followers %v", f)
	}
	if r.svc.Followers(r.ids[1]) != nil || r.svc.Followers(r.ids[2]) != nil {
		t.Fatal("a non-leader has followers")
	}
	refs, err := r.svc.Refs(ctx)
	if err != nil || refs[r.ids[1]].LeaderID != r.ids[0] || refs[r.ids[1]].Name != "Home" || len(refs) != 2 {
		t.Fatalf("refs %+v %v", refs, err)
	}
	named, err := r.svc.Named(ctx)
	if err != nil || len(named) != 1 || named[0].LeaderID != r.ids[0] {
		t.Fatalf("named %+v %v", named, err)
	}
	// a member's changed status reaches the feed with its group
	feed, stop := r.bus.Subscribe(nil)
	defer stop()
	r.svc.StateChanged(r.ids[1], capschema.MediaGroupID, json.RawMessage(`{"state":"playing","synced":true}`))
	r.svc.StateChanged(r.ids[2], capschema.MediaGroupID, json.RawMessage(`{"state":"off"}`))
	e := <-feed
	if d, ok := e.Data.(events.GroupStateData); e.Type != events.GroupState || !ok || d.GroupID != created.ID || e.DeviceID != r.ids[1] {
		t.Fatalf("event %+v", e)
	}
	select {
	case e := <-feed:
		t.Fatalf("an ungrouped device's state was published: %+v", e)
	default:
	}
}

// a deleted member's group takes a new key
func TestDeletedMemberChangesTheKey(t *testing.T) {
	r := newRig(t, 3)
	ctx := context.Background()
	for i := range 3 {
		r.link(i, fmt.Sprintf("10.0.0.%d", i))
	}
	r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1, 2))
	_, key := r.devs.of(r.ids[0])
	if _, err := r.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, r.ids[2]); err != nil {
		t.Fatal(err)
	}
	r.unlink(2)
	r.svc.Recompute(ctx)
	if c, k := r.devs.of(r.ids[0]); k == key || len(c.Members) != 1 {
		t.Fatalf("after the delete: %+v, key changed %v", c, k != key)
	}
}

// a member that plays a source of its own leads the group on the clock master's clock, and the lead falls back to the
// clock master once the source has been idle past the grace
func TestSourceLeadsTheGroup(t *testing.T) {
	r := newRig(t, 3)
	ctx := context.Background()
	for i := range 3 {
		r.link(i, fmt.Sprintf("10.0.0.%d", 10+i))
	}
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1, 2)))
	if c, _ := r.devs.of(r.ids[0]); c.Role != capschema.GroupRoleLeader || c.Clock != nil {
		t.Fatalf("before any source: %+v", c)
	}
	setState := func(i int, local bool) {
		r.devs.mu.Lock()
		r.devs.state[r.ids[i]] = json.RawMessage(fmt.Sprintf(`{"state":"idle","localSource":%v}`, local))
		r.devs.mu.Unlock()
	}
	// the stored state alone (a restart, or a report the hook missed) is read at the recompute
	setState(2, true)
	r.svc.Recompute(ctx)
	clockAt := &capschema.MediaGroupLeader{Host: "10.0.0.10", Port: capschema.GroupDefaultPort}
	src, _ := r.devs.of(r.ids[2])
	master, _ := r.devs.of(r.ids[0])
	other, _ := r.devs.of(r.ids[1])
	if src.Role != capschema.GroupRoleLeader || !reflect.DeepEqual(src.Clock, clockAt) || src.ClockMaster ||
		!slices.Equal(src.Members, []string{r.ids[0], r.ids[1]}) {
		t.Fatalf("the source: %+v", src)
	}
	if master.Role != capschema.GroupRoleMember || !master.ClockMaster || master.Clock != nil || master.Leader.Host != "10.0.0.12" {
		t.Fatalf("the clock master: %+v", master)
	}
	if other.Role != capschema.GroupRoleMember || other.ClockMaster || !reflect.DeepEqual(other.Clock, clockAt) || other.Leader.Host != "10.0.0.12" {
		t.Fatalf("another member: %+v", other)
	}
	for _, i := range []int{0, 1} {
		if to, ok := r.svc.Route(r.ids[i], "media.url"); !ok || to != r.ids[2] {
			t.Fatalf("member %d routes to %q %v", i, to, ok)
		}
	}
	if _, ok := r.svc.Route(r.ids[2], "media.player"); ok {
		t.Fatal("the source routed its own command")
	}
	if created.ClockLeaderID != r.ids[0] {
		t.Fatalf("created clock master %q", created.ClockLeaderID)
	}
	if g := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+created.ID, "")); g.LeaderID != r.ids[2] || g.ClockLeaderID != r.ids[0] {
		t.Fatalf("view leader %q, clock master %q", g.LeaderID, g.ClockLeaderID)
	}
	// the film paused: the source keeps the lead through the grace
	setState(2, false)
	r.svc.StateChanged(r.ids[2], capschema.MediaGroupID, r.devs.state[r.ids[2]])
	r.advance(20 * time.Second)
	r.svc.Recompute(ctx)
	if r.svc.Leader(created.ID) != r.ids[2] {
		t.Fatalf("within the grace: %q", r.svc.Leader(created.ID))
	}
	r.advance(11 * time.Second)
	r.svc.Recompute(ctx)
	if r.svc.Leader(created.ID) != r.ids[0] {
		t.Fatalf("after the grace: %q", r.svc.Leader(created.ID))
	}
	if c, _ := r.devs.of(r.ids[0]); c.Role != capschema.GroupRoleLeader || c.Clock != nil || c.ClockMaster {
		t.Fatalf("the clock master leads again: %+v", c)
	}
	if c, _ := r.devs.of(r.ids[2]); c.Role != capschema.GroupRoleMember || c.Clock != nil || c.ClockMaster || c.Leader.Host != "10.0.0.10" {
		t.Fatalf("the old source: %+v", c)
	}
	logs := r.logs.String()
	for _, want := range []string{
		`"msg":"group_leader_moved","group_id":"` + created.ID + `","from":"` + r.ids[0] + `","to":"` + r.ids[2] + `","reason":"source"`,
		`"msg":"group_leader_moved","group_id":"` + created.ID + `","from":"` + r.ids[2] + `","to":"` + r.ids[0] + `","reason":"preferred"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("no %s in\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "10.0.0.") {
		t.Fatal("an address reached the log")
	}
}

// followVideo (multiroom P4): on by default, kept off when an update says so, and the groups with it on are listed
// with their leader and members for the screens' fan-out
func TestFollowVideo(t *testing.T) {
	r := newRig(t, 3)
	ctx := context.Background()
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Screens", 0, 1)))
	if !created.FollowVideo {
		t.Fatalf("created %+v", created)
	}
	vg := r.svc.VideoGroups()
	if len(vg) != 1 || vg[0].ID != created.ID || vg[0].Leader != r.ids[0] || len(vg[0].Members) != 2 || !vg[0].Active {
		t.Fatalf("video groups %+v", vg)
	}
	off := strings.Replace(r.groupBody("Screens", 0, 1), `"kind"`, `"followVideo":false,"kind"`, 1)
	updated := decode[groups.GroupView](t, r.must(200, http.MethodPut, "/groups/"+created.ID, off))
	if updated.FollowVideo || len(r.svc.VideoGroups()) != 0 {
		t.Fatalf("updated %+v, video groups %+v", updated, r.svc.VideoGroups())
	}
	// an update that leaves it out turns it on again, as every default does
	r.must(200, http.MethodPut, "/groups/"+created.ID, r.groupBody("Screens", 0, 1))
	r.svc.Recompute(ctx)
	if got, _ := r.svc.Get(ctx, created.ID); !got.FollowVideo {
		t.Fatalf("got %+v", got)
	}
}
