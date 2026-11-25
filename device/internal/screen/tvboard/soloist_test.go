package tvboard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// a soloist of the board's own (Options.Soloist): its capability is among the board's, its config reaches audiod
// with the board's paths and the device's name, and its state is audiod's
func newSoloistBoard(t *testing.T) (*Board, *soloist.Soloist) {
	t.Helper()
	dir := t.TempDir()
	var made *soloist.Soloist
	b := New(Options{
		ID: "screen", DataDir: dir, DefaultLevel: 30, ScreenSocket: filepath.Join(dir, "s.sock"),
		TapPath: filepath.Join(dir, "tap.sock"), AudiodBinary: "/nonexistent/audiod",
		AudiodSocket: filepath.Join(dir, "a.sock"),
		Configure:    &audiodv1.Configure{Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_FILE, Channels: 2}},
		Soloist: func(vol *volume.Setter) *soloist.Soloist {
			made = soloist.New(vol, soloist.Options{
				Dir: filepath.Join(dir, "soloist"), ScratchDir: filepath.Join(dir, "run", "soloist"),
				CAFile: filepath.Join(dir, "ca.crt"),
			}, nil)
			return made
		},
	})
	t.Cleanup(func() { _ = b.Close() })
	b.setup()
	if made == nil {
		t.Fatal("the factory never ran")
	}
	return b, made
}

func soloistCapabilityOf(t *testing.T, b *Board) capability.Provider {
	t.Helper()
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "media.soloist" {
			return p
		}
	}
	t.Fatal("no media.soloist")
	return nil
}

func (s *sink) soloistConfig() *audiodv1.SoloistConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last *audiodv1.SoloistConfig
	for _, e := range s.envs {
		if c := e.GetSoloistConfig(); c != nil {
			last = c
		}
	}
	return last
}

// the capability is the board's, and a connected audiod gets its config with the board's paths and the device's name
func TestSoloistOptionWiresTheManager(t *testing.T) {
	b, manager := newSoloistBoard(t)
	soloistCap := soloistCapabilityOf(t, b)
	out := &sink{}
	manager.SoloistReady(out.send)
	cfg := out.soloistConfig()
	if cfg.GetDir() != filepath.Join(b.o.DataDir, "soloist") || cfg.GetScratchDir() != filepath.Join(b.o.DataDir, "run", "soloist") ||
		cfg.GetCaFile() != filepath.Join(b.o.DataDir, "ca.crt") || cfg.GetDeviceName() != "Cadence" {
		t.Fatalf("config %v", cfg)
	}

	// the device's own name is the receiver's while the config sets none, and a change goes out at once
	b.SetDeviceName("Гостиная")
	if name := out.soloistConfig().GetDeviceName(); name != "Гостиная" {
		t.Fatalf("receiver name %q", name)
	}

	// audiod's status is the capability's state once a config enabled soloist with its key
	secrets, ok := soloistCap.(capability.SecretApplier)
	if !ok {
		t.Fatal("media.soloist takes no secrets")
	}
	if err := secrets.ApplySecrets(context.Background(), 1, json.RawMessage(`{"enabled":true}`),
		map[string]string{"apiKey": "test-key-not-real"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, soloistCap, `"state":"starting"`)
	manager.SoloistEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistStatus{SoloistStatus: &audiodv1.SoloistStatus{
		State: audiodv1.SoloistState_SOLOIST_STATE_FAILED, Code: "scratch_failed",
	}}})
	waitState(t, soloistCap, `"state":"failed","code":"scratch_failed"`)
}

// a board without the option keeps the senders alone: no media.soloist capability, and nothing of one to forward
func TestNoSoloistOptionHasNoCapability(t *testing.T) {
	b := newBoard(t)
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "media.soloist" {
			t.Fatal("media.soloist without the option")
		}
	}
	b.SetDeviceName("Kitchen")
}
