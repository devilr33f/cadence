package bluetooth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/receivername"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	maxSessionID = 63

	sessionIDSchema = `{"type":"string","minLength":1,"maxLength":63}`
	idSchema        = `{"type":"string","pattern":"^[0-9a-f]{12}$"}`

	pairWindowSchema = `{"type":"object","properties":{"seconds":{"type":"integer","minimum":30,"maximum":300},` +
		`"sessionId":` + sessionIDSchema + `},"additionalProperties":false}`
	deviceArgsSchema = `{"type":"object","properties":{"id":` + idSchema + `,"sessionId":` + sessionIDSchema + `},` +
		`"required":["id"],"additionalProperties":false}`
	forgetSchema = `{"type":"object","properties":{"id":` + idSchema + `,"all":{"const":true},"sessionId":` + sessionIDSchema + `},` +
		`"oneOf":[{"required":["id"]},{"required":["all"]}],"additionalProperties":false}`
	listSchema = `{"type":"object","additionalProperties":false}`
)

var (
	errInvalid       = &capability.CommandError{Code: "invalid"}
	errWakeRequired  = &capability.CommandError{Code: "wake_required"}
	errUnavailable   = &capability.CommandError{Code: "unavailable"}
	errReceiveOff    = &capability.CommandError{Code: "receive_off"}
	errUnknownDevice = &capability.CommandError{Code: "unknown_device"}
	errConnectFailed = &capability.CommandError{Code: "connect_failed"}
)

func commands() map[string]string {
	return map[string]string{
		"pair_window": pairWindowSchema, "connect": deviceArgsSchema, "disconnect": deviceArgsSchema,
		"forget": forgetSchema, "list": listSchema,
	}
}

func (b *Bluetooth) command(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "list":
		if decodeObject(raw, &struct{}{}) != nil {
			return nil, errInvalid
		}
		snap, _, err := b.fresh(ctx, false)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Devices []DeviceState `json:"devices"`
		}{devices(snap)})
	case "pair_window":
		var a struct {
			Seconds   *int            `json:"seconds"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || a.Seconds != nil && (*a.Seconds < minWindow || *a.Seconds > maxWindow) {
			return nil, errInvalid
		}
		if err := b.woken(ctx, a.SessionID); err != nil {
			return nil, err
		}
		return b.pairWindow(ctx, a.Seconds)
	case "connect", "disconnect":
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
		return b.connect(ctx, a.ID, name == "connect")
	case "forget":
		var a struct {
			ID        *string         `json:"id"`
			All       *bool           `json:"all"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || (a.ID == nil) == (a.All == nil) || a.ID != nil && !validID(*a.ID) ||
			a.All != nil && !*a.All {
			return nil, errInvalid
		}
		if err := b.woken(ctx, a.SessionID); err != nil {
			return nil, err
		}
		id := ""
		if a.ID != nil {
			id = *a.ID
		}
		return b.forget(ctx, id)
	}
	return nil, capability.ErrUnsupported
}

func validID(id string) bool {
	return len(id) == 12 && strings.Trim(id, "0123456789abcdef") == ""
}

// woken: every write needs a session begun with the wake word or a press, on this device or vouched for by the
// server (rule 7); a follow-up, an unknown session or none is refused
func (b *Bluetooth) woken(ctx context.Context, raw json.RawMessage) error {
	// no session at all is the owner's authenticated call (the dashboard, its chat), as audio.output takes it: the
	// server sends a turn's session whenever it has one, and refuses a follow-up's write before it sends anything
	if raw == nil {
		return nil
	}
	if raw[0] != '"' {
		return errInvalid
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > maxSessionID {
		return errInvalid
	}
	t, ok := capability.SessionTrigger(ctx, b.opts.Sessions, id)
	if !ok || t != cadencev2.Trigger_TRIGGER_WAKE && t != cadencev2.Trigger_TRIGGER_BUTTON {
		return errWakeRequired
	}
	return nil
}

// fresh reads the backend now; enabled also needs the config on and the adapter present
func (b *Bluetooth) fresh(ctx context.Context, enabled bool) (Snapshot, Backend, error) {
	b.mu.Lock()
	be, cfg := b.backend, b.cfg
	b.mu.Unlock()
	if be == nil || enabled && !cfg.Enabled {
		return Snapshot{}, nil, errUnavailable
	}
	snap, err := be.Snapshot(ctx)
	if err != nil || snap.Adapter == nil {
		return Snapshot{}, nil, errUnavailable
	}
	return snap, be, nil
}

// pairWindow opens the window: bluez's own timeouts first, so it closes even should cadenced die with it open
func (b *Bluetooth) pairWindow(ctx context.Context, seconds *int) (json.RawMessage, error) {
	_, be, err := b.fresh(ctx, true)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	cfg := b.cfg
	alias := receivername.Pick(cfg.Name, b.device, maxName)
	b.mu.Unlock()
	if !cfg.Receive {
		return nil, errReceiveOff
	}
	n := cfg.PairWindowSeconds
	if seconds != nil {
		n = *seconds
	}
	b.adapterMu.Lock()
	b.mu.Lock()
	// open before bluez says so: the agent accepts from the first request
	b.window = &window{deadline: b.opts.Now().Add(time.Duration(n) * time.Second)}
	b.mu.Unlock()
	if be.SetWindowTimeouts(ctx, uint32(n)) != nil || be.SetPairable(ctx, true) != nil || be.SetDiscoverable(ctx, true) != nil { //nolint:gosec // 30-300
		b.mu.Lock()
		b.window = nil
		b.mu.Unlock()
		b.lockHeld(ctx, be)
		b.adapterMu.Unlock()
		b.log.Warn("bluetooth_window_failed")
		b.wake()
		return nil, errUnavailable
	}
	b.adapterMu.Unlock()
	b.log.Info("bluetooth_window_open", "seconds", n)
	b.wake()
	// the name phones see, for the spoken confirmation: in the result alone, never a log
	return json.Marshal(struct {
		Seconds int    `json:"seconds"`
		Name    string `json:"name"`
	}{n, alias})
}

func (b *Bluetooth) connect(ctx context.Context, id string, on bool) (json.RawMessage, error) {
	snap, be, err := b.fresh(ctx, true)
	if err != nil {
		return nil, err
	}
	d, ok := byID(snap, id)
	if !ok || !d.Paired {
		return nil, errUnknownDevice
	}
	if on {
		if be.Connect(ctx, d.Path) != nil {
			b.log.Warn("bluetooth_connect_failed", "id", id)
			return nil, errConnectFailed
		}
		b.log.Info("bluetooth_connected", "id", id)
		b.wake()
		return json.RawMessage(`{"connected":true}`), nil
	}
	if be.Disconnect(ctx, d.Path) != nil {
		b.log.Warn("bluetooth_disconnect_failed", "id", id)
		return nil, errUnavailable
	}
	b.log.Info("bluetooth_disconnected", "id", id)
	b.wake()
	return json.RawMessage(`{"connected":false}`), nil
}

// forget removes one paired device, or every one ("" for all); bluez drops its link key with it
func (b *Bluetooth) forget(ctx context.Context, id string) (json.RawMessage, error) {
	snap, be, err := b.fresh(ctx, false)
	if err != nil {
		return nil, err
	}
	var todo []Device
	if id != "" {
		d, ok := byID(snap, id)
		if !ok || !d.Paired {
			return nil, errUnknownDevice
		}
		todo = []Device{d}
	} else {
		for _, d := range snap.Devices {
			if d.Paired {
				todo = append(todo, d)
			}
		}
	}
	removed := 0
	for _, d := range todo {
		if be.Remove(ctx, d.Path) == nil {
			removed++
		}
	}
	b.log.Info("bluetooth_forget", "removed", removed, "kept", len(todo)-removed)
	b.wake()
	if id != "" && removed == 0 {
		return nil, errUnavailable
	}
	return fmt.Appendf(nil, `{"forgotten":%d}`, removed), nil
}

func byID(snap Snapshot, id string) (Device, bool) {
	for _, d := range snap.Devices {
		if ShortID(snap.Adapter.Address, d.Address) == id {
			return d, true
		}
	}
	return Device{}, false
}
