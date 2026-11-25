package main

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// the screen sim claims, shows its fake screen the code, and writes the config once the poll answers approved; the
// token is never printed
func TestClaimWritesConfigAndNeverPrintsToken(t *testing.T) {
	t.Run("subcommand", func(t *testing.T) { claimRun(t, false) })
	t.Run("in-process", func(t *testing.T) { claimRun(t, true) })
}

type syncWriter struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func claimRun(t *testing.T, inProcess bool) {
	const token = "cdd_claimedtoken"
	var polls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/claim":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"cdc_x","code":"K7M4QX","url":"https://c/claims/K7M4QX","expiresAt":"` +
				time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339) + `","pollAfterMs":1000}`))
		case r.URL.Path == "/v2/claim/cdc_x":
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"state":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"approved","deviceId":"dev-1","token":"` + token + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	dir, err := os.MkdirTemp("", "clm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	var out bytes.Buffer
	cmd := exec.Command(buildBinary(t, t.TempDir()), "claim", "--server", srv.URL, "--board", "sim-tv",
		"--config", cfg, "--data-dir", filepath.Join(dir, "d"), "--ca-file", ca)
	if inProcess {
		// the daemon's own start: it claims while the config is missing, then runs on it
		cmd = exec.Command(buildBinary(t, t.TempDir()), "--wait-config", "--config", cfg, "--claim-server", srv.URL,
			"--claim-board", "sim-tv", "--claim-data-dir", filepath.Join(dir, "d"), "--claim-ca-file", ca)
	}
	cmd.Env = append(cleanEnv(), "CADENCE_SIM_TV_FAKE_SCREEN=1", "CADENCE_SIM_TV=1")
	w := &syncWriter{b: &out}
	cmd.Stdout, cmd.Stderr = w, w
	if inProcess {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if raw, err := os.ReadFile(cfg); err == nil && strings.Contains(string(raw), token) {
				break
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				t.Fatal("no config")
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	} else if c := exitCode(t, cmd.Run()); c != 0 {
		t.Fatalf("exit = %d: %s", c, out.String())
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "K7M4QX") || strings.Contains(out.String(), "cdc_x") {
		t.Fatal("a credential printed")
	}
	if !strings.Contains(out.String(), "screen_connected") || !strings.Contains(out.String(), "claim_approved") {
		t.Fatalf("log %s", out.String())
	}
	raw, err := os.ReadFile(cfg)
	if err != nil || !strings.Contains(string(raw), token) || !strings.Contains(string(raw), `"sim-tv"`) {
		t.Fatalf("config: %v %s", err, raw)
	}
}

func TestClaimUsage(t *testing.T) {
	bin := buildBinary(t, t.TempDir())
	for _, args := range [][]string{
		{"claim", "--server", "https://x"},
		{"claim", "--server", "https://x", "--board", "sim", "--config", "/c", "--data-dir", "/d"},
	} {
		cmd := exec.Command(bin, args...)
		cmd.Env = cleanEnv()
		if c := exitCode(t, cmd.Run()); c != 2 {
			t.Fatalf("%v: exit = %d", args, c)
		}
	}
}
