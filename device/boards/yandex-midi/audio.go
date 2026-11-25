package midi

import (
	"os"
	"slices"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// v1's board profile. capture: the stock daemon's layout on `default`, seven channels at 48 kHz, four microphones and
// the speaker's stereo echo reference (channel 6 unused), with its 512-frame period required exactly. playback:
// `default` through dmix with a 960-frame period and a 7680-frame buffer, as on the pickle, shared with the media
// sources; audiod's 2880-frame fill cap is the start threshold v1 set; `Master` is behind every path into the card.
const (
	alsaDevice      = "default"
	captureRate     = 48000
	captureChannels = 7
	capturePeriod   = 512
	captureBuffer   = 32768
	playbackPeriod  = 960
	playbackBuffer  = 7680
	mixerControl    = "Master"

	stockLibrary    = "libs/libyandex_evil.so"
	activationModel = "spotter_models/activation/alisa"
	vqePreset       = "yandexmidi"

	// the level the boot wrapper applies when none was kept, so audiod's first VolumeSet matches what it set
	defaultLevel = 30
	// stereo into dmix; the server encodes at most this (S0: 128 kbps for stereo)
	outputChannels = 2
	maxBitrate     = 128000
)

var (
	micChannels       = []uint32{0, 1, 2, 3}
	referenceChannels = []uint32{4, 5}
	// the midi keeps its stock files where yandex put them; the other roots are the pickle's provisioning's, kept
	// for the one search order both boards share
	stockRoots = []string{"/usr/lib/cadence-stock", "/system/vendor/quasar", "/system/vendor/.quasar-disabled"}
)

// configure is audiod's Configure on a midi whose filesystem starts at root; commandModel says the owner pushed a
// command model, else the stock root's own is used when it carries one. The supervisor adds the volume.
func configure(root string, commandModel bool) *audiodv1.Configure {
	p := pathsUnder(root)
	roots := make([]string, len(stockRoots))
	for i, r := range stockRoots {
		roots[i] = under(root, r)
	}
	engines := &audiodv1.VendorEngines{
		Enabled: true, StockRoots: roots, Library: stockLibrary, ActivationModel: activationModel, VqePreset: vqePreset,
		CommandWords: input.CommandWords(),
	}
	if commandModel {
		engines.CommandModel = p.CommandModel
	} else {
		engines.CommandModel = board.StockCommandModel(roots)
	}
	return &audiodv1.Configure{
		Capture: &audiodv1.CaptureConfig{
			Backend: audiodv1.Backend_BACKEND_ALSA, Device: alsaDevice, SampleRate: captureRate, Channels: captureChannels,
			PeriodFrames: capturePeriod, BufferFrames: captureBuffer,
			Layout: &audiodv1.MicLayout{Channels: captureChannels, MicChannels: slices.Clone(micChannels), ReferenceChannels: slices.Clone(referenceChannels),
				SampleRate: captureRate},
			Engines: engines,
		},
		// no speaker eq profile measured yet
		Output: &audiodv1.OutputConfig{
			Backend: audiodv1.Backend_BACKEND_ALSA, Device: alsaDevice, MixerControl: mixerControl, Channels: outputChannels,
			PeriodFrames: playbackPeriod, BufferFrames: playbackBuffer,
		},
		Mic:    &audiodv1.MicConfig{SampleRate: 16000, Bitrate: 24000, FrameMs: 20},
		CaFile: p.CABundle,
	}
}

// configure is the board's Configure, with the command model only once the owner pushed it
func (b *Board) configure() *audiodv1.Configure {
	st, err := os.Stat(b.paths.CommandModel)
	return configure(b.root, err == nil && st.IsDir())
}

// startAudiod runs audiod from the kept level on, following every level set after it, with the input, media,
// soloist, airplay, the group and bluetooth on its connection. Call it once, after setup.
func (b *Board) startAudiod() audio.IO {
	spec := audiod.Spec{Binary: b.audiod, Socket: b.paths.Socket, Configure: b.configure()}
	opts := []audiod.SupervisorOption{
		audiod.WithInput(b.input), audiod.WithMedia(b.media), audiod.WithSoloist(b.soloist), audiod.WithAirplay(b.airplay),
		audiod.WithGroup(b.group), audiod.WithBluetooth(b.bt),
		audiod.WithToneState(b.ringer.ToneState),
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
	b.wg.Go(func() { b.soloist.Run(b.ctx) })
	b.wg.Go(func() { b.bt.Run(b.ctx) })
	// the input's triggers are core's to take
	return sup.IO(nil)
}
