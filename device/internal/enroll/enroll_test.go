package enroll_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/enroll"
)

// the data dir is the test's own: enroll clears a store inside it
func opts(srv *httptest.Server, dir string) enroll.Options {
	return enroll.Options{
		Server: srv.URL, Code: "ABCD-EFGH", Board: "sim",
		ConfigPath: filepath.Join(dir, "config.json"), DataDir: filepath.Join(dir, "data"), Client: srv.Client(),
	}
}

func TestEnrollWritesConfig(t *testing.T) {
	var got struct {
		Code        string `json:"code"`
		Fingerprint struct {
			Board string `json:"board"`
		} `json:"fingerprint"`
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/enroll" {
			http.Error(w, "bad", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-1", "token": "cdd_secret"})
	}))
	defer srv.Close()
	o := opts(srv, t.TempDir())
	if err := enroll.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got.Code != "ABCD-EFGH" || got.Fingerprint.Board != "sim" {
		t.Fatalf("request = %+v", got)
	}
	cfg, _, err := config.Load(o.ConfigPath)
	if err != nil || cfg.DeviceID != "dev-1" || cfg.DeviceToken != "cdd_secret" || cfg.ServerURL != srv.URL || cfg.DataDir != o.DataDir {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(o.ConfigPath); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", st.Mode())
		}
	}
}

func TestEnrollKeepsAudiod(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-1", "token": "cdd_secret"})
	}))
	defer srv.Close()
	o := opts(srv, t.TempDir())
	o.Audiod, o.AudiodBinary = true, "/opt/cadence-audiod"
	if err := enroll.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(o.ConfigPath)
	if err != nil || !cfg.Audiod || cfg.AudiodBinary != "/opt/cadence-audiod" {
		t.Fatalf("audiod = %v %q, err = %v", cfg.Audiod, cfg.AudiodBinary, err)
	}
}

func TestEnrollRejectsRelativeAudiodBinary(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server called")
	}))
	defer srv.Close()
	o := opts(srv, t.TempDir())
	o.Audiod, o.AudiodBinary = true, "audiod"
	if err := enroll.Run(context.Background(), o); !errors.Is(err, enroll.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"enroll_refused"}`))
	}))
	defer srv.Close()
	o := opts(srv, t.TempDir())
	if err := enroll.Run(context.Background(), o); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(o.ConfigPath); err == nil {
		t.Fatal("config written on refusal")
	}
}

func TestEnrollRefusesRedirect(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "d", "token": "cdd_t"})
	}))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v2/enroll", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	o := opts(srv, t.TempDir())
	if err := enroll.Run(context.Background(), o); err == nil {
		t.Fatal("redirect followed")
	}
	if _, err := os.Stat(o.ConfigPath); err == nil {
		t.Fatal("config written after redirect")
	}
}

func TestEnrollRejectsPlainHTTPOffSim(t *testing.T) {
	o := enroll.Options{Server: "http://example.test", Code: "c", Board: "yandex-pickle", ConfigPath: filepath.Join(t.TempDir(), "c.json"), DataDir: "/d"}
	if err := enroll.Run(context.Background(), o); !errors.Is(err, enroll.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// the stored configs belong to the old enrollment, whose generations the
// server no longer counts; a refused code leaves them alone
func TestEnrollClearsConfigStore(t *testing.T) {
	dir := t.TempDir()
	stored := filepath.Join(dir, "data", "config", "audio.output.json")
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte(`{"generation":7,"config":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer refused.Close()
	if err := enroll.Run(context.Background(), opts(refused, dir)); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("refused enroll touched the store: %v", err)
	}
	ok := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-2", "token": "cdd_t"})
	}))
	defer ok.Close()
	if err := enroll.Run(context.Background(), opts(ok, dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(stored)); !os.IsNotExist(err) {
		t.Fatalf("store kept: %v", err)
	}
	if cfg, _, err := config.Load(filepath.Join(dir, "config.json")); err != nil || cfg.DeviceID != "dev-2" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

// hard rule: the production client verifies the server against the system
// roots, so a certificate nothing vouches for is refused
func TestEnrollRejectsUntrustedTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "d", "token": "cdd_t"})
	}))
	// the refused handshake is the point; the server need not log it
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	o := opts(srv, t.TempDir())
	o.Client = nil
	if err := enroll.Run(context.Background(), o); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if _, err := os.Stat(o.ConfigPath); err == nil {
		t.Fatal("config written for an untrusted server")
	}
}

func TestEnrollTrustsCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-1", "token": "cdd_t"})
	}))
	defer srv.Close()
	dir := t.TempDir()
	o := opts(srv, dir)
	o.Client = nil
	o.CAFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(o.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enroll.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := config.Load(o.ConfigPath); err != nil || cfg.DeviceID != "dev-1" || cfg.CAFile != o.CAFile {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-3", "token": "cdd_t"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedStore(t *testing.T, dir string) string {
	t.Helper()
	stored := filepath.Join(dir, "data", "config", "audio.output.json")
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte(`{"generation":7,"config":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return stored
}

func enrollKeepsOld(t *testing.T, prepare func(cfgDir string)) {
	t.Helper()
	dir := t.TempDir()
	stored := seedStore(t, dir)
	storedBefore, _ := os.ReadFile(stored)
	cfgDir := filepath.Join(dir, "etc")
	if err := os.Mkdir(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	o := opts(okServer(t), dir)
	o.ConfigPath = filepath.Join(cfgDir, "config.json")
	if err := config.Save(o.ConfigPath, config.Config{Board: "sim", ServerURL: "https://old.example", DeviceID: "old", DeviceToken: "cdd_old", DataDir: o.DataDir}); err != nil {
		t.Fatal(err)
	}
	oldBytes, _ := os.ReadFile(o.ConfigPath)
	prepare(cfgDir)
	if err := enroll.Run(context.Background(), o); err == nil {
		t.Fatal("staging failure was not reported")
	}
	if got, err := os.ReadFile(o.ConfigPath); err != nil || !bytes.Equal(got, oldBytes) {
		t.Fatalf("old config changed: err = %v", err)
	}
	if got, err := os.ReadFile(stored); err != nil || !bytes.Equal(got, storedBefore) {
		t.Fatalf("store changed: err = %v", err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(cfgDir, ".tmp-*")); len(tmp) != 0 {
		t.Fatalf("temp left: %v", tmp)
	}
}

func TestEnrollKeepsStoreWhenStagingFails(t *testing.T) {
	restore := enroll.SetStageConfig(func(string, config.Config) (func() error, func(), error) {
		return nil, nil, errors.New("stage failed")
	})
	t.Cleanup(restore)
	enrollKeepsOld(t, func(string) {})
}

func TestEnrollKeepsStoreUntilConfigSaved(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only dir that is enforced")
	}
	enrollKeepsOld(t, func(cfgDir string) {
		if err := os.Chmod(cfgDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })
	})
}

func TestEnrollWipesStoreAfterStaging(t *testing.T) {
	dir := t.TempDir()
	stored := seedStore(t, dir)
	o := opts(okServer(t), dir)
	if err := enroll.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("store kept: %v", err)
	}
	if cfg, _, err := config.Load(o.ConfigPath); err != nil || cfg.DeviceID != "dev-3" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(entries) != 0 {
		t.Fatalf("temp left: %v", entries)
	}
}
