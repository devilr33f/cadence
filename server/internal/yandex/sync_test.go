package yandex

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectADBPreservesSyncAndUsesBuildrootNetwork(t *testing.T) {
	body := []byte(`{"status":"ok","config":{"account_config":{"spotter":"alisa"},"system_config":{"other":1},"device_config":{"adbd":{"network":false,"keep":"yes"}}},"big":9007199254740993}`)
	got, err := injectADB(body, "yandexmidi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"big":9007199254740993`) || !strings.Contains(string(got), `"keep":"yes"`) {
		t.Fatalf("sync changed unrelated data: %s", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	config := doc["config"].(map[string]any)
	for _, adb := range []any{
		config["system_config"].(map[string]any)["adbd"], config["device_config"].(map[string]any)["adbd"],
		config["adbd"], doc["adbd"],
	} {
		fields := adb.(map[string]any)
		if fields["remoteActivation"] != true || fields["network"] != true || fields["forceDisable"] != false {
			t.Fatalf("adb = %#v", fields)
		}
	}
	if config["system_config"].(map[string]any)["other"] != float64(1) {
		t.Fatal("system config replaced")
	}
}

func TestInjectADBForStationMax(t *testing.T) {
	got, err := injectADB([]byte(`{"config":{}}`), "yandexstation_2")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(got, &doc)
	config := doc["config"].(map[string]any)
	adb := config["system_config"].(map[string]any)["adbd"].(map[string]any)
	if adb["network"] != false {
		t.Fatalf("network = %#v", adb["network"])
	}
	tv := config["system_config"].(map[string]any)["com.yandex.tv.services.platform"].(map[string]any)
	if tv["adbEnabled"] != true || tv["developmentSettingsEnabled"] != true {
		t.Fatalf("tv = %#v", tv)
	}
}

func TestInjectADBRefusesInvalidJSON(t *testing.T) {
	for _, body := range []string{"", "null", "{}", "{} {}"} {
		if body == "{}" {
			continue
		}
		if _, err := injectADB([]byte(body), "yandexmidi"); err == nil {
			t.Fatalf("%q accepted", body)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(fallbackSync("yandex-pickle"), &doc); err != nil {
		t.Fatal(err)
	}
}

func TestRouteMatches(t *testing.T) {
	if !updatePath("/check_updates") || !updatePath("/quasar/allow_update") || updatePath("/get_sync_info") {
		t.Fatal("update route mismatch")
	}
	if !firmwarePath("/cucumber/ota/user/update.zip") || firmwarePath("/get_sync_info") {
		t.Fatal("firmware route mismatch")
	}
	if !allowedHost("quasar.yandex.net") || allowedHost("example.com") || allowedHost("quasar.yandex.net.evil.example") {
		t.Fatal("host allowlist mismatch")
	}
	if !droppedTelemetry(clckHost, "/quasar_metrics/write/batch") || !droppedTelemetry(clckHost, "/register") ||
		!droppedTelemetry("startup.mobile.yandex.net", "/startup") || droppedTelemetry("uniproxy.alice.yandex.net", "/uni.ws") {
		t.Fatal("telemetry route mismatch")
	}
}
