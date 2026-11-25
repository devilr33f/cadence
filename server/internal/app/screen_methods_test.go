package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

type settingsFake struct {
	puts    []string
	configs map[string]string
	putErr  error
}

func (f *settingsFake) deps() screenSettings {
	return screenSettings{
		effective: func(context.Context, string) (settings.Values, error) {
			return settings.Values{FollowUp: settings.FollowUp{Enabled: true, WindowMs: 5000, Limit: 3}}, nil
		},
		put: func(_ context.Context, _ string, body []byte) (settings.Overrides, error) {
			if f.putErr != nil {
				return nil, f.putErr
			}
			f.puts = append(f.puts, string(body))
			return settings.Overrides{}, nil
		},
		desired: func(_ context.Context, id, capability string) (json.RawMessage, error) {
			cfg, ok := f.configs[id]
			if !ok || capability != capschema.DisplayScreenID {
				return nil, devices.ErrUnknownCapability
			}
			if cfg == "" {
				return nil, nil
			}
			return json.RawMessage(cfg), nil
		},
		setConfig: func(_ context.Context, id, _ string, cfg json.RawMessage) (int64, error) {
			f.configs[id] = string(cfg)
			return 2, nil
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func methodsOf(s screenSettings) map[string]calls.Method {
	m := map[string]calls.Method{}
	screenMethods(func(name string, fn calls.Method) { m[name] = fn }, s)
	return m
}

func callOn(m map[string]calls.Method, dev, method, args string) calls.Reply {
	return m[method](context.Background(), calls.Call{DeviceID: dev, ID: "1", Method: method, Args: json.RawMessage(args)})
}

// a screen's settings page writes the device's own settings and its display.screen config, which config sync then
// pushes: the language both, the follow-up window into followUp, the screen keys merged over what was set
func TestSettingsSetWritesTheDevicesSettings(t *testing.T) {
	f := &settingsFake{configs: map[string]string{
		"tv": `{"theme":"afterglow","subtitles":{"size":"s","style":"box"},"overscan":0}`, "fresh-tv": "",
	}}
	m := methodsOf(f.deps())
	rep := callOn(m, "tv", rpc.SettingsSet,
		`{"language":"ru","followUpWindowMs":8000,"screen":{"subtitles":{"size":"l"},"overscan":2.5,"reducedMotion":true}}`)
	if rep.Status != rpc.StatusOK {
		t.Fatalf("set %+v", rep)
	}
	if len(f.puts) != 1 || f.puts[0] != `{"followUp":{"enabled":true,"windowMs":8000,"limit":3},"language":"ru"}` {
		t.Fatalf("settings %q", f.puts)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.configs["tv"]), &cfg); err != nil {
		t.Fatal(err)
	}
	sub, _ := cfg["subtitles"].(map[string]any)
	if cfg["theme"] != "afterglow" || cfg["language"] != "ru" || cfg["overscan"] != 2.5 || cfg["reducedMotion"] != true ||
		sub["size"] != "l" || sub["style"] != "box" {
		t.Fatalf("config %s", f.configs["tv"])
	}
	applied, _ := json.Marshal(rep.JSON)
	if !strings.Contains(string(applied), `"language"`) || !strings.Contains(string(applied), `"screen.overscan"`) {
		t.Fatalf("applied %s", applied)
	}
	// the window alone leaves the config untouched; a screen with no config yet gets one
	before := f.configs["tv"]
	if rep := callOn(m, "tv", rpc.SettingsSet, `{"followUpWindowMs":3000}`); rep.Status != rpc.StatusOK || f.configs["tv"] != before {
		t.Fatalf("window alone %+v %s", rep, f.configs["tv"])
	}
	if rep := callOn(m, "fresh-tv", rpc.SettingsSet, `{"screen":{"autoSkipOpenings":true}}`); rep.Status != rpc.StatusOK ||
		f.configs["fresh-tv"] != `{"autoSkipOpenings":true}` {
		t.Fatalf("fresh %+v %s", rep, f.configs["fresh-tv"])
	}
}

func TestSettingsSetRefusals(t *testing.T) {
	f := &settingsFake{configs: map[string]string{"tv": "{}"}}
	m := methodsOf(f.deps())
	for args, want := range map[string]string{
		`{"screen":{"theme":"x"}}`: rpc.StatusInvalid,
		`{"followUpWindowMs":900}`: rpc.StatusInvalid,
		`{}`:                       rpc.StatusInvalid,
	} {
		if rep := callOn(m, "tv", rpc.SettingsSet, args); rep.Status != want {
			t.Errorf("%s: %+v, want %s", args, rep, want)
		}
	}
	// a device with no screen writes nothing
	if rep := callOn(m, "speaker", rpc.SettingsSet, `{"language":"ru"}`); rep.Status != rpc.StatusInvalid || len(f.puts) != 0 {
		t.Fatalf("speaker %+v %q", rep, f.puts)
	}
	// a setting the store refuses leaves the config alone
	f.putErr = settings.ErrInvalid
	if rep := callOn(m, "tv", rpc.SettingsSet, `{"language":"ru"}`); rep.Status != rpc.StatusInvalid || f.configs["tv"] != "{}" {
		t.Fatalf("refused %+v %s", rep, f.configs["tv"])
	}
}
