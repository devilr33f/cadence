package bluetooth

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	maxScanSeconds = 30
	// how long our agent accepts bluez's questions about a pairing we started
	outgoingFor = 60 * time.Second

	scanSchema = `{"type":"object","properties":{"seconds":{"type":"integer","minimum":1,"maximum":30},` +
		`"sessionId":` + sessionIDSchema + `},"additionalProperties":false}`
	speakerForgetSchema = deviceArgsSchema
	setRouteSchema      = `{"type":"object","properties":{"route":{"enum":["local","bluetooth","both"]},"id":` + idSchema +
		`,"sessionId":` + sessionIDSchema + `},"required":["route"],"additionalProperties":false}`
)

var (
	errUnsupported = &capability.CommandError{Code: "unsupported"}
	errBusy        = &capability.CommandError{Code: "busy"}
)

func outputCommands() map[string]string {
	return map[string]string{
		"scan": scanSchema, "pair": deviceArgsSchema, "connect": deviceArgsSchema, "disconnect": deviceArgsSchema,
		"forget": speakerForgetSchema, "set_route": setRouteSchema,
	}
}

func (b *Bluetooth) outputCommand(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "scan":
		var a struct {
			Seconds   *int            `json:"seconds"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || a.Seconds != nil && (*a.Seconds < 1 || *a.Seconds > maxScanSeconds) {
			return nil, errInvalid
		}
		if err := b.woken(ctx, a.SessionID); err != nil {
			return nil, err
		}
		d := b.out.opts.ScanFor
		if a.Seconds != nil {
			d = time.Duration(*a.Seconds) * time.Second
		}
		return b.scan(ctx, d)
	case "pair", "connect", "disconnect", "forget":
		var a struct {
			ID        string          `json:"id"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || !validID(a.ID) {
			return nil, errInvalid
		}
		if err := b.woken(ctx, a.SessionID); err != nil {
			return nil, err
		}
		switch name {
		case "pair":
			return b.pair(ctx, a.ID)
		case "forget":
			if err := b.speaker(ctx, a.ID); err != nil {
				return nil, err
			}
			return b.forget(ctx, a.ID)
		}
		if err := b.speaker(ctx, a.ID); err != nil {
			return nil, err
		}
		return b.connect(ctx, a.ID, name == "connect")
	case "set_route":
		var a struct {
			Route     string          `json:"route"`
			ID        string          `json:"id"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || a.ID != "" && !validID(a.ID) {
			return nil, errInvalid
		}
		if a.Route != "bluetooth" && a.Route != "local" && a.Route != "both" {
			return nil, errInvalid
		}
		if err := b.woken(ctx, a.SessionID); err != nil {
			return nil, err
		}
		if a.Route != "bluetooth" {
			// the board has no output of its own to play beside or instead of the speaker
			return nil, errUnsupported
		}
		return b.setRoute(ctx, a.ID)
	}
	return nil, capability.ErrUnsupported
}

// speaker: id is a paired speaker; a phone is bluetooth's, not the output's
func (b *Bluetooth) speaker(ctx context.Context, id string) error {
	snap, _, err := b.fresh(ctx, false)
	if err != nil {
		return err
	}
	if d, ok := byID(snap, id); !ok || !d.Paired || !isSpeaker(d) {
		return errUnknownDevice
	}
	return nil
}

// scan discovers br/edr devices for d, then answers the speakers it knows of, nearest first
func (b *Bluetooth) scan(ctx context.Context, d time.Duration) (json.RawMessage, error) {
	_, be, err := b.fresh(ctx, true)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.out.scanning {
		b.mu.Unlock()
		return nil, errBusy
	}
	b.out.scanning = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.out.scanning = false
		b.mu.Unlock()
	}()
	if be.SetDiscoveryFilter(ctx, "bredr") != nil || be.StartDiscovery(ctx) != nil {
		b.log.Warn("bluetooth_scan_failed", "code", "start")
		return nil, errUnavailable
	}
	t := time.NewTimer(d)
	select {
	case <-ctx.Done():
	case <-t.C:
	}
	t.Stop()
	// read while still discovering: bluez drops a device's rssi once discovery stops
	snap, serr := be.Snapshot(context.WithoutCancel(ctx))
	_ = be.StopDiscovery(context.WithoutCancel(ctx))
	if serr != nil || snap.Adapter == nil {
		return nil, errUnavailable
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	type found struct {
		DeviceState
		rssi int16
	}
	var fs []found
	for _, dv := range snap.Devices {
		if !isSpeaker(dv) {
			continue
		}
		fs = append(fs, found{DeviceState{ID: ShortID(snap.Adapter.Address, dv.Address), Name: dv.Name,
			Kind: kind(dv.Icon, dv.Class), Paired: dv.Paired, Connected: dv.Connected}, dv.RSSI})
	}
	// an rssi of 0 is none: discovery did not see it this time
	slices.SortStableFunc(fs, func(x, y found) int {
		xr, yr := int(x.rssi), int(y.rssi)
		if xr == 0 {
			xr = -1000
		}
		if yr == 0 {
			yr = -1000
		}
		if xr != yr {
			return yr - xr
		}
		return strings.Compare(x.ID, y.ID)
	})
	out := make([]DeviceState, len(fs))
	for i, f := range fs {
		out[i] = f.DeviceState
	}
	b.log.Info("bluetooth_scan", "found", len(out), "seconds", int(d.Seconds()))
	return json.Marshal(struct {
		Devices []DeviceState `json:"devices"`
	}{out})
}

// pair pairs a speaker as the initiator (our agent accepts it: the owner chose it in a woken session), trusts it and
// connects it
func (b *Bluetooth) pair(ctx context.Context, id string) (json.RawMessage, error) {
	snap, be, err := b.fresh(ctx, true)
	if err != nil {
		return nil, err
	}
	d, ok := byID(snap, id)
	if !ok || !isSpeaker(d) {
		return nil, errUnknownDevice
	}
	if !d.Paired {
		if !b.bondable(ctx, be) {
			b.log.Warn("bluetooth_pair_failed", "id", id, "code", "bondable")
			b.wake()
			return nil, errUnavailable
		}
		b.mu.Lock()
		b.out.outgoing[d.Path] = b.opts.Now().Add(outgoingFor)
		b.mu.Unlock()
		err := be.Pair(ctx, d.Path)
		b.mu.Lock()
		delete(b.out.outgoing, d.Path)
		b.mu.Unlock()
		b.unbondable(context.WithoutCancel(ctx), be)
		if err != nil {
			b.log.Warn("bluetooth_pair_failed", "id", id)
			b.wake()
			return nil, errConnectFailed
		}
		b.log.Info("bluetooth_paired", "id", id, "kind", kind(d.Icon, d.Class), "direction", "outgoing")
	}
	if !d.Trusted && be.SetTrusted(ctx, d.Path, true) != nil {
		b.log.Warn("bluetooth_trust_failed", "id", id)
	}
	b.wake()
	if !d.Connected {
		if be.Connect(ctx, d.Path) != nil {
			b.log.Warn("bluetooth_connect_failed", "id", id)
			return nil, errConnectFailed
		}
		b.log.Info("bluetooth_connected", "id", id)
	}
	b.wake()
	return json.RawMessage(`{"paired":true,"connected":true}`), nil
}

// bondable turns pairable on for an outgoing pair: in bluez it is mgmt's bondable setting, and a pairing made without
// it stores no link key, so the speaker is unpaired after a reboot. PairableTimeout goes first, so bluez turns it off
// again should cadenced die meanwhile. An open window has it on already. Incoming pairings stay with our agent, which
// accepts outside a window only the device we pair to.
func (b *Bluetooth) bondable(ctx context.Context, be Backend) bool {
	b.adapterMu.Lock()
	defer b.adapterMu.Unlock()
	b.mu.Lock()
	b.bonding++
	open := b.window != nil
	b.mu.Unlock()
	if open {
		return true
	}
	if be.SetPairableTimeout(ctx, uint32(outgoingFor/time.Second)) == nil && be.SetPairable(ctx, true) == nil {
		return true
	}
	b.mu.Lock()
	b.bonding--
	b.mu.Unlock()
	b.lockHeld(ctx, be)
	return false
}

// unbondable ends an outgoing pair's turn: the lock is back unless a window opened or another pair runs meanwhile
func (b *Bluetooth) unbondable(ctx context.Context, be Backend) {
	b.adapterMu.Lock()
	defer b.adapterMu.Unlock()
	b.mu.Lock()
	b.bonding--
	b.mu.Unlock()
	b.lockHeld(ctx, be)
}

// outgoingLocked: a pairing we started with this device, still within its time; callers hold mu
func (b *Bluetooth) outgoingLocked(device string) bool {
	if b.out == nil {
		return false
	}
	until, ok := b.out.outgoing[device]
	return ok && b.opts.Now().Before(until)
}

// setRoute plays to the speaker: id, when given, becomes the active one (connected first when it is not)
func (b *Bluetooth) setRoute(ctx context.Context, id string) (json.RawMessage, error) {
	snap, be, err := b.fresh(ctx, true)
	if err != nil {
		return nil, err
	}
	if id == "" {
		b.wake()
		return json.RawMessage(`{"route":"bluetooth"}`), nil
	}
	d, ok := byID(snap, id)
	if !ok || !d.Paired || !isSpeaker(d) {
		return nil, errUnknownDevice
	}
	b.mu.Lock()
	b.out.chosen = id
	b.mu.Unlock()
	if !d.Connected {
		if be.Connect(ctx, d.Path) != nil {
			b.log.Warn("bluetooth_connect_failed", "id", id)
			b.wake()
			return nil, errConnectFailed
		}
		b.log.Info("bluetooth_connected", "id", id)
	}
	b.log.Info("bluetooth_route_set", "id", id)
	b.wake()
	return json.Marshal(struct {
		Route string `json:"route"`
		ID    string `json:"id"`
	}{"bluetooth", id})
}
