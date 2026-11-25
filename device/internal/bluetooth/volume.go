package bluetooth

import (
	"context"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type volumeWrite struct {
	pcm, id string
	v       uint8
}

// volume: a phone's first volume after it connects is answered with our level, never applied (rule 7: connecting
// never changes loudness; bluez 5.70 starts a transport at a 127 placeholder); a later move goes through the sender
// policy, our own write's echo aside; and every level the policy sets or keeps goes back to the phones
func (b *Bluetooth) volume(ctx context.Context, be Backend, snap Snapshot) {
	if b.opts.Senders == nil || snap.Adapter == nil {
		return
	}
	level, known := b.opts.Level()
	b.mu.Lock()
	dirty := b.levelDirty
	b.levelDirty = false
	present := map[string]bool{}
	var writes []volumeWrite
	var moves []int
	for _, p := range snap.PCMs {
		if p.Transport != TransportA2DPSink {
			continue
		}
		present[p.Path] = true
		d, _ := deviceAt(snap, p.Device)
		id := ShortID(snap.Adapter.Address, d.Address)
		v := pcmLevel(p.Volume)
		tr, ok := b.volumes[p.Path]
		switch {
		case !ok:
			tr = &pcmVolume{seen: v}
			b.volumes[p.Path] = tr
			if known && toPercent(v) != level {
				writes = append(writes, volumeWrite{pcm: p.Path, id: id, v: toAVRCP(level)})
			}
			continue
		case v == tr.seen:
		case tr.wrote && v == tr.written:
			// our own write's echo
			tr.seen = v
		default:
			tr.seen = v
			moves = append(moves, toPercent(v))
			b.log.Info("bluetooth_volume_moved", "id", id, "level", toPercent(v))
			continue
		}
		if dirty && known && toPercent(tr.seen) != level {
			writes = append(writes, volumeWrite{pcm: p.Path, id: id, v: toAVRCP(level)})
		}
	}
	for path := range b.volumes {
		if !present[path] {
			delete(b.volumes, path)
		}
	}
	b.mu.Unlock()
	for _, w := range writes {
		if err := be.SetVolume(ctx, w.pcm, w.v); err != nil {
			b.log.Warn("bluetooth_volume_failed", "id", w.id)
			continue
		}
		b.mu.Lock()
		if tr, ok := b.volumes[w.pcm]; ok {
			tr.written, tr.wrote = w.v, true
		}
		b.mu.Unlock()
	}
	for _, level := range moves {
		b.opts.Senders.SenderVolume(audiodv1.Sender_SENDER_BLUETOOTH, uint32(level)) //nolint:gosec // 0-100
	}
}
