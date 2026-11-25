package bluetooth

import (
	"context"
	"time"
)

const (
	// a speaker's volume report this soon after our write answers it, never moves the level: bluealsa and the
	// speaker settle on our value in that time
	volumeSettle = 1500 * time.Millisecond
	// a play this soon after the speaker connected is its own (many send one on connect), never the owner's press
	connectPlayGuard = 3 * time.Second
	pressQueue       = 4
)

// Speaker actions, as OutputOptions.Control hears them.
const (
	ActionResume   = "resume"
	ActionPause    = "pause"
	ActionToggle   = "toggle"
	ActionNext     = "next"
	ActionPrevious = "previous"
)

// hardVolume follows the active speaker's own (avrcp absolute) volume; only Run's goroutine touches it
type hardVolume struct {
	// the pcm we took over, "" for none
	pcm string
	// the board was told the speaker carries the level
	hard bool
	// the speaker's volume as last seen, and our last write
	seen, written uint8
	writtenAt     time.Time
}

// LevelChanged says the device's level changed, so the speaker's own volume follows; it never blocks.
func (b *Bluetooth) LevelChanged() {
	b.mu.Lock()
	b.levelDirty = true
	if b.out != nil {
		b.out.levelDirty = true
	}
	b.mu.Unlock()
	b.wake()
}

// speakerVolume keeps the active speaker's own volume at the level and the level at the speaker's buttons, when it
// takes absolute volume: bluealsa's soft volume off, so its pcm volume is the speaker's. The board's gain stays at
// full only once the speaker holds our level, and goes back to the level before anything else plays (route), so
// nothing ever plays louder than the level. Only Run's goroutine calls it.
func (b *Bluetooth) speakerVolume(ctx context.Context, be Backend, snap Snapshot) {
	o := b.out
	if o == nil {
		return
	}
	level, known := b.opts.Level()
	b.mu.Lock()
	dirty := o.levelDirty
	o.levelDirty = false
	address, id := o.address, o.active
	b.mu.Unlock()
	hv := &o.hv
	var pcm PCM
	found := false
	for _, p := range snap.PCMs {
		if p.Transport == TransportA2DPSource && address != "" {
			if d, ok := deviceAt(snap, p.Device); ok && d.Address == address {
				pcm, found = p, true
				break
			}
		}
	}
	if !found || !known || !pcm.HardVolume {
		b.setHard(false)
		if found && hv.pcm == pcm.Path {
			// the speaker stopped taking absolute volume: bluealsa scales again, at full, under the board's gain
			_ = be.SetSoftVolume(ctx, pcm.Path, true)
			_ = be.SetVolume(ctx, pcm.Path, 127)
			b.log.Info("bluetooth_speaker_volume", "id", id, "mode", "software")
		}
		hv.pcm = ""
		return
	}
	v := pcmLevel(pcm.Volume)
	now := b.opts.Now()
	write := false
	switch {
	case hv.pcm != pcm.Path || pcm.SoftVolume:
		// a speaker newly active (or its pcm made anew): our level is its first value, whatever it kept
		if pcm.SoftVolume {
			if err := be.SetSoftVolume(ctx, pcm.Path, false); err != nil {
				b.log.Warn("bluetooth_speaker_volume_failed", "id", id, "step", "soft_volume")
				return
			}
		}
		hv.pcm, hv.seen = pcm.Path, v
		write = true
	case v == hv.seen:
		write = dirty && toPercent(v) != level
	case v == hv.written:
		// our own write's echo
		hv.seen = v
	case now.Sub(hv.writtenAt) < volumeSettle:
		hv.seen = v
		write = true
	default:
		hv.seen = v
		moved := toPercent(v)
		b.log.Info("bluetooth_speaker_volume_moved", "id", id, "level", moved)
		if o.opts.SpeakerLevel != nil {
			o.opts.SpeakerLevel(moved)
		}
	}
	if write {
		w := toAVRCP(level)
		if err := be.SetVolume(ctx, pcm.Path, w); err != nil {
			b.log.Warn("bluetooth_speaker_volume_failed", "id", id, "step", "volume")
			hv.pcm = ""
			b.setHard(false)
			return
		}
		hv.written, hv.writtenAt = w, now
	}
	if !hv.hard {
		b.log.Info("bluetooth_speaker_volume", "id", id, "mode", "hardware", "level", level)
	}
	b.setHard(true)
}

// setHard tells the board whether the speaker's own volume carries the level, on a change
func (b *Bluetooth) setHard(on bool) {
	o := b.out
	if o.hv.hard == on {
		return
	}
	o.hv.hard = on
	if o.opts.Hardware != nil {
		o.opts.Hardware(on)
	}
}

// SetPlaying is the device's media status as the speaker's buttons should see it (PlayerPlaying, PlayerPaused or
// PlayerStopped); it never blocks.
func (b *Bluetooth) SetPlaying(status string) {
	b.mu.Lock()
	if b.out == nil || b.out.status == status {
		b.mu.Unlock()
		return
	}
	b.out.status = status
	b.mu.Unlock()
	b.wake()
}

// player registers our media player on each bluez with the adapter, and keeps its status the device's
func (b *Bluetooth) player(ctx context.Context, be Backend, snap Snapshot) {
	o := b.out
	if o == nil {
		return
	}
	if snap.Adapter == nil {
		// a new adapter (bluez restarted) needs our player again
		o.playerUp = false
		return
	}
	if !o.playerUp {
		if err := be.RegisterPlayer(ctx, b); err != nil {
			b.log.Warn("bluetooth_player_failed")
			return
		}
		o.playerUp, o.shownStatus = true, ""
		b.log.Info("bluetooth_player_registered")
	}
	b.mu.Lock()
	status := o.status
	b.mu.Unlock()
	if status == "" {
		status = PlayerStopped
	}
	if status == o.shownStatus {
		return
	}
	if err := be.SetPlayerStatus(ctx, status); err != nil {
		b.log.Warn("bluetooth_player_status_failed")
		return
	}
	o.shownStatus = status
}

// PlayerCommand is a speaker's button on our player: queued for the board, never blocking (bluez waits on it).
func (b *Bluetooth) PlayerCommand(method string) {
	o := b.out
	if o == nil {
		return
	}
	var action string
	switch method {
	case "Play":
		action = ActionResume
	case "Pause", "Stop":
		// a stop pauses: the session stays to resume from the speaker
		action = ActionPause
	case "PlayPause":
		action = ActionToggle
	case "Next":
		action = ActionNext
	case "Previous":
		action = ActionPrevious
	default:
		return
	}
	b.mu.Lock()
	since := b.opts.Now().Sub(o.activeAt)
	id := o.active
	b.mu.Unlock()
	if (action == ActionResume || action == ActionToggle) && (id == "" || since < connectPlayGuard) {
		b.log.Info("bluetooth_speaker_control_ignored", "id", id, "action", action, "reason", "connect")
		return
	}
	select {
	case o.presses <- action:
		b.log.Info("bluetooth_speaker_control", "id", id, "action", action)
	default:
		b.log.Warn("bluetooth_speaker_control_dropped", "id", id, "action", action)
	}
}

// presses hands the speaker's buttons to the board, one at a time, until ctx ends
func (b *Bluetooth) presses(ctx context.Context) {
	o := b.out
	if o == nil || o.opts.Control == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-o.presses:
			o.opts.Control(ctx, a)
		}
	}
}

var _ PlayerControl = (*Bluetooth)(nil)
