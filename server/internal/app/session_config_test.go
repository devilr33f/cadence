package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// sessionRig is one device's settings and stored configs, by capability
type sessionRig struct {
	global, device settings.Overrides
	configs        map[string]json.RawMessage
	sets           []string
	logs           bytes.Buffer
}

func (r *sessionRig) zones() *alarmZones {
	return newAlarmZones(zoneDeps{
		Global: func(context.Context) (settings.Overrides, error) { return r.global, nil },
		Device: func(context.Context, string) (settings.Overrides, error) { return r.device, nil },
		Config: func(_ context.Context, _, capability string) (json.RawMessage, error) {
			c, ok := r.configs[capability]
			if !ok {
				return nil, devices.ErrUnknownCapability
			}
			return c, nil
		},
		SetConfig: func(_ context.Context, _, capability string, cfg json.RawMessage) (int64, error) {
			if _, ok := r.configs[capability]; !ok {
				return 0, devices.ErrUnknownCapability
			}
			r.configs[capability] = cfg
			r.sets = append(r.sets, capability+" "+string(cfg))
			return int64(len(r.sets)), nil
		},
		Log:     slog.New(slog.NewTextHandler(&r.logs, nil)),
		Session: true,
	})
}

func TestSessionConfigFollowsTheSettings(t *testing.T) {
	r := &sessionRig{global: settings.Overrides{}, device: settings.Overrides{}, configs: map[string]json.RawMessage{"session": json.RawMessage(`{}`)}}
	z := r.zones()
	z.Sync(context.Background(), "d1")
	// the defaults reach the device, so they hold offline as the server has them
	if len(r.sets) != 1 || r.sets[0] != `session {"errorSound":"prompt","language":"en"}` {
		t.Fatalf("sets = %v", r.sets)
	}
	z.Sync(context.Background(), "d1")
	if len(r.sets) != 1 {
		t.Fatalf("an unchanged config was set again: %v", r.sets)
	}
	r.global["language"] = json.RawMessage(`"ru-RU"`)
	r.device["errorSound"] = json.RawMessage(`"beep"`)
	z.Sync(context.Background(), "d1")
	if len(r.sets) != 2 || r.sets[1] != `session {"errorSound":"beep","language":"ru"}` {
		t.Fatalf("sets = %v", r.sets)
	}
}

func TestSessionConfigSkipsADeviceWithoutIt(t *testing.T) {
	r := &sessionRig{global: settings.Overrides{}, device: settings.Overrides{}, configs: map[string]json.RawMessage{}}
	r.zones().Sync(context.Background(), "d1")
	if len(r.sets) != 0 || bytes.Contains(r.logs.Bytes(), []byte("failed")) {
		t.Fatalf("sets = %v, logs %s", r.sets, r.logs.String())
	}
}

func TestPromptLanguage(t *testing.T) {
	for in, want := range map[string]string{"ru": "ru", "en-US": "en", "RU": "ru", "fil": "fil", "x": "", "": "", "zh_Hant": "", "1a": ""} {
		if got := promptLanguage(in); got != want {
			t.Errorf("promptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
