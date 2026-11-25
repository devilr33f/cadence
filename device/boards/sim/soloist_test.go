package sim_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

// release is a tarball with a build for the sim's own cpu, which the fake
// audiod's probe then refuses to name
func release(t *testing.T) []byte {
	t.Helper()
	machine := map[string]uint16{"amd64": 62, "arm64": 183}[runtime.GOARCH]
	build := make([]byte, 4096)
	copy(build, "\x7fELF")
	build[4], build[5], build[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(build[18:], machine)
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	if err := tw.WriteHeader(&tar.Header{Name: "soloist", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(build))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(build)
	_ = tw.Close()
	var gb bytes.Buffer
	zw := gzip.NewWriter(&gb)
	_, _ = zw.Write(tb.Bytes())
	_ = zw.Close()
	return gb.Bytes()
}

// the e2e names its fake cdn, the ca that alone verifies it and the builds'
// cpu in the environment; without a url there is no cdn, and a machine that
// is no 16-bit number is the sim's own cpu's
func TestSoloistCDNFromEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if c, ok := sim.SoloistCDNFromEnv(getenv); ok {
		t.Fatalf("a cdn without a url: %+v", c)
	}
	env["CADENCE_SIM_SOLOIST_CDN_CA"] = "/run/cadence-cdn/ca.crt"
	env["CADENCE_SIM_SOLOIST_MACHINE"] = "62"
	if c, ok := sim.SoloistCDNFromEnv(getenv); ok {
		t.Fatalf("a cdn without a url: %+v", c)
	}
	env["CADENCE_SIM_SOLOIST_CDN"] = "https://soloist-cdn/soloist_release_amd64.tar.gz"
	c, ok := sim.SoloistCDNFromEnv(getenv)
	want := sim.SoloistCDN{URL: "https://soloist-cdn/soloist_release_amd64.tar.gz", CAFile: "/run/cadence-cdn/ca.crt", Machine: 62}
	if !ok || c != want {
		t.Fatalf("got %+v %v, want %+v", c, ok, want)
	}
	for _, machine := range []string{"", "x86", "-62", "65536"} {
		env["CADENCE_SIM_SOLOIST_MACHINE"] = machine
		if c, ok := sim.SoloistCDNFromEnv(getenv); !ok || c.Machine != 0 {
			t.Fatalf("machine %q: got %+v %v", machine, c, ok)
		}
	}
}

// the sim's updater gets its builds from the cdn its board options name, for
// its own cpu, and has audiod probe them in soloist's jail
func TestAudiodModeUpdatesSoloistFromItsCDN(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("audiod runs on linux only")
	}
	body := release(t)
	var requests atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := audiodtest.WriteFlags(filepath.Join(dir, "run"), "--soloist"); err != nil {
		t.Fatal(err)
	}
	logs := &logBuf{}
	b := sim.New(func(string) string { return "1" }, dir, slog.New(slog.NewJSONHandler(logs, nil)),
		sim.WithAudiod(exe), sim.WithSoloistCDN(sim.SoloistCDN{URL: srv.URL + "/soloist_release_amd64.tar.gz", CAFile: ca}))
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	var soloist capability.SecretApplier
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "media.soloist" {
			soloist, _ = p.(capability.SecretApplier)
		}
	}
	if soloist == nil {
		t.Fatal("no media.soloist taking secrets")
	}
	if err := soloist.ApplySecrets(context.Background(), 1, json.RawMessage(`{"enabled":true}`), map[string]string{"apiKey": "TESTKEY-sim"}); err != nil {
		t.Fatal(err)
	}
	audiodIO(t, b, logs)
	waitFor(t, "the update", func() bool {
		return strings.Contains(logs.String(), `"msg":"soloist_update","result":"failed","reason":"version"`)
	})
	waitRecord(t, dir, "soloist_probe", "soloist.next")
	if n := requests.Load(); n != 1 {
		t.Fatalf("%d downloads", n)
	}
	if strings.Contains(logs.String(), "TESTKEY") || strings.Contains(logs.String(), srv.URL) {
		t.Fatalf("log carries the key or the url: %s", logs)
	}
}
