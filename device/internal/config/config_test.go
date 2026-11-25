package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSim(t *testing.T) {
	cfg, n, err := config.Load(write(t, `{"board":"sim","dataDir":"/d"}`))
	if err != nil || cfg.Board != "sim" || cfg.DataDir != "/d" || n != 0 {
		t.Fatalf("cfg = %+v, n = %d, err = %v", cfg, n, err)
	}
}

func TestUnknownKeysCounted(t *testing.T) {
	cfg, n, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","future":1}`))
	if err != nil || n != 1 || cfg.Board != "sim" {
		t.Fatalf("cfg = %+v, n = %d, err = %v", cfg, n, err)
	}
}

func TestLoadRejectsBadBoard(t *testing.T) {
	_, _, err := config.Load(write(t, `{"board":"Sim!","dataDir":"/d"}`))
	if err == nil || !strings.Contains(err.Error(), "board") || strings.Contains(err.Error(), "Sim!") {
		t.Fatalf("err = %v", err)
	}
}

func TestDataDirRequired(t *testing.T) {
	_, _, err := config.Load(write(t, `{"board":"sim"}`))
	if err == nil || !strings.Contains(err.Error(), "dataDir") {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongTypeNamesFieldNotValue(t *testing.T) {
	_, _, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","deviceToken":12345}`))
	if err == nil || !strings.Contains(err.Error(), "deviceToken") || strings.Contains(err.Error(), "12345") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPOnlyForSim(t *testing.T) {
	_, _, err := config.Load(write(t, `{"board":"yandex-pickle","dataDir":"/d","serverUrl":"http://x:8080","deviceId":"a","deviceToken":"tok"}`))
	if err == nil || !strings.Contains(err.Error(), "serverUrl") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","serverUrl":"http://x:8080","deviceId":"a","deviceToken":"tok"}`)); err != nil {
		t.Fatalf("sim http: %v", err)
	}
	if _, _, err := config.Load(write(t, `{"board":"yandex-pickle","dataDir":"/d","serverUrl":"https://x","deviceId":"a","deviceToken":"tok"}`)); err != nil {
		t.Fatalf("https: %v", err)
	}
}

func TestTokenRequiredWithServer(t *testing.T) {
	_, _, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","serverUrl":"https://x","deviceId":"a"}`))
	if err == nil || !strings.Contains(err.Error(), "deviceToken") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = config.Load(write(t, `{"board":"sim","dataDir":"/d","serverUrl":"https://x","deviceToken":"t"}`))
	if err == nil || !strings.Contains(err.Error(), "deviceId") {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	want := config.Config{Board: "sim", ServerURL: "https://x", DeviceID: "d1", DeviceToken: "cdd_t", DataDir: "/d", CAFile: "/ca"}
	if err := config.Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, n, err := config.Load(p)
	if err != nil || got != want || n != 0 {
		t.Fatalf("got = %+v, n = %d, err = %v", got, n, err)
	}
}

func TestSaveMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes on windows")
	}
	p := filepath.Join(t.TempDir(), "c.json")
	if err := config.Save(p, config.Config{Board: "sim", DataDir: "/d"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", st.Mode(), err)
	}
}

func TestSaveAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only dir that is enforced")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	if err := config.Save(p, config.Config{Board: "sim", DataDir: "/old"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := config.Save(p, config.Config{Board: "sim", DataDir: "/new"}); err == nil {
		t.Fatal("save into a read-only dir succeeded")
	}
	got, _, err := config.Load(p)
	if err != nil || got.DataDir != "/old" {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
}

func TestUnknownKeysCaseInsensitive(t *testing.T) {
	_, n, err := config.Load(write(t, `{"Board":"sim","DATADIR":"/d","ServerURL":"","future":1}`))
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}

func TestAudiodKeys(t *testing.T) {
	cfg, n, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","audiod":true,"AudiodBinary":"/opt/cadence-audiod"}`))
	if err != nil || n != 0 || !cfg.Audiod || cfg.AudiodExecutable() != "/opt/cadence-audiod" {
		t.Fatalf("cfg = %+v, n = %d, err = %v", cfg, n, err)
	}
	cfg, _, err = config.Load(write(t, `{"board":"sim","dataDir":"/d","audiod":true}`))
	if err != nil || cfg.AudiodExecutable() != "/usr/lib/cadence/cadence-audiod" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	cfg, _, err = config.Load(write(t, `{"board":"sim","dataDir":"/d"}`))
	if err != nil || cfg.Audiod {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestAudiodBinaryMustBeAbsolute(t *testing.T) {
	for _, bin := range []string{"bin/secret-audiod", "secret-audiod", `C:secret-audiod`} {
		_, _, err := config.Load(write(t, `{"board":"sim","dataDir":"/d","audiodBinary":"`+bin+`"}`))
		if err == nil || !strings.Contains(err.Error(), "audiodBinary") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: err = %v", bin, err)
		}
	}
}

func TestPlainHTTPOnlyOnTheSimulators(t *testing.T) {
	for _, b := range []string{"sim", "sim-tv"} {
		if err := config.ValidateServerURL(b, "http://server:8080"); err != nil {
			t.Errorf("%s: %v", b, err)
		}
	}
	for _, b := range []string{"yandex-pickle", "rpi", "android-tv"} {
		if err := config.ValidateServerURL(b, "http://server:8080"); err == nil {
			t.Errorf("%s takes http", b)
		}
	}
}
