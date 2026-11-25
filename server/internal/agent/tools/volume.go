package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// "exactly one of level and change" is Run's check: a oneOf here would refuse
// first, in words the model cannot act on
const volumeInput = `{
	"type": "object",
	"properties": {
		"level": {"type": "integer", "minimum": 0, "maximum": 100},
		"change": {"type": "integer", "minimum": -100, "maximum": 100},
		` + deviceProp + `
	},
	"additionalProperties": false
}`

type VolumeDeps struct {
	State func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	// Command runs a capability command on the device and returns its result.
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// Target resolves the device argument; nil: this device alone
	Target TargetFunc
}

// DeviceVolume is the only code path where the agent moves a device's volume
// (rule 7): a raise needs a session the user began with the wake word or a
// press, on the target as on this device, and any change on another device
// needs one too (the cross-device policy). The device owns the level and
// checks the session again itself.
func DeviceVolume(d VolumeDeps) agent.Tool {
	return agent.Tool{
		Name: "device_volume",
		Description: "Set the speaker volume: level 0-100, or change (-100..100) relative to now. " +
			"Give exactly one. " + deviceHint + "Say nothing after it succeeds. If it fails with wake_required, " +
			"tell the user to wake you first.",
		Input:   volumeInput,
		Effects: agent.Write, Output: agent.Trusted,
		SilentOnSuccess: true,
		// past the commands service's own 10 s, so its timeout is the one that answers
		Timeout: 12 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Level  *int   `json:"level"`
				Change *int   `json:"change"`
				Device string `json:"device"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			if (a.Level == nil) == (a.Change == nil) {
				return nil, badArgs("Give exactly one of level and change.")
			}
			t, terr := target(ctx, d.Target, c, a.Device, agent.Need{Capability: agent.CapAudioOutput, Write: true})
			if terr != nil {
				return nil, terr
			}
			ctx, c = vouched(ctx, c, t), on(c, t)
			if a.Change != nil {
				return changeLevel(ctx, d, c, *a.Change)
			}
			return setLevel(ctx, d, c, clamp(*a.Level))
		},
	}
}

type volumeArgs struct {
	Level     *int   `json:"level,omitempty"`
	Delta     *int   `json:"delta,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

func setLevel(ctx context.Context, d VolumeDeps, c agent.Call, level int) (json.RawMessage, *agent.ToolError) {
	cur, known := currentLevel(ctx, d, c.DeviceID)
	// an unknown level cannot rule out a raise
	if (!known || level > cur) && !agent.Woken(c.Trigger) {
		return nil, agent.WakeRequired()
	}
	return volumeCommand(ctx, d, c.DeviceID, "set_level", volumeArgs{Level: &level, SessionID: c.SessionID}, true)
}

// changeLevel leaves the change to the device, which resolves it against its
// own level under its lock: the server's copy may be stale, and a "quieter"
// resolved here could land above the level now.
func changeLevel(ctx context.Context, d VolumeDeps, c agent.Call, delta int) (json.RawMessage, *agent.ToolError) {
	if delta > 0 && !agent.Woken(c.Trigger) {
		return nil, agent.WakeRequired()
	}
	// a change that may still land must not be sent again
	return volumeCommand(ctx, d, c.DeviceID, "change_level", volumeArgs{Delta: &delta, SessionID: c.SessionID}, false)
}

// volumeCommand answers the level the device reports it set.
func volumeCommand(ctx context.Context, d VolumeDeps, deviceID, name string, args volumeArgs, retryable bool) (json.RawMessage, *agent.ToolError) {
	raw, _ := json.Marshal(args)
	res, err := d.Command(ctx, deviceID, "audio.output", name, raw)
	if err != nil {
		return nil, volumeError(err, retryable)
	}
	var out struct {
		Level *int `json:"level"`
	}
	if json.Unmarshal(res, &out) != nil || out.Level == nil {
		return nil, volumeFailed(retryable)
	}
	return encode(map[string]int{"level": *out.Level})
}

// volumeError is the model's view of a failed command: never device text.
func volumeError(err error, retryable bool) *agent.ToolError {
	var device *commands.DeviceError
	switch {
	case errors.As(err, &device) && device.Code == "wake_required":
		return agent.WakeRequired()
	case errors.Is(err, devices.ErrNotFound), errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return unavailable("The device has no volume control.")
	case errors.Is(err, commands.ErrOffline):
		return &agent.ToolError{Code: "device_offline", Message: "The speaker is offline."}
	case errors.Is(err, commands.ErrTimeout):
		return &agent.ToolError{Code: "timeout", Message: "The speaker did not answer.", Retryable: retryable}
	}
	return volumeFailed(retryable)
}

func volumeFailed(retryable bool) *agent.ToolError {
	return &agent.ToolError{Code: "volume_failed", Message: "The speaker did not change its volume.", Retryable: retryable}
}

func currentLevel(ctx context.Context, d VolumeDeps, deviceID string) (int, bool) {
	raw, ok := d.State(ctx, deviceID, "audio.output")
	if !ok {
		return 0, false
	}
	var st struct {
		Level *int `json:"level"`
	}
	if json.Unmarshal(raw, &st) != nil || st.Level == nil {
		return 0, false
	}
	return clamp(*st.Level), true
}

func clamp(n int) int { return min(max(n, 0), 100) }
