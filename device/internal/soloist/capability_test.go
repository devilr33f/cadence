package soloist_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func TestDescriptorDeclaresTheKeyAsASecret(t *testing.T) {
	h := newHarness(t)
	d := h.cap.Descriptor()
	if d.ID != "media.soloist" || d.Version != 1 || len(d.Commands) != 0 {
		t.Fatalf("descriptor = %+v", d)
	}
	if len(d.Secrets) != 1 || d.Secrets[0].Name != "apiKey" || d.Secrets[0].Description == "" {
		t.Fatalf("secrets = %+v", d.Secrets)
	}
	for _, schema := range []string{d.ConfigSchema, d.StateSchema} {
		var v map[string]any
		if err := json.Unmarshal([]byte(schema), &v); err != nil || v["additionalProperties"] != false {
			t.Fatalf("schema %s: %v", schema, err)
		}
	}
	if _, err := h.cap.Command(t.Context(), "pause", nil); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("command = %v", err)
	}
}

func TestConfigIsCheckedStrictly(t *testing.T) {
	h := newHarness(t)
	for _, cfg := range []string{
		`{}`, `{"enabled":false}`, `{"enabled":true,"deviceName":"Kitchen 1"}`, `{"deviceName":"a"}`,
		`{"deviceName":"` + strings.Repeat("x", 32) + `"}`, `{"deviceName":"A.b_c-d e"}`,
	} {
		if err := h.apply(cfg, nil); err != nil {
			t.Fatalf("%s: %v", cfg, err)
		}
	}
	for _, cfg := range []string{
		``, `[]`, `{"enabled":"yes"}`, `{"enabled":true,"volume":3}`, `{"deviceName":""}`,
		`{"deviceName":"` + strings.Repeat("x", 33) + `"}`, `{"deviceName":" Kitchen"}`, `{"deviceName":"Kitchen "}`,
		`{"deviceName":"Küche"}`, `{"deviceName":"a/b"}`, `{"deviceName":null}`, `{} {}`, `{"enabled":true}x`,
	} {
		var ae *capability.ApplyError
		if err := h.apply(cfg, nil); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("%q: err = %v", cfg, err)
		}
	}
}

// the key is the owner's api key (reference §1.5): 1-256 bytes of 0x21-0x7e,
// with one trailing newline dropped; any other is key_missing, and audiod
// gets soloist off with no key at all
func TestKeyValidation(t *testing.T) {
	long := strings.Repeat("k", 256)
	for _, c := range []struct {
		name    string
		secrets map[string]string
		key     string // what audiod gets; "" for none
		code    string // key_missing's code; "" for a valid key
	}{
		{"none", nil, "", "missing"},
		{"absent", map[string]string{}, "", "missing"},
		{"empty", map[string]string{"apiKey": ""}, "", "missing"},
		{"one byte", map[string]string{"apiKey": "k"}, "k", ""},
		{"256 bytes", map[string]string{"apiKey": long}, long, ""},
		{"257 bytes", map[string]string{"apiKey": long + "k"}, "", "invalid"},
		{"newline", map[string]string{"apiKey": testKey + "\n"}, testKey, ""},
		{"crlf", map[string]string{"apiKey": testKey + "\r\n"}, testKey, ""},
		{"256 and a newline", map[string]string{"apiKey": long + "\n"}, long, ""},
		{"two newlines", map[string]string{"apiKey": testKey + "\n\n"}, "", "invalid"},
		{"only a newline", map[string]string{"apiKey": "\n"}, "", "invalid"},
		{"a bare cr", map[string]string{"apiKey": testKey + "\r"}, "", "invalid"},
		{"space", map[string]string{"apiKey": "ab cd"}, "", "invalid"},
		{"del", map[string]string{"apiKey": "ab\x7f"}, "", "invalid"},
		{"tab", map[string]string{"apiKey": "\tab"}, "", "invalid"},
		{"non-ascii", map[string]string{"apiKey": "ключ"}, "", "invalid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.ready()
			h.mustApply(`{"enabled":true,"deviceName":"Den"}`, c.secrets)
			cfgs := h.a.configs()
			last := cfgs[len(cfgs)-1]
			if last.GetApiKey() != c.key || last.GetEnabled() != (c.key != "") || last.GetDeviceName() != "Den" {
				t.Fatalf("config enabled=%v key bytes=%d name=%q", last.GetEnabled(), len(last.GetApiKey()), last.GetDeviceName())
			}
			if c.code != "" {
				h.waitState(`{"state":"key_missing","code":"` + c.code + `"}`)
				h.waitLog(`"msg":"soloist_key_missing","reason":"` + c.code + `"`)
			} else {
				h.waitState(`{"state":"starting"}`)
			}
		})
	}
}

// soloist off needs no key, and none goes to audiod
func TestDisabledSendsNoKey(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.mustApply(`{"enabled":false}`, map[string]string{"apiKey": testKey})
	h.waitState(`{"state":"off"}`)
	for _, c := range h.a.configs() {
		if c.GetEnabled() || c.GetApiKey() != "" {
			t.Fatalf("config enabled=%v key bytes=%d", c.GetEnabled(), len(c.GetApiKey()))
		}
	}
}

func TestStateFollowsAudiodStatus(t *testing.T) {
	h := newHarness(t)
	h.waitState(`{"state":"off"}`)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.waitState(`{"state":"starting"}`)
	const epoch = 1793059327 // 2026-10-27, expires 2027-01-25
	for _, c := range []struct {
		st   *audiodv1.SoloistStatus
		want string
	}{
		// a status from before audiod took the enabling config
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_OFF}, `{"state":"starting"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_NO_BUILD}, `{"state":"no_build"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_STARTING, BuildEpoch: epoch, Version: "1.3.10.18"},
			`{"state":"starting","build":"20261027","expiresAt":"2027-01-25T00:02:07Z"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_READY, BuildEpoch: epoch}, `{"state":"ready","build":"20261027","expiresAt":"2027-01-25T00:02:07Z"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_FAILED, Code: "auth_failed", BuildEpoch: epoch},
			`{"state":"failed","code":"auth_failed","build":"20261027","expiresAt":"2027-01-25T00:02:07Z"}`},
		// a code is a token, or nothing
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_FAILED, Code: "Upstream said: no"}, `{"state":"failed"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_EXPIRED, ExitCode: 10}, `{"state":"expired"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_KEY_MISSING, Code: "invalid"}, `{"state":"key_missing","code":"invalid"}`},
		{&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_STARTING, SilentStarts: 2}, `{"state":"starting"}`},
	} {
		h.status(c.st)
		h.waitState(c.want)
	}
	h.s.SoloistGone()
	h.waitState(`{"state":"starting"}`)
	h.mustApply(`{"enabled":false}`, nil)
	h.waitState(`{"state":"off"}`)
	if !strings.Contains(h.logs.String(), `"msg":"soloist_state","state":"no_build"`) {
		t.Fatalf("log: %s", h.logs)
	}
}

// audiod forgets soloist with its client: every Ready gets the config, then
// the level when the device has one, and every change gets the config again
func TestConfigFollowsEveryReadyAndChange(t *testing.T) {
	h := newHarness(t, withLevel(35))
	h.mustApply(`{"enabled":true,"deviceName":"Den"}`, map[string]string{"apiKey": testKey})
	if n := len(h.a.all()); n != 0 {
		t.Fatalf("%d envelopes before audiod was up", n)
	}
	h.ready()
	if got := h.a.kinds(); !slices.Equal(got, []string{"soloist_config", "sender_volume_set", "sender_volume_set"}) {
		t.Fatalf("after ready: %v", got)
	}
	c := h.a.configs()[0]
	if !c.GetEnabled() || c.GetApiKey() != testKey || c.GetDeviceName() != "Den" || c.GetDir() != h.opts.Dir ||
		c.GetScratchDir() != h.opts.ScratchDir || c.GetCaFile() != h.opts.CAFile || c.GetInitialVolume() != 35 {
		t.Fatalf("config enabled=%v name=%q dir=%q scratch=%q ca=%q initial=%d", c.GetEnabled(), c.GetDeviceName(),
			c.GetDir(), c.GetScratchDir(), c.GetCaFile(), c.GetInitialVolume())
	}
	if got := h.a.levels(); !slices.Equal(got, []uint32{35}) {
		t.Fatalf("levels = %v", got)
	}
	h.mustApply(`{"enabled":true,"deviceName":"Hall"}`, map[string]string{"apiKey": testKey})
	if cfgs := h.a.configs(); len(cfgs) != 2 || cfgs[1].GetDeviceName() != "Hall" {
		t.Fatalf("configs after a change: %d", len(cfgs))
	}
	h.s.SoloistGone()
	h.mustApply(`{"enabled":true,"deviceName":"Attic"}`, map[string]string{"apiKey": testKey})
	if cfgs := h.a.configs(); len(cfgs) != 2 {
		t.Fatalf("a config went out with audiod down: %d", len(cfgs))
	}
	h.ready()
	if got := h.a.kinds(); !slices.Equal(got[4:], []string{"soloist_config", "sender_volume_set", "sender_volume_set"}) || h.a.configs()[2].GetDeviceName() != "Attic" {
		t.Fatalf("after the second ready: %v", got)
	}
}

// a device with no level of its own tells soloist none, and starts it at 50
func TestUnknownLevelSendsNone(t *testing.T) {
	h := newHarness(t)
	h.ready()
	if got := h.a.kinds(); !slices.Equal(got, []string{"soloist_config"}) {
		t.Fatalf("after ready: %v", got)
	}
	if c := h.a.configs()[0]; c.GetInitialVolume() != 50 || c.GetDeviceName() != "Cadence" || c.GetEnabled() {
		t.Fatalf("config initial=%d name=%q enabled=%v", c.GetInitialVolume(), c.GetDeviceName(), c.GetEnabled())
	}
}

// review focus 3: the key goes to audiod in SoloistConfig alone, never into a
// log, a state, an error or any other envelope
func TestKeyNeverLeaves(t *testing.T) {
	h := newHarness(t, withLevel(40))
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey + "\n"})
	h.status(&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_READY, BuildEpoch: 1793059327})
	h.senderVolume(60)
	h.waitLog(`"msg":"media_volume"`)
	var errs []string
	for _, cfg := range []string{`{"enabled":"x"}`, `{"deviceName":"` + testKey + `!"}`} {
		if err := h.apply(cfg, map[string]string{"apiKey": testKey}); err != nil {
			errs = append(errs, err.Error())
		}
	}
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey + " bad"})
	h.waitState(`{"state":"key_missing","code":"invalid","build":"20261027","expiresAt":"2027-01-25T00:02:07Z"}`)
	h.s.SoloistGone()
	h.waitState(`{"state":"key_missing","code":"invalid"}`)
	bare := strings.TrimSuffix(testKey, "-0123456789abcdef")
	if out := h.logs.String(); strings.Contains(out, bare) {
		t.Fatalf("log carries the key: %s", out)
	}
	for _, s := range append(h.allStates(), errs...) {
		if strings.Contains(s, bare) {
			t.Fatalf("state or error carries the key: %s", s)
		}
	}
	keyed := 0
	for _, e := range h.a.all() {
		if strings.Contains(e.String(), bare) {
			if e.GetSoloistConfig() == nil {
				t.Fatalf("%v carries the key", e.ProtoReflect().Descriptor().FullName())
			}
			keyed++
		}
	}
	if keyed == 0 {
		t.Fatal("the key never reached audiod")
	}
}
