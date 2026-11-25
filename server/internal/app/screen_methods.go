package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// screenSettings are what settings.set writes: the device's own settings and its display.screen config
type screenSettings struct {
	effective func(ctx context.Context, deviceID string) (settings.Values, error)
	put       func(ctx context.Context, deviceID string, body []byte) (settings.Overrides, error)
	desired   func(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
	setConfig func(ctx context.Context, deviceID, capability string, cfg json.RawMessage) (int64, error)
	log       *slog.Logger
}

// screenMethods registers settings.set
func screenMethods(register func(name string, m calls.Method), s screenSettings) {
	register(rpc.SettingsSet, s.method())
}

type settingsSetArgs struct {
	Language         string                     `json:"language,omitempty"`
	FollowUpWindowMs int                        `json:"followUpWindowMs,omitempty"`
	Screen           map[string]json.RawMessage `json:"screen,omitempty"`
}

// method serves settings.set {...} (capschema.SettingsSetArgs) for the screen that calls it: language and the
// follow-up window become the device's own setting overrides (validated as the dashboard's are), and language and
// the screen keys are merged into its desired display.screen config, which config sync pushes to it. answers
// {applied: [keys]}; nothing is written when the screen has no display.screen or a write is invalid, and the values
// are never logged
func (s screenSettings) method() calls.Method {
	schema, err := jsonschemax.Compile(capschema.SettingsSetArgs)
	if err != nil {
		panic("settings.set's schema: " + err.Error())
	}
	return func(ctx context.Context, c calls.Call) calls.Reply {
		if _, ok := jsonschemax.Validate(schema, c.Args); !ok {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		var a settingsSetArgs
		if json.Unmarshal(c.Args, &a) != nil {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		cur, err := s.desired(ctx, c.DeviceID, capschema.DisplayScreenID)
		switch {
		case errors.Is(err, devices.ErrUnknownCapability):
			return calls.Fail(rpc.StatusInvalid, "not_a_screen")
		case err != nil:
			return s.failed(c, "desired")
		}
		var applied []string
		patch := map[string]any{}
		if a.Language != "" {
			patch["language"] = a.Language
			applied = append(applied, "language")
		}
		if a.FollowUpWindowMs != 0 {
			v, err := s.effective(ctx, c.DeviceID)
			if err != nil {
				return s.failed(c, "settings")
			}
			f := v.FollowUp
			f.WindowMs = a.FollowUpWindowMs
			patch["followUp"] = f
			applied = append(applied, "followUpWindowMs")
		}
		if len(patch) > 0 {
			body, _ := json.Marshal(patch)
			if _, err := s.put(ctx, c.DeviceID, body); err != nil {
				if errors.Is(err, settings.ErrInvalid) {
					return calls.Fail(rpc.StatusInvalid, "invalid")
				}
				return s.failed(c, "settings")
			}
		}
		if a.Language == "" && len(a.Screen) == 0 {
			return calls.OK(map[string]any{"applied": applied})
		}
		cfg, err := mergeScreen(cur, a)
		if err != nil {
			return s.failed(c, "desired")
		}
		if _, err := s.setConfig(ctx, c.DeviceID, capschema.DisplayScreenID, cfg); err != nil {
			var bad *devices.ConfigInvalidError
			if errors.As(err, &bad) {
				return calls.Fail(rpc.StatusInvalid, "invalid")
			}
			return s.failed(c, "config")
		}
		for k := range a.Screen {
			applied = append(applied, "screen."+k)
		}
		s.log.Info("screen_settings_set", "device_id", c.DeviceID, "rpc_id", c.ID, "keys", len(applied))
		return calls.OK(map[string]any{"applied": applied})
	}
}

func (s screenSettings) failed(c calls.Call, stage string) calls.Reply {
	s.log.Warn("screen_settings_failed", "device_id", c.DeviceID, "rpc_id", c.ID, "stage", stage)
	return calls.Fail(rpc.StatusUnavailable, "unavailable")
}

// mergeScreen is the display.screen config with the write's keys over it: subtitles merged key by key, the rest
// replaced, language set
func mergeScreen(cur json.RawMessage, a settingsSetArgs) (json.RawMessage, error) {
	cfg := map[string]json.RawMessage{}
	if len(cur) > 0 && string(cur) != "null" {
		if err := json.Unmarshal(cur, &cfg); err != nil {
			return nil, err
		}
	}
	for k, v := range a.Screen {
		if k == "subtitles" {
			merged := map[string]json.RawMessage{}
			if old, ok := cfg[k]; ok {
				_ = json.Unmarshal(old, &merged)
			}
			var next map[string]json.RawMessage
			if err := json.Unmarshal(v, &next); err != nil {
				return nil, err
			}
			for sk, sv := range next {
				merged[sk] = sv
			}
			b, err := json.Marshal(merged)
			if err != nil {
				return nil, err
			}
			cfg[k] = b
			continue
		}
		cfg[k] = v
	}
	if a.Language != "" {
		b, _ := json.Marshal(a.Language)
		cfg["language"] = b
	}
	return json.Marshal(cfg)
}
