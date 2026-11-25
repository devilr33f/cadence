package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/discovery"
)

func fakeBrowse(t *testing.T, devices []discovery.Device) *time.Duration {
	t.Helper()
	var got time.Duration
	old := browse
	browse = func(_ context.Context, wait time.Duration) ([]discovery.Device, error) {
		got = wait
		return devices, nil
	}
	t.Cleanup(func() { browse = old })
	return &got
}

var found = []discovery.Device{{
	Instance: "Cadence yandex-midi 0a1b2c3d", DeviceID: "0a1b2c3d-0000-4000-8000-000000000000", Board: "yandex-midi",
	Version: "v2.0.0", Host: "cadence-0a1b2c3d.local", Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.182")},
}}

func TestDiscoverListsDevices(t *testing.T) {
	wait := fakeBrowse(t, found)
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"discover", "--wait", "1s"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if *wait != time.Second {
		t.Fatalf("wait %v", *wait)
	}
	for _, want := range []string{"BOARD", "yandex-midi", "0a1b2c3d-0000-4000-8000-000000000000", "cadence-0a1b2c3d.local", "192.168.1.182"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in %s", want, out.String())
		}
	}
}

func TestDiscoverJSON(t *testing.T) {
	fakeBrowse(t, found)
	var out bytes.Buffer
	if code := run(context.Background(), []string{"discover", "--json"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code %d", code)
	}
	var list []map[string]any
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list) != 1 || list[0]["deviceId"] != found[0].DeviceID {
		t.Fatalf("json %s (%v)", out.String(), err)
	}
}

func TestDiscoverNoneAndBadFlags(t *testing.T) {
	fakeBrowse(t, nil)
	var out bytes.Buffer
	if code := run(context.Background(), []string{"discover"}, &out, &bytes.Buffer{}); code != 0 || !strings.Contains(out.String(), "no cadence device") {
		t.Fatalf("code %d out %q", code, out.String())
	}
	for _, args := range [][]string{{"discover", "--wait", "0s"}, {"discover", "--wait", "2m"}, {"discover", "extra"}, {"discover", "--nope"}} {
		if code := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); code != exitUsage {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}
