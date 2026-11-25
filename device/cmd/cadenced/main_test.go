package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return ee.ExitCode()
}

func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CADENCE_SIM=") {
			env = append(env, e)
		}
	}
	return env
}

func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cadenced")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"board":"sim","dataDir":"/d"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("missing config flag", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = cleanEnv()
		if c := exitCode(t, cmd.Run()); c != 2 {
			t.Fatalf("exit = %d", c)
		}
	})

	t.Run("fingerprint refused", func(t *testing.T) {
		buf := &syncBuf{}
		cmd := exec.Command(bin, "--config", cfg)
		cmd.Env = cleanEnv()
		cmd.Stderr = buf
		if c := exitCode(t, cmd.Run()); c != 3 {
			t.Fatalf("exit = %d", c)
		}
		if out := buf.String(); !strings.Contains(out, `"msg":"board_refused"`) || !strings.Contains(out, `"reason":"fingerprint"`) {
			t.Fatalf("stderr = %q", out)
		}
	})

	t.Run("unknown board refused", func(t *testing.T) {
		unknown := filepath.Join(dir, "unknown.json")
		if err := os.WriteFile(unknown, []byte(`{"board":"no-such-board","dataDir":"/d"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		buf := &syncBuf{}
		cmd := exec.Command(bin, "--config", unknown)
		cmd.Env = cleanEnv()
		cmd.Stderr = buf
		if c := exitCode(t, cmd.Run()); c != 3 {
			t.Fatalf("exit = %d", c)
		}
		if out := buf.String(); !strings.Contains(out, `"msg":"board_refused"`) || !strings.Contains(out, `"reason":"unknown"`) {
			t.Fatalf("stderr = %q", out)
		}
	})

	t.Run("sigterm", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no SIGTERM on windows")
		}
		buf := &syncBuf{}
		cmd := exec.Command(bin, "--config", cfg)
		cmd.Env = append(cleanEnv(), "CADENCE_SIM=1")
		cmd.Stderr = buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(buf.String(), `"msg":"board_ready"`) {
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				t.Fatal("no board_ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if c := exitCode(t, cmd.Wait()); c != 0 {
			t.Fatalf("exit = %d", c)
		}
	})
}

func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "cadenced-enroll-test")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestEnrollNeverPrintsToken(t *testing.T) {
	const token = "cdd_supersecrettoken"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"deviceId":"dev-1","token":"` + token + `"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(buildBinary(t, dir), "enroll", "--server", srv.URL, "--code", "ABCD-EFGH", "--board", "sim",
		"--config", cfg, "--data-dir", filepath.Join(dir, "data"), "--ca-file", ca)
	cmd.Env = cleanEnv()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c := exitCode(t, cmd.Run()); c != 0 {
		t.Fatalf("exit = %d, stderr = %q", c, stderr.String())
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatal("token printed")
	}
	raw, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(raw), token) {
		t.Fatalf("config missing token: %v", err)
	}
}

func TestEnrollAudiodFlags(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"deviceId":"dev-1","token":"cdd_x"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	cmd := exec.Command(buildBinary(t, dir), "enroll", "--server", srv.URL, "--code", "ABCD-EFGH", "--board", "sim",
		"--config", cfg, "--data-dir", filepath.Join(dir, "data"), "--ca-file", ca, "--audiod", "--audiod-binary", "/opt/a")
	cmd.Env = cleanEnv()
	if c := exitCode(t, cmd.Run()); c != 0 {
		t.Fatalf("exit = %d", c)
	}
	var got struct {
		Audiod       bool   `json:"audiod"`
		AudiodBinary string `json:"audiodBinary"`
	}
	raw, err := os.ReadFile(cfg)
	if err != nil || json.Unmarshal(raw, &got) != nil || !got.Audiod || got.AudiodBinary != "/opt/a" {
		t.Fatalf("config lacks audiod: %v", err)
	}
}

func TestEnrollExitCodes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := buildBinary(t, dir)
	cfg := filepath.Join(dir, "config.json")

	refused := exec.Command(bin, "enroll", "--server", srv.URL, "--code", "x", "--board", "sim", "--config", cfg, "--data-dir", "/d", "--ca-file", ca)
	refused.Env = cleanEnv()
	if c := exitCode(t, refused.Run()); c != 3 {
		t.Fatalf("refused exit = %d", c)
	}
	usage := exec.Command(bin, "enroll", "--server", srv.URL)
	usage.Env = cleanEnv()
	if c := exitCode(t, usage.Run()); c != 2 {
		t.Fatalf("usage exit = %d", c)
	}
}

func TestWaitConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	buf := &syncBuf{}
	cmd := exec.Command(buildBinary(t, dir), "--wait-config", "--config", cfg)
	cmd.Env = append(cleanEnv(), "CADENCE_SIM=1")
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(exited) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()

	select {
	case <-exited:
		t.Fatalf("exited without a config: %v, stderr = %q", waitErr, buf.String())
	case <-time.After(1500 * time.Millisecond):
	}
	if strings.Contains(buf.String(), `"msg":"board_ready"`) {
		t.Fatal("started without a config")
	}

	// a rename, as enroll writes it: the waiter never sees a partial file
	tmp := filepath.Join(dir, "config.tmp")
	if err := os.WriteFile(tmp, []byte(`{"board":"sim","dataDir":"`+filepath.ToSlash(dir)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"board_ready"`) {
		select {
		case <-exited:
			t.Fatalf("exited: %v, stderr = %q", waitErr, buf.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no board_ready after the config appeared, stderr = %q", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUnknownConfigKeysLogged(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"board":"sim","dataDir":"/d","future":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	cmd := exec.Command(buildBinary(t, dir), "--config", cfg)
	cmd.Env = cleanEnv()
	cmd.Stderr = buf
	if c := exitCode(t, cmd.Run()); c != 3 {
		t.Fatalf("exit = %d", c)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"config_unknown_keys"`) || !strings.Contains(out, `"count":1`) {
		t.Fatalf("stderr = %q", out)
	}
}
