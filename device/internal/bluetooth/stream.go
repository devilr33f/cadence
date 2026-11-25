package bluetooth

import (
	"context"
	"slices"
	"strings"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// BluetoothReady sends a new audiod connection the config and uses the connection; a phone already streaming is
// opened again on it.
func (b *Bluetooth) BluetoothReady(send func(*audiodv1.Envelope) error, sendFD func(*audiodv1.Envelope, int) error) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	_ = send(b.configEnvelope())
	b.mu.Lock()
	b.send, b.sendFD = send, sendFD
	b.conn++
	b.mu.Unlock()
	b.wake()
}

// BluetoothEnvelope takes a BluetoothControl from audiod; it never blocks, and drops one past a full queue.
func (b *Bluetooth) BluetoothEnvelope(env *audiodv1.Envelope) {
	c := env.GetBluetoothControl()
	if c == nil {
		return
	}
	select {
	case b.ctls <- c:
	default:
		b.log.Warn("bluetooth_control_dropped")
	}
}

// BluetoothGone follows the end of the connection BluetoothReady took: audiod closed its fds, and the pcms are let
// go until the next one.
func (b *Bluetooth) BluetoothGone() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.send, b.sendFD = nil, nil
	for path, st := range b.streams {
		b.endLocked(path, st, "audiod")
	}
}

func (b *Bluetooth) configEnvelope() *audiodv1.Envelope {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothConfig{BluetoothConfig: &audiodv1.BluetoothConfig{
		Enabled: b.cfg.Enabled && b.cfg.Receive,
	}}}
}

// streamsFrom opens each phone's a2dp pcm that runs and ends each stream whose pcm stopped, went or is not wanted
func (b *Bluetooth) streamsFrom(ctx context.Context, be Backend, snap Snapshot) {
	b.mu.Lock()
	on := b.cfg.Enabled && b.cfg.Receive && b.send != nil && snap.Adapter != nil
	conn := b.conn
	want := map[string]PCM{}
	for _, p := range snap.PCMs {
		if _, known := deviceAt(snap, p.Device); on && known && p.Transport == TransportA2DPSink && p.Running {
			want[p.Path] = p
		}
	}
	for path, st := range b.streams {
		p, ok := want[path]
		switch {
		case !ok:
			b.endLocked(path, st, endReason(snap, path, on))
		case p.Rate != st.rate || p.Channels != st.channels:
			// a codec change reopens the pcm
			b.endLocked(path, st, "format")
		}
	}
	var open []PCM
	for path, p := range want {
		if _, ok := b.streams[path]; !ok {
			open = append(open, p)
		}
	}
	for path := range b.failed {
		if _, ok := want[path]; !ok {
			delete(b.failed, path)
		}
	}
	b.mu.Unlock()
	slices.SortFunc(open, func(x, y PCM) int { return strings.Compare(x.Path, y.Path) })
	for _, p := range open {
		b.open(ctx, be, snap, p, conn)
	}
}

func endReason(snap Snapshot, path string, on bool) string {
	switch {
	case !on:
		return "off"
	case slices.ContainsFunc(snap.PCMs, func(p PCM) bool { return p.Path == path }):
		return "idle"
	default:
		return "gone"
	}
}

func (b *Bluetooth) open(ctx context.Context, be Backend, snap Snapshot, p PCM, conn int) {
	d, _ := deviceAt(snap, p.Device)
	id := ShortID(snap.Adapter.Address, d.Address)
	fail := func(code string) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.failed[p.Path] {
			b.failed[p.Path] = true
			b.log.Warn("bluetooth_stream_failed", "id", id, "code", code)
		}
	}
	// only our level scales the audio
	if p.SoftVolume && be.SetSoftVolume(ctx, p.Path, false) != nil {
		fail("soft_volume")
		return
	}
	data, ctrl, err := be.Open(ctx, p.Path)
	if err != nil {
		fail("open")
		return
	}
	defer func() { _ = data.Close() }()
	env := &audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothStream{BluetoothStream: &audiodv1.BluetoothStream{
		StreamId: id, Rate: p.Rate, Channels: uint32(p.Channels), Sender: d.Name,
	}}}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	sendFD := b.sendFD
	if sendFD == nil || b.conn != conn {
		b.mu.Unlock()
		_ = ctrl.Close()
		return
	}
	b.mu.Unlock()
	if err := sendFD(env, int(data.Fd())); err != nil {
		_ = ctrl.Close()
		fail("send")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != conn {
		// audiod went while the frame went out: it closed its copy
		_ = ctrl.Close()
		return
	}
	delete(b.failed, p.Path)
	b.streams[p.Path] = &stream{id: id, device: p.Device, rate: p.Rate, channels: p.Channels, conn: conn, ctrl: ctrl}
	b.log.Info("bluetooth_stream_open", "id", id, "rate", p.Rate, "channels", p.Channels)
}

// endLocked tells audiod the stream ended, when it is still the stream's connection, and lets the pcm go; callers
// hold mu
func (b *Bluetooth) endLocked(path string, st *stream, reason string) {
	delete(b.streams, path)
	if b.send != nil && b.conn == st.conn {
		_ = b.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothStreamEnd{BluetoothStreamEnd: &audiodv1.BluetoothStreamEnd{
			StreamId: st.id,
		}}})
	}
	_ = st.ctrl.Close()
	b.log.Info("bluetooth_stream_end", "id", st.id, "reason", reason)
}

var playerMethods = map[audiodv1.MediaAction]string{
	audiodv1.MediaAction_MEDIA_ACTION_PAUSE: "Pause", audiodv1.MediaAction_MEDIA_ACTION_RESUME: "Play",
	audiodv1.MediaAction_MEDIA_ACTION_STOP: "Stop", audiodv1.MediaAction_MEDIA_ACTION_NEXT: "Next",
	audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS: "Previous",
}

// control runs audiod's transport action on the stream's phone player
func (b *Bluetooth) control(ctx context.Context, be Backend, c *audiodv1.BluetoothControl) {
	method, ok := playerMethods[c.GetAction()]
	if !ok {
		return
	}
	b.mu.Lock()
	var device string
	for _, st := range b.streams {
		if st.id == c.GetStreamId() {
			device = st.device
		}
	}
	i := slices.IndexFunc(b.snap.Players, func(p Player) bool { return device != "" && p.Device == device })
	var player string
	if i >= 0 {
		player = b.snap.Players[i].Path
	}
	b.mu.Unlock()
	if player == "" {
		b.log.Info("bluetooth_control_failed", "id", c.GetStreamId(), "code", "no_player")
		return
	}
	if err := be.Control(ctx, player, method); err != nil {
		b.log.Warn("bluetooth_control_failed", "id", c.GetStreamId(), "code", "player")
		return
	}
	b.log.Info("bluetooth_control", "id", c.GetStreamId(), "action", method)
}
