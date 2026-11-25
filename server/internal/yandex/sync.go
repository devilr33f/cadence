package yandex

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

const (
	quasarNet = "quasar.yandex.net"
	quasarRU  = "quasar.yandex.ru"
	scbhHost  = "scbh.yandex.net"
	clckHost  = "clck.yandex.net"
)

var allowedHosts = map[string]bool{
	quasarNet: true, quasarRU: true, scbhHost: true,
	clckHost: true, "clck.yasmina.yango.com": true,
	"report.appmetrica.yandex.net": true, "startup.mobile.yandex.net": true,
	"startup.appmetrica.yango.com": true,
	"log.strm.yandex.ru":           true,
	"rpc.alice.yandex.net":         true,
	"uniproxy.alice.yandex.net":    true,
	"alice.yandex.net":             true,
	"alice-ats.yandex.net":         true,
	"alice-ats-test.yandex.net":    true,
	"oauth.yandex.ru":              true,
}

func allowedHost(host string) bool { return allowedHosts[canonicalHost(host)] }

func quasarHost(host string) bool {
	host = canonicalHost(host)
	return host == quasarNet || host == quasarRU || host == scbhHost
}

func syncPath(path string) bool { return path == "/get_sync_info" }

func updatePath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "check_updates") || strings.Contains(path, "allow_update")
}

func firmwarePath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "/ota/") || strings.HasSuffix(path, ".zip")
}

func telemetryHost(host string) bool {
	host = canonicalHost(host)
	return host == clckHost || host == "clck.yasmina.yango.com"
}

// injectADB preserves the upstream sync document and changes only the switches
// the firmware's AdbSwitcher and the android platform service read.
func injectADB(body []byte, platform string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, io.ErrUnexpectedEOF
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}
	android := strings.EqualFold(platform, "yandexstation") || strings.EqualFold(platform, "yandexstation_2")
	adb := map[string]any{"remoteActivation": true, "network": !android, "forceDisable": false}
	config := object(document, "config")
	for _, parent := range []map[string]any{object(config, "system_config"), object(config, "device_config"), config, document} {
		mergeMap(parent, "adbd", adb)
	}
	if android {
		system := object(config, "system_config")
		tv := object(system, "com.yandex.tv.services.platform")
		tv["adbEnabled"] = true
		tv["developmentSettingsEnabled"] = true
	}
	document["status"] = "ok"
	return json.Marshal(document)
}

func fallbackSync(platform string) []byte {
	body, _ := injectADB([]byte(`{"status":"ok","config":{"account_config":{"spotter":"alisa"},"device_config":{"name":"quasar"},"system_config":{}},"subscription":{"mode":"transaction"}}`), platform)
	return body
}

func object(parent map[string]any, key string) map[string]any {
	if child, ok := parent[key].(map[string]any); ok {
		return child
	}
	child := map[string]any{}
	parent[key] = child
	return child
}

func mergeMap(parent map[string]any, key string, values map[string]any) {
	child, _ := parent[key].(map[string]any)
	if child == nil {
		child = map[string]any{}
		parent[key] = child
	}
	for name, value := range values {
		child[name] = value
	}
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}
