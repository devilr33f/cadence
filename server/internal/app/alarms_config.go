package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	alarmsCapability = "alarms"
	// how often the linked devices' zones are checked against their settings
	zoneCheckEvery = time.Minute
	// bounds one device's check: it reads the settings and the stored config, and may set a new one
	zoneSyncTimeout = 5 * time.Second
)

type zoneDeps struct {
	// the global and the device's own settings, the keys each scope sets
	Global func(ctx context.Context) (settings.Overrides, error)
	Device func(ctx context.Context, deviceID string) (settings.Overrides, error)
	// the stored config of a device's capability, and setting a new one
	Config    func(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
	SetConfig func(ctx context.Context, deviceID, capability string, cfg json.RawMessage) (int64, error)
	// ConfigKeys names the keys the device's reported config schema declares; nil: every key goes
	ConfigKeys func(ctx context.Context, deviceID, capability string) ([]string, error)
	Log        *slog.Logger
	// Ticker fires each check; nil: a time.Ticker
	Ticker func(d time.Duration) (<-chan time.Time, func())
	// Timeout bounds one device's check from when it holds the device; zero: zoneSyncTimeout
	Timeout time.Duration
	// Session keeps the device's session config too (its error sound and prompt language) at each check
	Session bool
}

// alarmZones keeps each linked device's alarms config equal to its settings, one source of truth: the zone (M6a-D2)
// of its timeZone setting, and the keys of its alarms setting (ramp, ring limits, snooze), the device's own over the
// global one, each key absent the device's default; with Session also its session config equal to its errorSound
// and language settings. Only a zone a scope set counts (R6): the built-in UTC is no one's choice, and an alarm rung
// by a wrong calendar is worse than none, so until the owner sets one the device's alarms stay unavailable (R2).
type alarmZones struct {
	d zoneDeps

	mu     sync.Mutex
	linked map[string]bool
	// one check of a device at a time, as the devices service's push lock: a link and the minute's check never set the
	// same zone twice, and no device's check waits on another's
	checking map[string]*sync.Mutex
	// the devices whose stored zone no scope sets any more, told once each per server run
	unset map[string]bool
	// the link-time checks in flight, which stop waits for; none starts once stopped
	inflight sync.WaitGroup
	stopped  bool
}

func newAlarmZones(d zoneDeps) *alarmZones {
	if d.Ticker == nil {
		d.Ticker = func(every time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(every)
			return t.C, t.Stop
		}
	}
	if d.Timeout == 0 {
		d.Timeout = zoneSyncTimeout
	}
	return &alarmZones{d: d, linked: map[string]bool{}, checking: map[string]*sync.Mutex{}, unset: map[string]bool{}}
}

// Linked makes the device one the minute's check covers.
func (z *alarmZones) Linked(deviceID string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.linked[deviceID] = true
}

func (z *alarmZones) Closed(deviceID string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	delete(z.linked, deviceID)
}

// Run checks the linked devices every minute until ctx ends or stop is called.
func (z *alarmZones) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ticks, stopTicks := z.d.Ticker(zoneCheckEvery)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicks()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				for _, id := range z.linkedIDs() {
					z.Sync(ctx, id)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
		z.mu.Lock()
		z.stopped = true
		z.mu.Unlock()
		z.inflight.Wait()
	}
}

// SyncLater runs the device's check on its own goroutine: a link's hello and report run under the gateway's lifecycle
// lock, which no store read of the check may hold, and whatever it pushes goes out after the welcome.
func (z *alarmZones) SyncLater(ctx context.Context, deviceID string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.stopped {
		return
	}
	z.inflight.Add(1)
	go func() {
		defer z.inflight.Done()
		z.Sync(ctx, deviceID)
	}()
}

// settle waits for the link-time checks in flight
func (z *alarmZones) settle() { z.inflight.Wait() }

// hold takes the device's check lock
func (z *alarmZones) hold(deviceID string) func() {
	z.mu.Lock()
	m := z.checking[deviceID]
	if m == nil {
		m = &sync.Mutex{}
		z.checking[deviceID] = m
	}
	z.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (z *alarmZones) linkedIDs() []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	ids := make([]string, 0, len(z.linked))
	for id := range z.linked {
		ids = append(ids, id)
	}
	return ids
}

// Sync sets the device's alarms config to its zone and alarms setting when the stored one differs: an unchanged config
// adds no generation. A device without the capability, or without a zone a scope set, gets nothing: the config needs
// a zone, so a removed setting leaves the device on the one it has, which is told once.
func (z *alarmZones) Sync(ctx context.Context, deviceID string) {
	defer z.hold(deviceID)()
	ctx, cancel := context.WithTimeout(ctx, z.d.Timeout)
	defer cancel()
	if z.d.Session {
		defer z.syncSessionConfig(ctx, deviceID)
	}
	z.syncZone(ctx, deviceID)
}

// syncZone is Sync's alarms zone; callers hold the device
func (z *alarmZones) syncZone(ctx context.Context, deviceID string) {
	failed := func(reason string) { z.d.Log.Warn("alarms_zone_failed", "device_id", deviceID, "reason", reason) }
	stored, err := z.d.Config(ctx, deviceID, alarmsCapability)
	if errors.Is(err, devices.ErrUnknownCapability) {
		return
	}
	if err != nil {
		failed("store")
		return
	}
	zone, ok, extra, err := z.zone(ctx, deviceID)
	if err != nil {
		failed("settings")
		return
	}
	var cur map[string]any
	known := json.Unmarshal(stored, &cur) == nil
	if !ok {
		if zone, _ := cur["timeZone"].(string); known && zone != "" {
			z.toldUnset(deviceID)
		}
		return
	}
	want := map[string]any{"timeZone": zone}
	for k, v := range extra {
		want[k] = v
	}
	// a device older than a key refuses the whole config, every minute: it gets only the keys its schema declares
	dropped := 0
	if z.d.ConfigKeys != nil && len(extra) > 0 {
		keys, err := z.d.ConfigKeys(ctx, deviceID, alarmsCapability)
		if err != nil {
			failed("store")
			return
		}
		for k := range want {
			if k != "timeZone" && !slices.Contains(keys, k) {
				delete(want, k)
				dropped++
			}
		}
	}
	if known && reflect.DeepEqual(cur, want) {
		return
	}
	cfg, err := json.Marshal(want)
	if err != nil {
		return
	}
	gen, err := z.d.SetConfig(ctx, deviceID, alarmsCapability, cfg)
	var invalid *devices.ConfigInvalidError
	switch {
	case errors.As(err, &invalid):
		failed("invalid")
	case errors.Is(err, devices.ErrUnknownCapability):
	case err != nil:
		failed("store")
	default:
		z.d.Log.Info("alarms_zone_set", "device_id", deviceID, "generation", gen)
		// once per generation: an unchanged trimmed config is not set again
		if dropped > 0 {
			z.d.Log.Info("alarms_config_trimmed", "device_id", deviceID, "dropped", dropped)
		}
	}
}

// toldUnset logs, once per device and server run, that the device keeps a zone no scope sets any more
func (z *alarmZones) toldUnset(deviceID string) {
	z.mu.Lock()
	told := z.unset[deviceID]
	z.unset[deviceID] = true
	z.mu.Unlock()
	if !told {
		z.d.Log.Info("alarms_zone_unset", "device_id", deviceID)
	}
}

// zone is the device's timeZone as a scope set it, its own override else the global one, and the keys of its alarms
// setting the same way, as the config holds them
func (z *alarmZones) zone(ctx context.Context, deviceID string) (string, bool, map[string]any, error) {
	own, err := z.d.Device(ctx, deviceID)
	if err != nil {
		return "", false, nil, err
	}
	global, err := z.d.Global(ctx)
	if err != nil {
		return "", false, nil, err
	}
	extra, ok := alarmsIn(own)
	if !ok {
		extra, _ = alarmsIn(global)
	}
	if zone, ok := zoneIn(own); ok {
		return zone, true, extra, nil
	}
	zone, ok := zoneIn(global)
	return zone, ok, extra, nil
}

// alarmsIn is the alarms setting's keys a scope set, decoded as the stored config decodes; false when it sets none
func alarmsIn(o settings.Overrides) (map[string]any, bool) {
	raw, ok := o["alarms"]
	var keys map[string]any
	if !ok || json.Unmarshal(raw, &keys) != nil || keys == nil {
		return nil, false
	}
	delete(keys, "timeZone")
	return keys, true
}

func zoneIn(o settings.Overrides) (string, bool) {
	var zone string
	raw, ok := o["timeZone"]
	if !ok || json.Unmarshal(raw, &zone) != nil || zone == "" {
		return "", false
	}
	return zone, true
}
