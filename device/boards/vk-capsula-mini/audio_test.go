package vkcapsula_test

import (
	"testing"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func TestConfigureMatchesTheProbedCapsula(t *testing.T) {
	b := vkcapsula.New("", nil)
	cfg := vkcapsula.ConfigureOf(b)
	c := cfg.Capture
	if c.Backend != audiodv1.Backend_BACKEND_ALSA || c.Device != "single_plugin" || c.SampleRate != 16000 ||
		c.Channels != 1 || c.PeriodFrames != 640 || c.BufferFrames != 2560 {
		t.Fatalf("capture = %+v", c)
	}
	if c.Layout != nil || c.Engines != nil {
		t.Fatalf("layout = %+v, engines = %+v", c.Layout, c.Engines)
	}
	o := cfg.Output
	if o.Backend != audiodv1.Backend_BACKEND_ALSA || o.Device != "playback" || o.Channels != 2 ||
		o.PeriodFrames != 960 || o.BufferFrames != 3840 || o.MixerControl != "" {
		t.Fatalf("output = %+v", o)
	}
	if m := cfg.Mic; m.SampleRate != 16000 || m.Bitrate != 24000 || m.FrameMs != 20 {
		t.Fatalf("mic = %+v", m)
	}
	if cfg.CaFile != "/data/cadence/share/ca-certificates.crt" {
		t.Fatalf("ca = %q", cfg.CaFile)
	}
}

func TestConfigureEnablesKeywordWhenModelExists(t *testing.T) {
	tr := capsulaTree(t)
	b := vkcapsula.New("", nil, vkcapsula.WithRoot(tr.root))
	cfg := vkcapsula.ConfigureOf(b)
	k := cfg.Capture.GetKeyword()
	if k == nil {
		t.Fatal("no keyword engine with the stock model present")
	}
	// the offline gate's policy: the stock model with its pos threshold tightened from 0.994
	if k.Kind != "vk-kws" || k.ModelPath != tr.path("usr/share/voice-agent/model.bin") ||
		k.ModelSha256 != "8b33272b9fa8569f85630fd6208f475021ea806a7d79168e070a374569732f3b" ||
		k.PosThreshold != 0.999 || k.SpThreshold != 0.2 || k.RearmFrames != 147 || k.ConsecutiveCalls != 1 {
		t.Fatalf("keyword = %+v", k)
	}
	c := cfg.Capture
	if c.Backend != audiodv1.Backend_BACKEND_ALSA || c.Device != "single_plugin" || c.SampleRate != 16000 ||
		c.Channels != 1 || c.PeriodFrames != 640 || c.BufferFrames != 2560 || c.Engines != nil || c.Layout != nil {
		t.Fatalf("capture = %+v", c)
	}
}

func TestConfigureOmitsKeywordWithoutModel(t *testing.T) {
	tr := capsulaTree(t)
	tr.remove("usr/share/voice-agent/model.bin")
	if k := vkcapsula.ConfigureOf(vkcapsula.New("", nil, vkcapsula.WithRoot(tr.root))).Capture.GetKeyword(); k != nil {
		t.Fatalf("keyword = %+v without a model", k)
	}
	// a directory in its place is no model either
	tr.write("usr/share/voice-agent/model.bin/x", "")
	if k := vkcapsula.ConfigureOf(vkcapsula.New("", nil, vkcapsula.WithRoot(tr.root))).Capture.GetKeyword(); k != nil {
		t.Fatalf("keyword = %+v with a directory for the model", k)
	}
}

func TestConfigureCapturesThroughUrocOnThe2021Firmware(t *testing.T) {
	tr := capsulaTree(t)
	tr.write("etc/asound.conf", "pcm.!default{\n    type hw\n}\npcm.uroc {\n    type uroc\n}\npcm.playback {\n    type dmix\n}\n")
	if d := vkcapsula.ConfigureOf(vkcapsula.New("", nil, vkcapsula.WithRoot(tr.root))).Capture.Device; d != "plug:uroc" {
		t.Fatalf("capture device = %q on the 2021 firmware", d)
	}
	tr.write("etc/asound.conf", "pcm.uroc {\n    type uroc\n}\npcm.single_plugin {\n    type single\n}\n")
	if d := vkcapsula.ConfigureOf(vkcapsula.New("", nil, vkcapsula.WithRoot(tr.root))).Capture.Device; d != "single_plugin" {
		t.Fatalf("capture device = %q with single_plugin defined", d)
	}
}
