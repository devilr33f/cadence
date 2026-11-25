package soloist_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const day = 24 * time.Hour

// build is a file that reads as an aarch64 build made at epoch
func build(epoch int64) []byte {
	head := make([]byte, 64)
	copy(head, "\x7fELF")
	head[4], head[5], head[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(head[18:], updater.MachineAArch64)
	return append(fmt.Appendf(head, "epoch=%d;", epoch), bytes.Repeat([]byte{1}, 2000)...)
}

// daysLeft is the epoch of a build with that many days left
func daysLeft(days int) int64 {
	return time.Now().Add(time.Duration(days)*day - updater.Lifetime).Unix()
}

func release(t *testing.T, b []byte) []byte {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	if err := tw.WriteHeader(&tar.Header{Name: "soloist", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(b)
	_ = tw.Close()
	var gb bytes.Buffer
	zw := gzip.NewWriter(&gb)
	_, _ = zw.Write(tb.Bytes())
	_ = zw.Close()
	return gb.Bytes()
}

type fakeCDN struct {
	url, ca  string
	requests atomic.Int32
}

func newFakeCDN(t *testing.T, body []byte) *fakeCDN {
	t.Helper()
	c := &fakeCDN{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.requests.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL + "/soloist_release_arm64.tar.gz"
	c.ca = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(c.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func withCDN(c *fakeCDN) option {
	return func(_ *harness, o *soloist.Options) {
		o.Updates = &soloist.Updates{URL: c.url, CAFile: c.ca, Machine: updater.MachineAArch64}
	}
}

var epochIn = regexp.MustCompile(`epoch=(\d+);`)

// probeFiles answers a probe as audiod would, from what the build file says
func probeFiles(p *audiodv1.SoloistProbe) *audiodv1.SoloistProbeResult {
	r := &audiodv1.SoloistProbeResult{RequestId: p.GetRequestId()}
	raw, err := os.ReadFile(p.GetPath())
	if err != nil {
		r.Code = "io"
		return r
	}
	m := epochIn.FindSubmatch(raw)
	if m == nil {
		r.Code = "version"
		return r
	}
	r.Ok, r.Version = true, "1.3.10.18"
	r.BuildEpoch, _ = strconv.ParseInt(string(m[1]), 10, 64)
	return r
}

func (h *harness) answerProbes(f func(*audiodv1.SoloistProbe) *audiodv1.SoloistProbeResult) {
	h.a.mu.Lock()
	h.a.answer = f
	h.a.mu.Unlock()
}

func (h *harness) switches() int {
	n := 0
	for _, e := range h.a.all() {
		if e.GetSoloistSwitch() != nil {
			n++
		}
	}
	return n
}

func (h *harness) probes() []string {
	var out []string
	for _, e := range h.a.all() {
		if p := e.GetSoloistProbe(); p != nil {
			out = append(out, filepath.Base(p.GetPath()))
		}
	}
	return out
}

func noBuild() *audiodv1.SoloistStatus {
	return &audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_NO_BUILD}
}

func silent(n uint32) *audiodv1.SoloistStatus {
	return &audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_STARTING, SilentStarts: n}
}

func expiry(epoch int64) string {
	b := updater.Build{Epoch: epoch}
	return `"build":"` + b.Date() + `","expiresAt":"` + b.ExpiresAt().Format(time.RFC3339) + `"`
}

// without a build audiod reports no_build: the check runs at once, verifies
// the cdn's build in soloist's jail, installs it and switches audiod onto it
func TestNoBuildFetchesInstallsAndSwitches(t *testing.T) {
	epoch := time.Now().Add(-day).Unix()
	c := newFakeCDN(t, release(t, build(epoch)))
	h := newHarness(t, withLevel(40), withCDN(c))
	h.answerProbes(probeFiles)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(noBuild())
	h.waitLog(`"msg":"soloist_update","result":"ok","build":"` + updater.Build{Epoch: epoch}.Date() + `"`)
	waitFor(t, "the switch", func() bool { return h.switches() == 1 })
	if got := h.probes(); len(got) != 1 || got[0] != "soloist.next" {
		t.Fatalf("probes = %v", got)
	}
	if !bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist")), build(epoch)) {
		t.Fatal("not installed")
	}
	h.waitState(`{"state":"no_build",` + expiry(epoch) + `}`)
	// another no_build of the same run is no new edge
	h.status(noBuild())
	settle()
	if n, probes := c.requests.Load(), h.probes(); n != 1 || len(probes) != 1 {
		t.Fatalf("%d downloads, probes %v", n, probes)
	}
}

// three silent starts of the new build restore the one before while it runs
func TestSilentStartsRestoreThePreviousBuild(t *testing.T) {
	old, fresh := daysLeft(10), time.Now().Add(-day).Unix()
	c := newFakeCDN(t, release(t, build(fresh)))
	h := newHarness(t, withLevel(40), withCDN(c))
	if err := os.MkdirAll(h.opts.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(h.opts.Dir, "soloist"), build(old))
	h.answerProbes(probeFiles)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(silent(1))
	h.status(noBuild())
	waitFor(t, "the switch", func() bool { return h.switches() == 1 })
	// audiod starts the count over at the switch
	for _, n := range []uint32{0, 1, 2} {
		h.status(silent(n))
	}
	settle()
	if h.switches() != 1 {
		t.Fatal("rolled back before the third silent start")
	}
	h.status(silent(3))
	h.waitLog(`"msg":"soloist_rollback","restored":true`)
	waitFor(t, "the switch back", func() bool { return h.switches() == 2 })
	if !bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist")), build(old)) {
		t.Fatal("the build before was not restored")
	}
	h.waitState(`{"state":"starting",` + expiry(old) + `}`)
}

// with nothing to go back to the silent build goes, and soloist stays down
// until the check a day on: a no_build then fetches nothing
func TestSilentStartsDeleteABuildWithNothingBefore(t *testing.T) {
	c := newFakeCDN(t, release(t, build(time.Now().Add(-day).Unix())))
	h := newHarness(t, withLevel(40), withCDN(c))
	h.answerProbes(probeFiles)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(noBuild())
	waitFor(t, "the switch", func() bool { return h.switches() == 1 })
	for _, n := range []uint32{1, 2, 3} {
		h.status(silent(n))
	}
	h.waitLog(`"msg":"soloist_rollback","restored":false`)
	waitFor(t, "the switch", func() bool { return h.switches() == 2 })
	if _, err := os.Stat(filepath.Join(h.opts.Dir, "soloist")); !os.IsNotExist(err) {
		t.Fatalf("the silent build stayed: %v", err)
	}
	h.status(noBuild())
	h.waitState(`{"state":"no_build"}`)
	settle()
	if n := c.requests.Load(); n != 1 {
		t.Fatalf("%d downloads", n)
	}
}

// fail closed: a probe audiod cannot answer is io, and nothing is switched
func TestAudiodGoneFailsTheProbe(t *testing.T) {
	c := newFakeCDN(t, release(t, build(time.Now().Add(-day).Unix())))
	h := newHarness(t, withLevel(40), withCDN(c))
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(noBuild())
	waitFor(t, "the probe", func() bool { return len(h.probes()) == 1 })
	h.s.SoloistGone()
	h.waitLog(`"msg":"soloist_update","result":"failed","reason":"io"`)
	if h.switches() != 0 {
		t.Fatal("switched with audiod gone")
	}
	if _, err := os.Stat(filepath.Join(h.opts.Dir, "soloist")); !os.IsNotExist(err) {
		t.Fatalf("installed unverified: %v", err)
	}
}

// audiod's probe codes: version is a build that does not say what it is
func TestProbeVersionCodeFailsTheCandidate(t *testing.T) {
	c := newFakeCDN(t, release(t, build(time.Now().Add(-day).Unix())))
	h := newHarness(t, withLevel(40), withCDN(c))
	h.answerProbes(func(p *audiodv1.SoloistProbe) *audiodv1.SoloistProbeResult {
		return &audiodv1.SoloistProbeResult{RequestId: p.GetRequestId(), Code: "version"}
	})
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(noBuild())
	h.waitLog(`"msg":"soloist_update","result":"failed","reason":"version"`)
}

// soloist off, or with no key, never downloads anything
func TestNoUpdatesWithoutSoloist(t *testing.T) {
	c := newFakeCDN(t, release(t, build(time.Now().Add(-day).Unix())))
	for _, apply := range []struct {
		cfg     string
		secrets map[string]string
	}{{`{"enabled":false}`, map[string]string{"apiKey": testKey}}, {`{"enabled":true}`, nil}} {
		h := newHarness(t, withLevel(40), withCDN(c))
		h.answerProbes(probeFiles)
		h.ready()
		h.mustApply(apply.cfg, apply.secrets)
		h.status(noBuild())
		settle()
		if n := c.requests.Load(); n != 0 || strings.Contains(h.logs.String(), "soloist_update") {
			t.Fatalf("%s: %d downloads", apply.cfg, n)
		}
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // test dir
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o755); err != nil { //nolint:gosec // a test build is executable
		t.Fatal(err)
	}
}

// an expired build (soloist's exit 10) is replaced at once, kept aside as
// previous, which can no longer run
func TestExpiredBuildIsReplacedAtOnce(t *testing.T) {
	old, fresh := daysLeft(-1), time.Now().Add(-day).Unix()
	c := newFakeCDN(t, release(t, build(fresh)))
	h := newHarness(t, withLevel(40), withCDN(c))
	if err := os.MkdirAll(h.opts.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(h.opts.Dir, "soloist"), build(old))
	h.answerProbes(probeFiles)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(&audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_EXPIRED, ExitCode: 10})
	waitFor(t, "the switch", func() bool { return h.switches() == 1 })
	if !bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist")), build(fresh)) ||
		!bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist.previous")), build(old)) {
		t.Fatal("not swapped")
	}
	h.waitState(`{"state":"expired",` + expiry(fresh) + `}`)
}

// a rollback whose probe of the build before audiod never ran keeps both
// builds, and runs again once audiod reports
func TestRollbackWaitsForAudiod(t *testing.T) {
	old, fresh := daysLeft(10), time.Now().Add(-day).Unix()
	c := newFakeCDN(t, release(t, build(fresh)))
	h := newHarness(t, withLevel(40), withCDN(c))
	if err := os.MkdirAll(h.opts.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(h.opts.Dir, "soloist"), build(old))
	h.answerProbes(probeFiles)
	h.ready()
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.status(noBuild())
	waitFor(t, "the switch", func() bool { return h.switches() == 1 })
	h.answerProbes(func(*audiodv1.SoloistProbe) *audiodv1.SoloistProbeResult { return nil })
	for _, n := range []uint32{1, 2, 3} {
		h.status(silent(n))
	}
	waitFor(t, "the rollback's probe", func() bool { p := h.probes(); return len(p) == 3 && p[2] == "soloist.previous" })
	h.s.SoloistGone()
	h.waitLog(`"msg":"soloist_rollback_deferred"`)
	if !bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist")), build(fresh)) ||
		!bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist.previous")), build(old)) {
		t.Fatal("a deferred rollback changed the builds")
	}
	h.answerProbes(probeFiles)
	h.ready()
	h.status(silent(0))
	h.waitLog(`"msg":"soloist_rollback","restored":true`)
	if !bytes.Equal(readFile(t, filepath.Join(h.opts.Dir, "soloist")), build(old)) {
		t.Fatal("the build before was not restored")
	}
}
