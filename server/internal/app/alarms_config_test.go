package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// zoneRig is the settings, the stored alarms configs and a clock the test ticks.
type zoneRig struct {
	mu     sync.Mutex
	global settings.Overrides
	device map[string]settings.Overrides
	// per device, the stored alarms config; absent: no alarms capability
	config map[string]json.RawMessage
	sets   []string
	gen    int64
	// failures, by dependency
	globalErr, configErr, setErr error
	// a device's first config read waits for its channel to close, ignoring its deadline, as a slow store would; each
	// read sends on entered first, when set
	block   map[string]chan struct{}
	entered chan string
	z       *alarmZones
	// per device, the config keys its reported schema declares; absent: every key
	keys map[string][]string

	ticks chan time.Time
	every time.Duration
	logs  bytes.Buffer
}

func newZoneRig() *zoneRig {
	return &zoneRig{
		global: settings.Overrides{}, device: map[string]settings.Overrides{}, config: map[string]json.RawMessage{},
		ticks: make(chan time.Time),
	}
}

func zoneSetting(zone string) settings.Overrides {
	return settings.Overrides{"timeZone": json.RawMessage(`"` + zone + `"`), "language": json.RawMessage(`"ru"`)}
}

func (r *zoneRig) zones() *alarmZones {
	r.z = newAlarmZones(zoneDeps{
		Global: func(context.Context) (settings.Overrides, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.global, r.globalErr
		},
		Device: func(_ context.Context, id string) (settings.Overrides, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if o, ok := r.device[id]; ok {
				return o, nil
			}
			return settings.Overrides{}, nil
		},
		Config: func(ctx context.Context, id, capability string) (json.RawMessage, error) {
			if capability != "alarms" {
				panic("wrong capability")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r.mu.Lock()
			wait, entered := r.block[id], r.entered
			delete(r.block, id)
			r.mu.Unlock()
			if entered != nil {
				entered <- id
			}
			if wait != nil {
				<-wait
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.configErr != nil {
				return nil, r.configErr
			}
			cfg, ok := r.config[id]
			if !ok {
				return nil, devices.ErrUnknownCapability
			}
			return cfg, nil
		},
		SetConfig: func(_ context.Context, id, capability string, cfg json.RawMessage) (int64, error) {
			if capability != "alarms" {
				panic("wrong capability")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.setErr != nil {
				return 0, r.setErr
			}
			r.sets = append(r.sets, id+" "+string(cfg))
			// jsonb renders with spaces, as the store does
			r.config[id] = bytes.ReplaceAll(cfg, []byte(`":"`), []byte(`": "`))
			r.gen++
			return r.gen, nil
		},
		ConfigKeys: func(_ context.Context, id, _ string) ([]string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			keys, ok := r.keys[id]
			if !ok {
				return []string{"timeZone", "rampSeconds", "rampFrom", "alarmLimitSeconds", "timerLimitSeconds",
					"reminderLimitSeconds", "snoozeMinutes", "autoSnooze"}, nil
			}
			return keys, nil
		},
		Log: slog.New(slog.NewTextHandler(&r.logs, nil)),
		Ticker: func(d time.Duration) (<-chan time.Time, func()) {
			r.every = d
			return r.ticks, func() {}
		},
	})
	return r.z
}

// tick runs one check and waits for it: the second tick is taken only once the first check is done
func (r *zoneRig) tick() {
	r.ticks <- time.Time{}
	r.ticks <- time.Time{}
}

// wantSets waits for the link-time checks, then takes the sets so far
func (r *zoneRig) wantSets(t *testing.T, want ...string) {
	t.Helper()
	if r.z != nil {
		r.z.settle()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.sets, want) && (len(r.sets) != 0 || len(want) != 0) {
		t.Fatalf("sets %q, want %q", r.sets, want)
	}
	r.sets = nil
}

func (r *zoneRig) set(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

// zoneCaps knows a device's capabilities at once, or asks for the full report
type zoneCaps struct{ full bool }

func (c zoneCaps) Hello(context.Context, string, *cadencev2.Hello) *cadencev2.Welcome {
	return &cadencev2.Welcome{SendFullReport: c.full}
}
func (zoneCaps) Handle(_ context.Context, _ string, env *cadencev2.Envelope) bool {
	return env.GetCapabilityReport() != nil
}
func (zoneCaps) Closed(string) {}

func zoneLink(z *alarmZones, full bool) gateway.Handlers {
	log := slog.New(slog.DiscardHandler)
	return deviceLink(linkDeps{
		caps: zoneCaps{full}, cmds: commands.New(nil, log), sessions: media.NewStore(nil, log), voices: fakeVoices{},
		seen: seenNone, zones: z, version: "t", log: log,
	})
}

const bishkek = `{"timeZone":"Asia/Bishkek"}`

// D2, R6: the device's alarms ring by its own timeZone setting, set at its link, a device override over the global one
func TestAlarmZonePushedAtLink(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.device["dev-2"] = zoneSetting("Europe/Berlin")
	r.config["dev-1"], r.config["dev-2"] = nil, json.RawMessage(`{"timeZone": "Asia/Bishkek"}`)
	hs := zoneLink(r.zones(), false)
	ctx := context.Background()
	hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	r.wantSets(t, "dev-1 "+bishkek)
	hs.OnHello(ctx, &fakeConn{id: "dev-2"}, &cadencev2.Hello{})
	r.wantSets(t, `dev-2 {"timeZone":"Europe/Berlin"}`)
	// unchanged: no new generation
	hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	hs.OnHello(ctx, &fakeConn{id: "dev-2"}, &cadencev2.Hello{})
	r.wantSets(t)
	// a device without the capability gets none
	hs.OnHello(ctx, &fakeConn{id: "dev-3"}, &cadencev2.Hello{})
	r.wantSets(t)
}

// R6 fail-closed: the built-in UTC is no one's choice, so it pushes nothing and alarms stay unavailable
func TestAlarmZoneDefaultOnlyPushesNothing(t *testing.T) {
	r := newZoneRig()
	r.global = settings.Overrides{"language": json.RawMessage(`"ru"`)}
	r.device["dev-1"] = settings.Overrides{"timeZone": json.RawMessage(`null`)}
	r.config["dev-1"] = nil
	z := r.zones()
	stop := z.Run(context.Background())
	defer stop()
	zoneLink(z, false).OnHello(context.Background(), &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	r.tick()
	r.wantSets(t)
}

// a device that sends its full report has its capabilities known once the report's last part is in
func TestAlarmZoneAfterTheFullReport(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	// what the store holds may be the old report's, which the new one replaces
	r.config["dev-1"] = nil
	hs := zoneLink(r.zones(), true)
	ctx := context.Background()
	c := &fakeConn{id: "dev-1"}
	hs.OnHello(ctx, c, &cadencev2.Hello{})
	r.wantSets(t)
	part := func(n, of uint32) *cadencev2.Envelope {
		return &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: &cadencev2.CapabilityReport{Part: n, Parts: of}}}
	}
	hs.OnMessage(ctx, c, part(0, 2))
	r.wantSets(t)
	hs.OnMessage(ctx, c, part(1, 2))
	r.wantSets(t, "dev-1 "+bishkek)
	r.set(func() { r.config["dev-1"] = nil })
	hs.OnMessage(ctx, c, part(0, 0))
	r.wantSets(t, "dev-1 "+bishkek)
}

// a changed setting reaches the linked devices at the next minute's check
func TestAlarmZoneCheckedEveryMinute(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.config["dev-1"], r.config["dev-2"] = nil, nil
	z := r.zones()
	stop := z.Run(context.Background())
	defer stop()
	if r.every != time.Minute {
		t.Fatalf("checked every %v", r.every)
	}
	hs := zoneLink(z, false)
	ctx := context.Background()
	hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	hs.OnHello(ctx, &fakeConn{id: "dev-3"}, &cadencev2.Hello{})
	r.wantSets(t, "dev-1 "+bishkek)
	r.set(func() { r.global = zoneSetting("Europe/Berlin") })
	r.tick()
	r.wantSets(t, `dev-1 {"timeZone":"Europe/Berlin"}`)
	r.tick()
	r.wantSets(t)
	// only linked devices are checked
	hs.OnClose("dev-1")
	r.set(func() { r.global = zoneSetting("Asia/Tokyo") })
	r.tick()
	r.wantSets(t)
	hs.OnHello(ctx, &fakeConn{id: "dev-2"}, &cadencev2.Hello{})
	r.wantSets(t, `dev-2 {"timeZone":"Asia/Tokyo"}`)
}

// rule 8: ids, generations and reasons only, never the zone
func TestAlarmZoneLogsNoZone(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.config["dev-1"] = nil
	z := r.zones()
	ctx := context.Background()
	z.Sync(ctx, "dev-1")
	for _, c := range []struct {
		fail   func()
		reason string
	}{
		{func() { r.setErr = &devices.ConfigInvalidError{Path: "/timeZone"} }, "invalid"},
		{func() { r.setErr = errors.New("db down") }, "store"},
		{func() { r.configErr = errors.New("db down") }, "store"},
		{func() { r.globalErr = errors.New("db down") }, "settings"},
	} {
		r.set(func() {
			r.config["dev-1"], r.setErr, r.configErr, r.globalErr = nil, nil, nil, nil
			c.fail()
		})
		z.Sync(ctx, "dev-1")
		if !strings.Contains(r.logs.String(), "msg=alarms_zone_failed device_id=dev-1 reason="+c.reason) {
			t.Fatalf("%s: %s", c.reason, r.logs.String())
		}
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "msg=alarms_zone_set device_id=dev-1 generation=1") || strings.Contains(logs, "Bishkek") {
		t.Fatalf("logs: %s", logs)
	}
}

func (r *zoneRig) setsSoFar() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sets...)
}

// a link's check runs off the gateway's lifecycle lock, after the welcome: a hello never waits on a check, and one
// device's slow check holds back no other device's
func TestAlarmZoneCheckNeverHoldsAHello(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.config["dev-1"], r.config["dev-2"] = nil, nil
	release := make(chan struct{})
	r.block = map[string]chan struct{}{"dev-1": release}
	hs := zoneLink(r.zones(), false)
	ctx := context.Background()
	hellos := make(chan struct{})
	go func() {
		defer close(hellos)
		hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
		hs.OnHello(ctx, &fakeConn{id: "dev-2"}, &cadencev2.Hello{})
	}()
	select {
	case <-hellos:
	case <-time.After(2 * time.Second):
		t.Fatal("a hello waited on a check")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Equal(r.setsSoFar(), []string{"dev-2 " + bishkek}) {
		if time.Now().After(deadline) {
			t.Fatalf("dev-2 waited on dev-1's check: sets %q", r.setsSoFar())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	r.wantSets(t, "dev-2 "+bishkek, "dev-1 "+bishkek)
}

// a check's deadline runs from when it holds the device: one queued behind a slow check of the same device still has
// all of its own
func TestAlarmZoneDeadlineStartsOnceItHoldsTheDevice(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.config["dev-1"] = nil
	release := make(chan struct{})
	r.block = map[string]chan struct{}{"dev-1": release}
	r.entered = make(chan string, 4)
	z := r.zones()
	z.d.Timeout = 100 * time.Millisecond
	ctx := context.Background()
	first, second := make(chan struct{}), make(chan struct{})
	go func() { defer close(first); z.Sync(ctx, "dev-1") }()
	<-r.entered
	go func() { defer close(second); z.Sync(ctx, "dev-1") }()
	time.Sleep(3 * z.d.Timeout)
	close(release)
	<-first
	<-second
	r.wantSets(t, "dev-1 "+bishkek)
	if strings.Contains(r.logs.String(), "alarms_zone_failed") {
		t.Fatalf("the queued check timed out waiting: %s", r.logs.String())
	}
}

// a setting removed pushes nothing, as the config needs a zone, so the device keeps the one it has; the stale zone is
// told once per device and server run
func TestAlarmZoneRemovedPushesNothing(t *testing.T) {
	r := newZoneRig()
	r.config["dev-1"] = json.RawMessage(`{"timeZone": "Asia/Bishkek"}`)
	r.config["dev-2"] = nil
	z := r.zones()
	ctx := context.Background()
	for range 2 {
		z.Sync(ctx, "dev-1")
		z.Sync(ctx, "dev-2")
	}
	r.set(func() { r.global = zoneSetting("Asia/Bishkek") })
	z.Sync(ctx, "dev-1")
	r.set(func() { r.global = settings.Overrides{} })
	z.Sync(ctx, "dev-1")
	r.wantSets(t)
	logs := r.logs.String()
	if n := strings.Count(logs, "msg=alarms_zone_unset device_id=dev-1"); n != 1 ||
		strings.Contains(logs, "alarms_zone_unset device_id=dev-2") || strings.Contains(logs, "Bishkek") {
		t.Fatalf("logs: %s", logs)
	}
}

// the alarms setting rides in the alarms config beside the zone: the device's own over the global one, whole; an
// unchanged config adds no generation, a removed setting goes back to the zone alone
func TestAlarmSettingsMergedWithTheZone(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.global["alarms"] = json.RawMessage(`{"rampSeconds":120,"rampFrom":"quiet","snoozeMinutes":5}`)
	r.device["dev-2"] = settings.Overrides{"alarms": json.RawMessage(`{"autoSnooze":2}`)}
	r.config["dev-1"], r.config["dev-2"] = nil, nil
	z := r.zones()
	stop := z.Run(context.Background())
	defer stop()
	hs := zoneLink(z, false)
	ctx := context.Background()
	hs.OnHello(ctx, &fakeConn{id: "dev-1"}, &cadencev2.Hello{})
	r.wantSets(t, `dev-1 {"rampFrom":"quiet","rampSeconds":120,"snoozeMinutes":5,"timeZone":"Asia/Bishkek"}`)
	hs.OnHello(ctx, &fakeConn{id: "dev-2"}, &cadencev2.Hello{})
	r.wantSets(t, `dev-2 {"autoSnooze":2,"timeZone":"Asia/Bishkek"}`)
	r.tick()
	r.wantSets(t)
	r.set(func() { delete(r.global, "alarms") })
	r.tick()
	r.wantSets(t, "dev-1 "+bishkek)
}

// a device whose schema predates the alarms setting gets the zone alone, so it never refuses the config every minute;
// the trim is logged once, by count, with the generation it set, and a newer device gets every key
func TestAlarmSettingsTrimmedToTheDeviceSchema(t *testing.T) {
	r := newZoneRig()
	r.global = zoneSetting("Asia/Bishkek")
	r.global["alarms"] = json.RawMessage(`{"rampSeconds":120,"snoozeMinutes":5}`)
	r.keys = map[string][]string{"old": {"timeZone"}}
	r.config["old"], r.config["new"] = nil, nil
	z := r.zones()
	stop := z.Run(context.Background())
	defer stop()
	hs := zoneLink(z, false)
	ctx := context.Background()
	hs.OnHello(ctx, &fakeConn{id: "old"}, &cadencev2.Hello{})
	r.wantSets(t, "old "+bishkek)
	hs.OnHello(ctx, &fakeConn{id: "new"}, &cadencev2.Hello{})
	r.wantSets(t, `new {"rampSeconds":120,"snoozeMinutes":5,"timeZone":"Asia/Bishkek"}`)
	r.tick()
	r.tick()
	r.wantSets(t)
	if n := strings.Count(r.logs.String(), "msg=alarms_config_trimmed device_id=old dropped=2"); n != 1 {
		t.Fatalf("trimmed logged %d times:\n%s", n, r.logs.String())
	}
	if strings.Contains(r.logs.String(), "alarms_config_trimmed device_id=new") || strings.Contains(r.logs.String(), "alarms_zone_failed") {
		t.Fatalf("log:\n%s", r.logs.String())
	}
}
