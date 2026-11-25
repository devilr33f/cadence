package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// sessionCapability is the device's own session settings: how it tells the owner a request failed, and in which
// language its spoken prompts are
const sessionCapability = "session"

// sessionConfig is the session capability's config
type sessionConfig struct {
	ErrorSound string `json:"errorSound"`
	Language   string `json:"language"`
}

// syncSessionConfig keeps the device's session config equal to its effective errorSound and language settings,
// defaults included, so it holds offline as the server would have it. A device without the capability gets nothing;
// an unchanged config adds no generation. Runs beside the alarms zone's check, under its lock and deadline.
func (z *alarmZones) syncSessionConfig(ctx context.Context, deviceID string) {
	failed := func(reason string) { z.d.Log.Warn("session_config_failed", "device_id", deviceID, "reason", reason) }
	stored, err := z.d.Config(ctx, deviceID, sessionCapability)
	if errors.Is(err, devices.ErrUnknownCapability) {
		return
	}
	if err != nil {
		failed("store")
		return
	}
	global, err := z.d.Global(ctx)
	if err != nil {
		failed("settings")
		return
	}
	own, err := z.d.Device(ctx, deviceID)
	if err != nil {
		failed("settings")
		return
	}
	v, err := settings.Merge(global, own)
	if err != nil {
		failed("settings")
		return
	}
	want := sessionConfig{ErrorSound: v.ErrorSound, Language: promptLanguage(v.Language)}
	var cur sessionConfig
	if json.Unmarshal(stored, &cur) == nil && cur == want {
		return
	}
	cfg, err := json.Marshal(want)
	if err != nil {
		return
	}
	gen, err := z.d.SetConfig(ctx, deviceID, sessionCapability, cfg)
	var invalid *devices.ConfigInvalidError
	switch {
	case errors.As(err, &invalid):
		failed("invalid")
	case errors.Is(err, devices.ErrUnknownCapability):
	case err != nil:
		failed("store")
	default:
		z.d.Log.Info("session_config_set", "device_id", deviceID, "generation", gen)
	}
}

// promptLanguage is a language setting's primary subtag in lower case ("ru-RU" is ru), the device's prompt directory;
// one the device cannot take falls back to its default
func promptLanguage(lang string) string {
	primary, _, _ := strings.Cut(strings.ToLower(lang), "-")
	if len(primary) < 2 || len(primary) > 3 {
		return ""
	}
	for _, r := range primary {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return primary
}
