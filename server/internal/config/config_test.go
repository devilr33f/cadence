package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(env(map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" {
		t.Fatalf("listen = %q", c.Listen)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := config.Load(env(nil))
	var ce config.Error
	if !errors.As(err, &ce) || ce != (config.Error{Variable: "DATABASE_URL", Reason: "required"}) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsBadURLWithoutValue(t *testing.T) {
	_, err := config.Load(env(map[string]string{"DATABASE_URL": "mysql://u:s3cret-value@h/db"}))
	var ce config.Error
	if !errors.As(err, &ce) || ce.Reason != "must be a postgres url" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret-value") {
		t.Fatal("error leaks the value")
	}
}

func TestMasterKeyFileRequired(t *testing.T) {
	_, err := config.Load(env(map[string]string{"DATABASE_URL": "postgres://u:p@h/db"}))
	var ce config.Error
	if !errors.As(err, &ce) || ce != (config.Error{Variable: "CADENCE_MASTER_KEY_FILE", Reason: "required"}) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadReadsMasterKeyFileAndDevFakes(t *testing.T) {
	m := map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k", "CADENCE_DEV_FAKES": "1"}
	c, err := config.Load(env(m))
	if err != nil || c.MasterKeyFile != "/k" || !c.DevFakes {
		t.Fatalf("%+v %v", c, err)
	}
	m["CADENCE_DEV_FAKES"] = "true"
	if c, _ = config.Load(env(m)); c.DevFakes {
		t.Fatal("only 1 enables fakes")
	}
}

// the dev knob that shortens compactAfter's unit holds only beside the fakes
func TestDevCompactSeconds(t *testing.T) {
	m := map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k", "CADENCE_DEV_COMPACT_SECONDS": "1"}
	if c, err := config.Load(env(m)); err != nil || c.DevCompactUnit != 0 {
		t.Fatalf("without fakes: %+v %v", c, err)
	}
	m["CADENCE_DEV_FAKES"] = "1"
	if c, err := config.Load(env(m)); err != nil || c.DevCompactUnit != time.Second {
		t.Fatalf("with fakes: %+v %v", c, err)
	}
	m["CADENCE_DEV_COMPACT_SECONDS"] = "60"
	if c, err := config.Load(env(m)); err != nil || c.DevCompactUnit != time.Minute {
		t.Fatalf("60: %+v %v", c, err)
	}
	for _, bad := range []string{"0", "61", "1.5", "x"} {
		m["CADENCE_DEV_COMPACT_SECONDS"] = bad
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce != (config.Error{Variable: "CADENCE_DEV_COMPACT_SECONDS", Reason: "must be 1 to 60"}) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// the dev knob that shortens the awake window after a session (M5b-R8) holds
// only beside the fakes; the window stays 2 min otherwise
func TestDevAwakeSeconds(t *testing.T) {
	m := map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k", "CADENCE_DEV_AWAKE_SECONDS": "1"}
	if c, err := config.Load(env(m)); err != nil || c.DevAwake != 0 {
		t.Fatalf("without fakes: %+v %v", c, err)
	}
	m["CADENCE_DEV_FAKES"] = "1"
	if c, err := config.Load(env(m)); err != nil || c.DevAwake != time.Second {
		t.Fatalf("with fakes: %+v %v", c, err)
	}
	m["CADENCE_DEV_AWAKE_SECONDS"] = "120"
	if c, err := config.Load(env(m)); err != nil || c.DevAwake != 2*time.Minute {
		t.Fatalf("120: %+v %v", c, err)
	}
	for _, bad := range []string{"0", "121", "1.5", "x"} {
		m["CADENCE_DEV_AWAKE_SECONDS"] = bad
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce != (config.Error{Variable: "CADENCE_DEV_AWAKE_SECONDS", Reason: "must be 1 to 120"}) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func base() map[string]string {
	return map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "CADENCE_MASTER_KEY_FILE": "/k"}
}

// the origin a cookie request's Origin must match: scheme and host as a
// browser sends them, nothing else
func TestPublicOrigin(t *testing.T) {
	m := base()
	if c, err := config.Load(env(m)); err != nil || c.PublicOrigin != "" {
		t.Fatalf("unset: %+v %v", c, err)
	}
	for _, ok := range []string{"https://cadence.example.com", "http://localhost:8080", "https://[::1]:8443"} {
		m["CADENCE_PUBLIC_ORIGIN"] = ok
		if c, err := config.Load(env(m)); err != nil || c.PublicOrigin != ok {
			t.Fatalf("%q: %+v %v", ok, c, err)
		}
	}
	for _, bad := range []string{
		"cadence.example.com", "https://", "https://x.example/", "https://x.example/path", "https://x.example?q=1",
		"https://x.example#f", "https://u@x.example", "ftp://x.example", "https://X.example", "HTTPS://x.example",
		"https://x.example:443", "http://x.example:80", " https://x.example", "null",
	} {
		m["CADENCE_PUBLIC_ORIGIN"] = bad
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce.Variable != "CADENCE_PUBLIC_ORIGIN" || strings.Contains(err.Error(), "x.example") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// Secure is left off the session cookie only for 1 (plain-http dev stacks)
func TestCookieInsecure(t *testing.T) {
	m := base()
	for v, want := range map[string]bool{"": false, "0": false, "true": false, "1": true} {
		m["CADENCE_COOKIE_INSECURE"] = v
		if c, err := config.Load(env(m)); err != nil || c.CookieInsecure != want {
			t.Fatalf("%q: %+v %v", v, c, err)
		}
	}
}

func TestYandexProxyDisabledByDefault(t *testing.T) {
	c, err := config.Load(env(base()))
	if err != nil || c.YandexProxy.Listen != "" || c.YandexProxy.Clients != nil {
		t.Fatalf("%+v %v", c.YandexProxy, err)
	}
}

func TestYandexProxyConfiguration(t *testing.T) {
	m := base()
	m["CADENCE_YANDEX_PROXY_LISTEN"] = ":7443"
	for _, variable := range []string{"CADENCE_YANDEX_PROXY_CA_CERT", "CADENCE_YANDEX_PROXY_CA_KEY", "CADENCE_YANDEX_PROXY_CLIENTS"} {
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce.Variable != variable {
			t.Fatalf("%s: %v", variable, err)
		}
		m[variable] = map[string]string{
			"CADENCE_YANDEX_PROXY_CA_CERT": "/ca.pem", "CADENCE_YANDEX_PROXY_CA_KEY": "/ca.key",
			"CADENCE_YANDEX_PROXY_CLIENTS": "192.168.1.181,10.0.0.0/8",
		}[variable]
	}
	m["CADENCE_YANDEX_PROXY_UPSTREAM_DNS"] = "1.1.1.1:53"
	m["CADENCE_YANDEX_PROXY_DEVICES"] = "vendor-1=123e4567-e89b-12d3-a456-426614174000"
	c, err := config.Load(env(m))
	if err != nil || len(c.YandexProxy.Clients) != 2 || c.YandexProxy.Devices["vendor-1"] != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("%+v %v", c.YandexProxy, err)
	}
	if got := c.YandexProxy.Clients[0].String(); got != "192.168.1.181/32" {
		t.Fatalf("client = %q", got)
	}
}

func TestYandexProxyRejectsBadConfigurationWithoutValues(t *testing.T) {
	m := base()
	m["CADENCE_YANDEX_PROXY_LISTEN"] = ":7443"
	m["CADENCE_YANDEX_PROXY_CA_CERT"] = "/ca.pem"
	m["CADENCE_YANDEX_PROXY_CA_KEY"] = "/ca.key"
	m["CADENCE_YANDEX_PROXY_CLIENTS"] = "192.168.1.181"
	for _, tc := range []struct{ variable, value string }{
		{"CADENCE_YANDEX_PROXY_CLIENTS", "192.168.1.999"},
		{"CADENCE_YANDEX_PROXY_UPSTREAM_DNS", "resolver-secret"},
		{"CADENCE_YANDEX_PROXY_DEVICES", "vendor-secret=not-a-uuid"},
		{"CADENCE_YANDEX_PROXY_DEVICES", "bad/vendor=123e4567-e89b-12d3-a456-426614174000"},
	} {
		previous, existed := m[tc.variable]
		m[tc.variable] = tc.value
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce.Variable != tc.variable || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s %q: %v", tc.variable, tc.value, err)
		}
		if existed {
			m[tc.variable] = previous
		} else {
			delete(m, tc.variable)
		}
	}
}

// the proxies whose X-Forwarded-For names the client
func TestTrustedProxy(t *testing.T) {
	m := base()
	if c, err := config.Load(env(m)); err != nil || c.TrustedProxies != nil {
		t.Fatalf("unset: %+v %v", c, err)
	}
	m["CADENCE_TRUSTED_PROXY"] = "172.18.0.0/16, 10.0.0.5,fd00::/8"
	c, err := config.Load(env(m))
	if err != nil || strings.Join(c.TrustedProxies, " ") != "172.18.0.0/16 10.0.0.5 fd00::/8" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"traefik", "10.0.0.5,", "10.0.0.0/33", ",", "10.0.0.5 10.0.0.6"} {
		m["CADENCE_TRUSTED_PROXY"] = bad
		_, err := config.Load(env(m))
		var ce config.Error
		if !errors.As(err, &ce) || ce.Variable != "CADENCE_TRUSTED_PROXY" || strings.Contains(err.Error(), "10.0") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}
