package rpi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	rpi "git.shino.boo/devilreef/cadence/device/boards/rpi"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	speakerAddr = "AA:BB:CC:DD:EE:10"
	hdmiDevice  = "hdmi:CARD=vc4hdmi0"
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

func newBoard(t *testing.T, reason string, be *btfake.Backend) (*rpi.Board, *syncBuf) {
	t.Helper()
	logs := &syncBuf{}
	getenv := func(key string) string {
		if key == "CADENCE_RPI_ALSA_DEVICE" {
			return hdmiDevice
		}
		return ""
	}
	b := rpi.New(getenv, t.TempDir(), slog.New(slog.NewJSONHandler(logs, nil)),
		rpi.WithRoot(t.TempDir()), rpi.WithAudiod("/nonexistent/audiod"),
		rpi.WithStack(reason, func(context.Context) (bluetooth.Backend, error) { return be, nil }))
	t.Cleanup(func() { _ = b.Close() })
	return b, logs
}

func ids(b *rpi.Board) []string {
	var out []string
	for _, c := range b.Capabilities() {
		out = append(out, c.Descriptor().ID)
	}
	return out
}

func TestFingerprintByDeviceTreeModel(t *testing.T) {
	root := t.TempDir()
	getenv := func(string) string { return "" }
	b := rpi.New(getenv, t.TempDir(), nil, rpi.WithRoot(root))
	if err := b.Fingerprint(context.Background()); !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
	dt := filepath.Join(root, "proc", "device-tree")
	if err := os.MkdirAll(dt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dt, "model"), []byte("Raspberry Pi 4 Model B Rev 1.5\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.ID() != "rpi" {
		t.Fatalf("id %q", b.ID())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// one board carries the screen, cec and bluetooth; bluetooth.output only where the stack qualifies, and no audiod
// url player (the screen's own player takes urls)
func TestCapabilities(t *testing.T) {
	b, _ := newBoard(t, "", btfake.New())
	got := ids(b)
	for _, want := range []string{
		"audio.output", "audio.input", "media.player", "media.eq", "media.viz", "media.group", "display.screen",
		"display.cec", "bluetooth", "bluetooth.output",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("capabilities %v lack %s", got, want)
		}
	}
	if slices.Contains(got, "media.url") {
		t.Errorf("a screen reports audiod's url player: %v", got)
	}
	b, logs := newBoard(t, "profile", btfake.New())
	got = ids(b)
	if !slices.Contains(got, "bluetooth") || slices.Contains(got, "bluetooth.output") {
		t.Fatalf("capabilities without a2dp-source %v", got)
	}
	if !strings.Contains(logs.String(), `"msg":"bluetooth_output_unavailable","reason":"profile"`) {
		t.Fatalf("logs %s", logs)
	}
	var bb board.Board = b
	if !is[board.Inputs](bb) {
		t.Error("the pi has no inputs")
	}
	for name, ok := range map[string]bool{"lights": is[board.Lights](bb), "alarms": is[board.Alarms](bb)} {
		if ok {
			t.Errorf("the pi has %s", name)
		}
	}
}

func is[T any](b board.Board) bool {
	_, ok := b.(T)
	return ok
}

// audiod plays to bluealsa's a2dp pcm for the active speaker, and to hdmi with none
func TestConfigureFollowsTheSpeaker(t *testing.T) {
	c := rpi.Configure("")
	out := c.GetOutput()
	if out.GetDevice() != hdmiDevice || out.GetBackend() != audiodv1.Backend_BACKEND_ALSA || c.GetCapture() != nil || c.GetCaFile() == "" {
		t.Fatalf("hdmi: %v", c)
	}
	out = rpi.Configure(speakerAddr).GetOutput()
	if out.GetBackend() != audiodv1.Backend_BACKEND_ALSA || out.GetDevice() != "bluealsa:DEV="+speakerAddr+",PROFILE=a2dp" ||
		out.GetMixerControl() != "" || out.GetChannels() != 2 {
		t.Fatalf("output %v", out)
	}
}

type supervisor struct {
	mu     sync.Mutex
	cfgs   []*audiodv1.Configure
	levels []int
}

func (s *supervisor) SetVolume(level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels = append(s.levels, level)
}

func (s *supervisor) gain() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.levels) == 0 {
		return -1
	}
	return s.levels[len(s.levels)-1]
}

func (s *supervisor) Reconfigure(cfg *audiodv1.Configure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfgs = append(s.cfgs, cfg)
}

func (s *supervisor) last() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cfgs) == 0 {
		return "", false
	}
	return s.cfgs[len(s.cfgs)-1].GetOutput().GetDevice(), true
}

func waitDevice(t *testing.T, s *supervisor, device string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, ok := s.last(); ok && d == device {
			return
		}
		if time.Now().After(deadline) {
			d, _ := s.last()
			t.Fatalf("output %q, want %q", d, device)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func groupOutput(t *testing.T, b *rpi.Board) capschema.MediaGroupOutput {
	t.Helper()
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID != capschema.MediaGroupID {
			continue
		}
		select {
		case raw := <-p.State():
			var st capschema.MediaGroupStatus
			if json.Unmarshal(raw, &st) != nil || st.Output == nil {
				t.Fatalf("group state = %s", raw)
			}
			return *st.Output
		case <-time.After(time.Second):
			t.Fatal("no group state")
		}
	}
	t.Fatal("no media.group")
	return capschema.MediaGroupOutput{}
}

// audiod follows the speaker's connection, a new speaker first brings the level down to 40, and a disconnect returns
// the output to hdmi
func TestOutputFollowsTheSpeaker(t *testing.T) {
	be := btfake.New()
	b, logs := newBoard(t, "", be)
	sup := &supervisor{}
	b.UseSupervisor(sup)
	if _, err := b.Volume().Set(70, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	be.AddSpeaker(speakerAddr, "JBL", true)
	be.Update(func(s *bluetooth.Snapshot) {
		for i := range s.PCMs {
			if s.PCMs[i].Path == btfake.SourcePCMPath(speakerAddr) {
				s.PCMs[i].DelayHintUs, s.PCMs[i].DelayKnown = 145000, true
			}
		}
	})
	waitDevice(t, sup, "bluealsa:DEV="+speakerAddr+",PROFILE=a2dp")
	if out := groupOutput(t, b); out.ID != "bt:aa:bb:cc:dd:ee:10" || out.Kind != capschema.GroupOutputBluetooth ||
		out.DelayHintUs == nil || *out.DelayHintUs != 145000 {
		t.Fatalf("bluetooth output = %+v", out)
	}
	if level := b.Volume().Level(); level != 40 {
		t.Fatalf("level %d", level)
	}
	be.Drop(speakerAddr)
	waitDevice(t, sup, hdmiDevice)
	if out := groupOutput(t, b); out.ID != "hdmi" || out.Kind != capschema.GroupOutputHDMI || out.DelayHintUs != nil {
		t.Fatalf("hdmi output = %+v", out)
	}
	if strings.Contains(logs.String(), speakerAddr) || strings.Contains(logs.String(), "JBL") {
		t.Fatalf("an address or a name in the logs: %s", logs)
	}
}

// a speaker that takes absolute volume carries the level itself, audiod's gain at full; its buttons move the level,
// and once it is gone hdmi's software gain is the level again
func TestSpeakerVolumeCarriesTheLevel(t *testing.T) {
	be := btfake.New()
	b, _ := newBoard(t, "", be)
	sup := &supervisor{}
	b.UseSupervisor(sup)
	if _, err := b.Volume().Set(30, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	be.SetHardVolume(speakerAddr, true)
	be.AddSpeaker(speakerAddr, "JBL", true)
	waitDevice(t, sup, "bluealsa:DEV="+speakerAddr+",PROFILE=a2dp")
	waitGain(t, sup, 100)
	pcm := btfake.SourcePCMPath(speakerAddr)
	if !slices.Contains(be.Calls(), "SetVolume 38 "+pcm) {
		t.Fatalf("calls %v", be.Calls())
	}
	time.Sleep(1600 * time.Millisecond)
	be.SpeakerVolume(speakerAddr, 64)
	deadline := time.Now().Add(5 * time.Second)
	for b.Volume().Level() != 50 {
		if time.Now().After(deadline) {
			t.Fatalf("level %d after the speaker's button", b.Volume().Level())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g := sup.gain(); g != 100 {
		t.Fatalf("gain %d", g)
	}
	be.Drop(speakerAddr)
	waitDevice(t, sup, hdmiDevice)
	waitGain(t, sup, 50)
}

func waitGain(t *testing.T, s *supervisor, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.gain() != want {
		if time.Now().After(deadline) {
			t.Fatalf("gain %d, want %d", s.gain(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPlayerStatus(t *testing.T) {
	for state, want := range map[cadencev2.PlaybackState]string{
		cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING: bluetooth.PlayerPlaying,
		cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED:  bluetooth.PlayerPaused,
		cadencev2.PlaybackState_PLAYBACK_STATE_IDLE:    bluetooth.PlayerStopped,
	} {
		if got := rpi.PlayerStatus(&cadencev2.MediaSessionState{State: state}); got != want {
			t.Errorf("%v: %s", state, got)
		}
	}
}
