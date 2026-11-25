package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
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

func env(v string) func(string) string { return func(string) string { return v } }

func newLog(buf *syncBuf) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

func TestRunReady(t *testing.T) {
	buf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- core.Run(ctx, config.Config{Board: "sim"}, []board.Board{sim.New(env("1"), "", nil)}, newLog(buf), "v1")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"board_ready"`) {
		if time.Now().After(deadline) {
			t.Fatal("no board_ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	r, s := strings.Index(out, `"msg":"board_ready"`), strings.Index(out, `"msg":"shutdown"`)
	if r < 0 || s < r {
		t.Fatalf("log order: %s", out)
	}
	if !strings.Contains(out, `"board":"sim"`) || !strings.Contains(out, `"version":"v1"`) {
		t.Fatalf("attrs: %s", out)
	}
}

func TestRunUnknownBoard(t *testing.T) {
	buf := &syncBuf{}
	err := core.Run(context.Background(), config.Config{Board: "yandex-pickle"}, []board.Board{sim.New(env("1"), "", nil)}, newLog(buf), "v")
	if !errors.Is(err, board.ErrUnknownBoard) {
		t.Fatalf("err = %v", err)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"board_refused"`) || !strings.Contains(out, `"reason":"unknown"`) {
		t.Fatalf("log: %s", out)
	}
}

func TestRunFingerprint(t *testing.T) {
	buf := &syncBuf{}
	err := core.Run(context.Background(), config.Config{Board: "sim"}, []board.Board{sim.New(env(""), "", nil)}, newLog(buf), "v")
	if !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"board_refused"`) || !strings.Contains(out, `"reason":"fingerprint"`) {
		t.Fatalf("log: %s", out)
	}
}

func waitLog(t *testing.T, buf *syncBuf, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"`+msg+`"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s; log: %s", msg, buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunLinksToServer(t *testing.T) {
	ls := newLinkServer(t)
	dir := t.TempDir()
	store, err := syncstore.Open(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("audio.output", 2, json.RawMessage(`{"level":30}`)); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Config{Board: "sim", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: dir}
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx, cfg, []board.Board{sim.New(env("1"), "", nil)}, newLog(buf), "v1") }()

	p := ls.accept(t)
	// the advertisement switch's states come in any order beside the board's
	next := func() *cadencev2.Envelope {
		for {
			if env := p.recv(); env.GetStateDelta().GetCapability() != "network.discovery" {
				return env
			}
		}
	}
	if h := p.handshake(true); h.GetBoard() != "sim" || h.GetDaemonVersion() != "v1" {
		t.Fatalf("hello = %v", h)
	}
	// the board's capability, then the device's own advertisement switch and session settings
	if r := next().GetCapabilityReport(); len(r.GetDescriptors()) != 3 || r.GetDescriptors()[0].GetId() != "audio.output" ||
		r.GetDescriptors()[1].GetId() != "network.discovery" || r.GetDescriptors()[2].GetId() != "session" {
		t.Fatalf("report = %v", r)
	}
	// the stored legacy config seeded the level at boot, before the link came up
	if d := next().GetStateDelta(); d.GetJson() != `{"level":30,"channels":1,"maxBitrate":64000}` {
		t.Fatalf("state = %v", d)
	}

	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{Capability: "audio.output", Generation: 3, Json: `{}`}}})
	if a := next().GetConfigApplied(); a.GetGeneration() != 3 || a.GetErrorCode() != "" {
		t.Fatalf("applied = %v", a)
	}
	if gen, _, _, _ := store.Load("audio.output"); gen != 3 {
		t.Fatalf("stored gen = %d", gen)
	}

	p.command("r1", "audio.output", "set_level", `{"level":40}`)
	var gotResult, gotState bool
	for !gotResult || !gotState {
		switch env := next(); {
		case env.GetCommandResult() != nil:
			if r := env.GetCommandResult(); r.GetRequestId() != "r1" || r.GetCode() != "" || r.GetJson() != `{"level":40}` {
				t.Fatalf("result = %v", r)
			}
			gotResult = true
		case env.GetStateDelta() != nil:
			if j := env.GetStateDelta().GetJson(); j != `{"level":40,"channels":1,"maxBitrate":64000}` {
				t.Fatalf("state = %s", j)
			}
			gotState = true
		default:
			t.Fatalf("unexpected %v", env)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, msg := range []string{"board_ready", "link_up", "shutdown"} {
		if !strings.Contains(out, `"msg":"`+msg+`"`) {
			t.Fatalf("no %s; log: %s", msg, out)
		}
	}
	if strings.Contains(out, token) {
		t.Fatal("log carries the token")
	}
}

func TestRunWithoutLinkOnDuplicateCapabilities(t *testing.T) {
	ls := newLinkServer(t)
	dup := fakeBoard{id: "dup", caps: []capability.Provider{newProvider("audio.output"), newProvider("audio.output")}}
	cfg := config.Config{Board: "dup", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: t.TempDir()}
	buf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx, cfg, []board.Board{dup}, newLog(buf), "v1") }()
	waitLog(t, buf, "capability_duplicate")
	waitLog(t, buf, "board_ready")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := ls.dials.Load(); n != 0 {
		t.Fatalf("dialed %d times", n)
	}
	if !strings.Contains(buf.String(), `"capability":"audio.output"`) {
		t.Fatalf("log: %s", buf.String())
	}
}

// no part could carry the descriptor, so the server would never get the report
func TestRunWithoutLinkOnOversizeDescriptor(t *testing.T) {
	ls := newLinkServer(t)
	huge := fakeBoard{id: "huge", caps: []capability.Provider{newProvider("a.small"), bigProvider{newProvider("b.huge"), wire.MaxFrameBytes}}}
	cfg := config.Config{Board: "huge", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: t.TempDir()}
	buf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx, cfg, []board.Board{huge}, newLog(buf), "v1") }()
	waitLog(t, buf, "capability_too_large")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := ls.dials.Load(); n != 0 {
		t.Fatalf("dialed %d times", n)
	}
	if !strings.Contains(buf.String(), `"msg":"capability_too_large","capability":"b.huge"`) {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestRunRefusesUnreadableCAFile(t *testing.T) {
	ls := newLinkServer(t)
	dir := t.TempDir()
	cfg := config.Config{
		Board: "sim", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: dir,
		CAFile: filepath.Join(dir, "missing.pem"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := core.Run(ctx, cfg, []board.Board{sim.New(env("1"), "", nil)}, newLog(&syncBuf{}), "v1"); err == nil {
		t.Fatal("run started without its ca file")
	}
	if n := ls.dials.Load(); n != 0 {
		t.Fatalf("dialed %d times", n)
	}
}

func TestRunHidesStorePath(t *testing.T) {
	ls := newLinkServer(t)
	// a file where the data dir should be: the store cannot be made
	dir := filepath.Join(t.TempDir(), "datadir-marker")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Board: "sim", ServerURL: ls.url, DeviceID: "dev-1", DeviceToken: token, DataDir: dir}
	buf := &syncBuf{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := core.Run(ctx, cfg, []board.Board{sim.New(env("1"), "", nil)}, newLog(buf), "v1")
	if err == nil {
		t.Fatal("run started without a store")
	}
	if strings.Contains(err.Error(), "datadir-marker") || strings.Contains(buf.String(), "datadir-marker") {
		t.Fatalf("path leaked: err = %v; log: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), `"msg":"link_refused","reason":"store"`) {
		t.Fatalf("log: %s", buf.String())
	}
	if n := ls.dials.Load(); n != 0 {
		t.Fatalf("dialed %d times", n)
	}
}
