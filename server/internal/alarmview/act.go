package alarmview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// ActDeps are what alarms.act reaches.
type ActDeps struct {
	// Command runs a capability command on a device (commands.Service.Send)
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	Log     *slog.Logger
}

type actArgs struct {
	DeviceID string `json:"deviceId"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	Minutes  int    `json:"minutes,omitempty"`
}

// ActMethod serves alarms.act {deviceId, kind, id, action, minutes?}: a screen's press. stop and snooze end or put off
// a ring set in a woken session, which a screen may do for any device (owner, 2026-11-16); cancel takes a timer or a
// reminder off its device. An alarm is never set, nor cancelled, from a screen (rule 7). Answers {} or refused {code}.
func ActMethod(d ActDeps) calls.Method {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, c calls.Call) calls.Reply {
		a, name, args, ok := readAct(c.Args)
		if !ok {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		_, err := d.Command(ctx, a.DeviceID, Capability, name, args)
		code := refusal(err)
		d.Log.Info("alarms_act", "device_id", c.DeviceID, "target_id", a.DeviceID, "action", a.Action, "kind", a.Kind,
			"code", cmpOr(code, "ok"))
		if code != "" {
			return calls.Fail(rpc.StatusRefused, code)
		}
		return calls.OK(struct{}{})
	}
}

// readAct checks the press and names the device command it is: stop_ring, snooze, cancel_timer or cancel_reminder
func readAct(raw json.RawMessage) (actArgs, string, []byte, bool) {
	var a actArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&a) != nil || dec.More() || a.DeviceID == "" || len(a.DeviceID) > 64 || !token(a.Kind) || !token(a.ID) {
		return a, "", nil, false
	}
	ring := map[string]any{"kind": a.Kind, "id": a.ID}
	var name string
	switch a.Action {
	case "stop":
		if a.Minutes != 0 {
			return a, "", nil, false
		}
		name = "stop_ring"
	case "snooze":
		if a.Minutes < 0 || a.Minutes > 60 {
			return a, "", nil, false
		}
		if a.Minutes > 0 {
			ring["minutes"] = a.Minutes
		}
		name = "snooze"
	case "cancel":
		if a.Minutes != 0 || (a.Kind != KindTimer && a.Kind != KindReminder) {
			return a, "", nil, false
		}
		name, ring = "cancel_"+a.Kind, map[string]any{"id": a.ID}
	default:
		return a, "", nil, false
	}
	args, err := json.Marshal(ring)
	return a, name, args, err == nil
}

// token is a kind or an id as the device writes them: [a-z0-9], 1 to 16
func token(s string) bool {
	if s == "" || len(s) > maxToken {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// refusal is the press's refusal code, "" for none
func refusal(err error) string {
	var dev *commands.DeviceError
	var bad *commands.InvalidArgumentsError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, commands.ErrOffline):
		return "offline"
	case errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return "unsupported"
	case errors.Is(err, devices.ErrNotFound):
		return "not_found"
	case errors.As(err, &bad):
		return "invalid"
	case errors.As(err, &dev) && dev.Code != "":
		switch dev.Code {
		case "not_found", "invalid", "unsupported", "wake_required":
			return dev.Code
		}
	}
	return "failed"
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
