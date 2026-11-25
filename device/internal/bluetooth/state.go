package bluetooth

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
)

type adapterState struct {
	Present      bool `json:"present"`
	Powered      bool `json:"powered"`
	Pairable     bool `json:"pairable"`
	Discoverable bool `json:"discoverable"`
}

type windowState struct {
	Open             bool `json:"open"`
	RemainingSeconds int  `json:"remainingSeconds"`
}

// DeviceState is a paired or connected device as state and list show it; its name is for the dashboard alone.
type DeviceState struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Playing   bool   `json:"playing"`
}

type state struct {
	Adapter adapterState  `json:"adapter"`
	Window  windowState   `json:"window"`
	Devices []DeviceState `json:"devices"`
	Code    string        `json:"code"`
}

// devices are the paired or connected ones, by id
func devices(snap Snapshot) []DeviceState {
	out := []DeviceState{}
	if snap.Adapter == nil {
		return out
	}
	for _, d := range snap.Devices {
		if !d.Paired && !d.Connected {
			continue
		}
		playing := slices.ContainsFunc(snap.PCMs, func(p PCM) bool {
			return p.Device == d.Path && p.Transport == TransportA2DPSink && p.Running
		})
		out = append(out, DeviceState{
			ID: ShortID(snap.Adapter.Address, d.Address), Name: d.Name, Kind: kind(d.Icon, d.Class),
			Paired: d.Paired, Connected: d.Connected, Playing: playing,
		})
	}
	slices.SortFunc(out, func(x, y DeviceState) int { return strings.Compare(x.ID, y.ID) })
	return out
}

// callers hold mu
func (b *Bluetooth) stateLocked() state {
	st := state{Devices: devices(b.snap), Code: b.code}
	if a := b.snap.Adapter; a != nil {
		st.Adapter = adapterState{Present: true, Powered: a.Powered, Pairable: a.Pairable, Discoverable: a.Discoverable}
	}
	if w := b.window; w != nil {
		left := w.deadline.Sub(b.opts.Now()).Seconds()
		st.Window = windowState{Open: true, RemainingSeconds: int(math.Max(0, math.Ceil(left)))}
	}
	return st
}

// publishLocked offers the state when it changed; callers hold mu
func (b *Bluetooth) publishLocked() {
	b.publishOutputLocked()
	st := b.stateLocked()
	raw, err := json.Marshal(st)
	if err != nil || string(raw) == b.shown {
		return
	}
	if b.shown == "" || st.Code != b.shownCode {
		b.log.Info("bluetooth_state", "code", st.Code, "devices", len(st.Devices))
	}
	b.shown, b.shownCode = string(raw), st.Code
	offer(b.state, json.RawMessage(raw))
}

func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
