package micro

import (
	"os"
	"slices"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// the stock 0.369.1.48 profile: four microphones selected from the 8-channel PDM
// slave plus the speaker's stereo loopback in default's 6-channel multi stream
const (
	alsaDevice      = "default"
	captureRate     = 48000
	captureChannels = 6
	capturePeriod   = 512
	captureBuffer   = 2048
	playbackPeriod  = 960
	playbackBuffer  = 7680
	mixerControl    = "Master"
	stockLibrary    = "libs/libyandex_evil.so"
	activationModel = "spotter_models/activation/alisa"
	vqePreset       = "yandexmicro"
	defaultLevel    = 30
	outputChannels  = 2
	maxBitrate      = 128000
)

var (
	micChannels       = []uint32{0, 1, 2, 3}
	referenceChannels = []uint32{4, 5}
	stockRoots        = []string{"/system/vendor/quasar"}
)

func (b *Board) configure() *audiodv1.Configure {
	p := b.paths
	roots := make([]string, len(stockRoots))
	for i, r := range stockRoots {
		roots[i] = under(b.root, r)
	}
	engines := &audiodv1.VendorEngines{Enabled: true, StockRoots: roots, Library: stockLibrary,
		ActivationModel: activationModel, VqePreset: vqePreset}
	if st, err := os.Stat(p.CommandModel); err == nil && st.IsDir() {
		engines.CommandModel = p.CommandModel
	} else {
		engines.CommandModel = board.StockCommandModel(roots)
	}
	return &audiodv1.Configure{
		Capture: &audiodv1.CaptureConfig{
			Backend: audiodv1.Backend_BACKEND_ALSA, Device: alsaDevice, SampleRate: captureRate,
			Channels: captureChannels, PeriodFrames: capturePeriod, BufferFrames: captureBuffer,
			Layout: &audiodv1.MicLayout{Channels: captureChannels, MicChannels: slices.Clone(micChannels),
				ReferenceChannels: slices.Clone(referenceChannels), SampleRate: captureRate}, Engines: engines,
		},
		Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_ALSA, Device: alsaDevice,
			MixerControl: mixerControl, Channels: outputChannels, PeriodFrames: playbackPeriod, BufferFrames: playbackBuffer},
		Mic: &audiodv1.MicConfig{SampleRate: 16000, Bitrate: 24000, FrameMs: 20}, CaFile: p.CABundle,
	}
}

func (b *Board) startAudiod() audio.IO {
	spec := audiod.Spec{Binary: b.audiod, Socket: b.paths.Socket, Configure: b.configure()}
	opts := []audiod.SupervisorOption{
		audiod.WithInput(b.input), audiod.WithMedia(b.media), audiod.WithSoloist(b.senders),
		audiod.WithGroup(b.group), audiod.WithBluetooth(b.bt), audiod.WithToneState(b.ringer.ToneState),
		audiod.WithCounts(b.metricsEnv.App),
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
	b.wg.Go(func() { b.senders.Run(b.ctx) })
	b.wg.Go(func() { b.bt.Run(b.ctx) })
	return sup.IO(nil)
}
