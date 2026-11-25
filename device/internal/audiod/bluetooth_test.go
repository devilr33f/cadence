package audiod_test

import (
	"os"
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// btHandler sends its config and one stream with a pipe's fd after each Ready, and hands back the controls it gets
type btHandler struct {
	t        *testing.T
	controls chan *audiodv1.BluetoothControl
	gone     chan struct{}
}

func (h *btHandler) BluetoothReady(send func(*audiodv1.Envelope) error, sendFD func(*audiodv1.Envelope, int) error) {
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothConfig{BluetoothConfig: &audiodv1.BluetoothConfig{Enabled: true}}})
	r, w, err := os.Pipe()
	if err != nil {
		h.t.Error(err)
		return
	}
	defer func() { _, _ = r.Close(), w.Close() }()
	st := &audiodv1.BluetoothStream{StreamId: "a1b2c3d4e5f6", Rate: 44100, Channels: 2}
	if err := sendFD(&audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothStream{BluetoothStream: st}}, int(r.Fd())); err != nil {
		h.t.Error(err)
	}
}

func (h *btHandler) BluetoothEnvelope(env *audiodv1.Envelope) {
	if c := env.GetBluetoothControl(); c != nil {
		h.controls <- c
	}
}

func (h *btHandler) BluetoothGone() { h.gone <- struct{}{} }

// bluetooth's config and stream follow every Ready, and audiod's controls reach the handler
func TestBluetoothFollowsReady(t *testing.T) {
	h := &btHandler{t: t, controls: make(chan *audiodv1.BluetoothControl, 4), gone: make(chan struct{}, 4)}
	sup := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithBluetooth(h)}, "--bluetooth-control")
	select {
	case c := <-h.controls:
		if c.GetStreamId() != "a1b2c3d4e5f6" || c.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_PAUSE {
			t.Fatalf("control %v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no control; records %v", audiodtest.Records(sup.dir))
	}
	got := afterConfigure(audiodtest.Records(sup.dir), 1)
	if !slices.Contains(got, "bluetooth_config enabled=true") || !slices.Contains(got, "bluetooth_stream id=a1b2c3d4e5f6 rate=44100 channels=2") {
		t.Fatalf("records %q", got)
	}
	sup.stop()
	select {
	case <-h.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("no BluetoothGone")
	}
}
