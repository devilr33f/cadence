package vkcapsula

import (
	"os"
	"regexp"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// measured on the capsula: the single_plugin pcm grants 640-frame periods in a 2560-frame buffer and nothing else.
// the 2021 firmware has no single_plugin: its vendor beamformer is the uroc pcm, which gives float samples alone, so
// plug converts them; it grants the same periods
const (
	captureDevice   = "single_plugin"
	captureUroc     = "plug:uroc"
	captureRate     = 16000
	captureChannels = 1
	capturePeriod   = 640
	captureBuffer   = 2560
	playbackDevice  = "playback"
	playbackPeriod  = 960
	playbackBuffer  = 3840
	defaultLevel    = 30
	outputChannels  = 2
	maxBitrate      = 128000
)

func (b *Board) configure() *audiodv1.Configure {
	return &audiodv1.Configure{
		Capture: &audiodv1.CaptureConfig{
			Backend: audiodv1.Backend_BACKEND_ALSA, Device: b.captureDevice(), SampleRate: captureRate,
			Channels: captureChannels, PeriodFrames: capturePeriod, BufferFrames: captureBuffer, Keyword: b.keyword(),
		},
		Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_ALSA, Device: playbackDevice,
			Channels: outputChannels, PeriodFrames: playbackPeriod, BufferFrames: playbackBuffer},
		Mic: &audiodv1.MicConfig{SampleRate: 16000, Bitrate: 24000, FrameMs: 20}, CaFile: b.paths.CABundle,
	}
}

// captureDevice is single_plugin unless the firmware's alsa config defines uroc and no single_plugin (the 2021
// firmware); an unreadable config keeps single_plugin
func (b *Board) captureDevice() string {
	conf, err := os.ReadFile(b.paths.AsoundConf)
	if err != nil {
		return captureDevice
	}
	defines := func(name string) bool {
		return regexp.MustCompile(`(?m)^\s*pcm\.` + regexp.QuoteMeta(name) + `\s*\{`).Match(conf)
	}
	if defines("uroc") && !defines(captureDevice) {
		return captureUroc
	}
	return captureDevice
}

func (b *Board) startAudiod() audio.IO {
	spec := audiod.Spec{Binary: b.audiod, Socket: b.paths.Socket, Configure: b.configure()}
	opts := []audiod.SupervisorOption{
		audiod.WithInput(b.input), audiod.WithMedia(b.media), audiod.WithGroup(b.group),
		audiod.WithToneState(b.ringer.ToneState), audiod.WithCounts(b.metricsEnv.App),
	}
	if b.logs != nil {
		opts = append(opts, audiod.WithLogs(b.logs, b.logsEnv.Local))
	}
	var sup *audiod.Supervisor
	b.vol.Follow(func(level int) func(int) {
		sup = audiod.NewSupervisor(spec, level, b.log, opts...)
		return sup.SetVolume
	})
	b.ringer.UseSpeaker(sup)
	b.wg.Go(func() { sup.Run(b.ctx) })
	return sup.IO(nil)
}
