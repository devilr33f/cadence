package rpi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	// bluealsa's pipe and codec add delay beyond the HDMI buffer
	bluetoothPlaybackBuffer = 7680
	// a speaker's button waits no longer than this on audiod
	controlTimeout = 5 * time.Second
)

// reconfigurer is audiod's supervisor as the speaker's changes reach it.
type reconfigurer interface {
	Reconfigure(cfg *audiodv1.Configure)
	SetVolume(level int)
}

// routeConfigure plays to the active speaker, or to HDMI while none is active.
func routeConfigure(hdmi, address string) *audiodv1.Configure {
	c := configure(hdmi)
	if address != "" {
		c.Output.Device = "bluealsa:DEV=" + address + ",PROFILE=a2dp"
		c.Output.BufferFrames = bluetoothPlaybackBuffer
	}
	return c
}

// route is bluetooth.output's: audiod follows the active speaker over a new connection, and HDMI on disconnect.
func (b *Board) route(address string) {
	b.bt.outMu.Lock()
	defer b.bt.outMu.Unlock()
	b.bt.speaker = address
	out := capschema.MediaGroupOutput{ID: capschema.GroupOutputHDMI, Kind: capschema.GroupOutputHDMI}
	if address != "" {
		out.ID, out.Kind = "bt:"+strings.ToLower(address), capschema.GroupOutputBluetooth
		if b.bt.manager != nil {
			if hint, ok := b.bt.manager.OutputDelay(address); ok {
				out.DelayHintUs = &hint
			}
		}
	}
	b.Board.SetGroupOutput(out)
	if b.bt.sup != nil {
		b.bt.sup.Reconfigure(routeConfigure(b.hdmiDevice, address))
	}
}

// gainLocked is full while the active speaker's own volume carries the level, else the level; callers hold outMu.
func (b *Board) gainLocked(level int) int {
	if b.bt.speaker != "" && b.bt.hard {
		return 100
	}
	return level
}

// applyGain puts each level set on sup as its gain.
func (b *Board) applyGain(sup reconfigurer) func(int) {
	return func(level int) {
		b.bt.outMu.Lock()
		defer b.bt.outMu.Unlock()
		b.bt.level = level
		sup.SetVolume(b.gainLocked(level))
	}
}

// hardware is bluetooth.output's: the speaker's own volume carries the level (on) or audiod's gain does (off).
func (b *Board) hardware(on bool) {
	b.bt.outMu.Lock()
	defer b.bt.outMu.Unlock()
	if b.bt.hard == on {
		return
	}
	b.bt.hard = on
	if b.bt.sup != nil && b.bt.levelKnown {
		b.bt.sup.SetVolume(b.gainLocked(b.bt.level))
	}
}

// speakerLevel is the speaker's own volume buttons: the owner's press on it, as a button on the device (rule 7).
func (b *Board) speakerLevel(level int) {
	if _, err := b.Board.Volume().Set(level, volume.OriginButton); err != nil {
		b.log.Warn("bluetooth_speaker_level_failed")
	}
}

// control is the speaker's play, pause, next and previous on the session that plays here.
func (b *Board) control(ctx context.Context, action string) {
	switch action {
	case bluetooth.ActionToggle:
		action = bluetooth.ActionResume
		if b.MediaPlaying() {
			action = bluetooth.ActionPause
		}
	case bluetooth.ActionResume, bluetooth.ActionPause, bluetooth.ActionNext, bluetooth.ActionPrevious:
	default:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	if err := b.Board.Media().Transport(ctx, action, 0); err != nil {
		b.log.Info("bluetooth_speaker_control_failed", "action", action)
	}
}

// playerStatus is the media session as the speaker's play button sees it.
func playerStatus(s *cadencev2.MediaSessionState) string {
	switch s.GetState() {
	case cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING:
		return bluetooth.PlayerPlaying
	case cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED:
		return bluetooth.PlayerPaused
	default:
		return bluetooth.PlayerStopped
	}
}

// lower caps the level before a new speaker plays (rule 7).
func (b *Board) lower(top int) {
	if _, err := b.Board.Volume().Cap(top, volume.OriginSpeaker); err != nil {
		b.log.Warn("bluetooth_speaker_level_failed")
	}
}

func (b *Board) audiodOptions() []audiod.SupervisorOption {
	if b.bt.manager == nil {
		return nil
	}
	return []audiod.SupervisorOption{audiod.WithBluetooth(b.bt.manager)}
}

// newSupervisor keeps the speaker, its configure and its gain under one lock: a route meanwhile reaches either the
// supervisor before it is kept or this one, never a stale output. tvboard calls it from the volume's Follow.
func (b *Board) newSupervisor(spec audiod.Spec, level int, log *slog.Logger, opts ...audiod.SupervisorOption) (*audiod.Supervisor, func(int)) {
	b.bt.outMu.Lock()
	defer b.bt.outMu.Unlock()
	spec.Configure = routeConfigure(b.hdmiDevice, b.bt.speaker)
	b.bt.level, b.bt.levelKnown = level, true
	sup := audiod.NewSupervisor(spec, b.gainLocked(level), log, opts...)
	b.bt.sup = sup
	return sup, b.applyGain(sup)
}
