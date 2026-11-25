package rpi

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// the screen is optional: the pi plays as a speaker with nothing on its hdmi ports, and its screen runs, and its
// screen capabilities are available, only while a sink is plugged in. The drm connectors' status in sysfs is polled
// rather than udev's drm change uevents followed: a uevent only says that something on the card changed (vc4 sends
// HOTPLUG=1 on card1, not the connector), so the status files are read either way, and two small reads every 2 s
// need no netlink socket, no event parsing and no resync after a missed event.
const (
	hdmiEvery = 2 * time.Second
	// a status must hold this long before it counts: a flap (a tv's input switch, a loose plug) moves nothing
	hdmiSettle = 1500 * time.Millisecond
	// the systemd unit that runs cadence-screen; CADENCE_RPI_SCREEN_UNIT names another, "none" leaves the screen to
	// whoever runs it
	screenUnit = "cadence-screen.service"
)

// gated are the capabilities whose hardware is the hdmi sink: the screen plane's and the tv's cec. The receivers
// (media.screencast's airplay audio, bluetooth), the output and the input stay available: a speaker needs no screen.
var gated = []string{
	capschema.DisplayScreenID, capschema.MediaVideoID, capschema.MediaMpvID, capschema.InputRemoteID,
	capschema.DisplayCecID,
}

// hdmiStatus reads every hdmi connector's status under root: connected when any one is, known false when the card
// has no hdmi connector at all (no kms driver loaded: the screen is then left as it was, always on)
func hdmiStatus(root string) (connected, known bool) {
	paths, _ := filepath.Glob(under(root, "/sys/class/drm/card*-HDMI-A-*/status"))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		known = true
		if strings.TrimSpace(string(b)) == "connected" {
			return true, true
		}
	}
	return false, known
}

// hotplug debounces the polled status: the first read counts at once (the boot's reconcile), every later change
// only once it held for settle.
type hotplug struct {
	settle  time.Duration
	read    func() (connected, known bool)
	started bool
	applied bool
	pending bool
	since   time.Time
	waiting bool
}

// step takes one poll at now and returns the state to apply, ok false while nothing changes
func (h *hotplug) step(now time.Time) (connected, ok bool) {
	c, known := h.read()
	if !known {
		c = true
	}
	if !h.started {
		h.started, h.applied = true, c
		return c, true
	}
	if c == h.applied {
		h.waiting = false
		return false, false
	}
	if !h.waiting || h.pending != c {
		h.waiting, h.pending, h.since = true, c, now
	}
	if now.Sub(h.since) < h.settle {
		return false, false
	}
	h.waiting, h.applied = false, c
	return c, true
}

// screenControl starts and stops whatever runs cadence-screen
type screenControl interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// systemdUnit is cadence-screen's unit, started and stopped through systemctl; reset-failed first, so a start
// limit an earlier sink hit never keeps a new one dark
type systemdUnit struct {
	name string
	run  func(ctx context.Context, args ...string) error
}

func newSystemdUnit(name string) *systemdUnit {
	return &systemdUnit{name: name, run: func(ctx context.Context, args ...string) error {
		return exec.CommandContext(ctx, "systemctl", args...).Run()
	}}
}

func (u *systemdUnit) Start(ctx context.Context) error {
	_ = u.run(ctx, "reset-failed", u.name)
	return u.run(ctx, "start", "--no-block", u.name)
}

func (u *systemdUnit) Stop(ctx context.Context) error {
	return u.run(ctx, "stop", "--no-block", u.name)
}

// noScreenControl leaves the screen to whoever runs it; availability still follows the sink
type noScreenControl struct{}

func (noScreenControl) Start(context.Context) error { return nil }
func (noScreenControl) Stop(context.Context) error  { return nil }

// hdmi follows the sink: the gated capabilities' availability, the screen's unit, and a cec rescan once a sink comes
type hdmi struct {
	h       hotplug
	every   time.Duration
	control screenControl
	rescan  func(ctx context.Context)
	log     *slog.Logger
	set     func(available bool, ids ...string) bool
	// runs the rescan beside the poll
	wg *sync.WaitGroup
}

// apply puts the device in step with the sink
func (w *hdmi) apply(ctx context.Context, connected bool) {
	w.set(connected, gated...)
	w.log.Info("hdmi_sink", "connected", connected)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if connected {
		if err := w.control.Start(cctx); err != nil {
			w.log.Warn("screen_start_failed", "code", "systemctl")
		}
		if w.rescan != nil && w.wg != nil {
			w.wg.Go(func() { w.rescan(ctx) })
		}
		return
	}
	if err := w.control.Stop(cctx); err != nil {
		w.log.Warn("screen_stop_failed", "code", "systemctl")
	}
}

// first reads and applies the sink at once: the availability is in force before the first link
func (w *hdmi) first(ctx context.Context) {
	if c, ok := w.h.step(time.Now()); ok {
		w.apply(ctx, c)
	}
}

// run polls until ctx ends
func (w *hdmi) run(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick:
			if c, ok := w.h.step(now); ok {
				w.apply(ctx, c)
			}
		}
	}
}

// screenHotplug is the board's hdmi follower, started once with the board
type screenHotplug struct {
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (b *Board) setupHDMI() {
	b.hp.once.Do(func() {
		b.hp.ctx, b.hp.cancel = context.WithCancel(context.Background())
		var control screenControl = noScreenControl{}
		if b.screenUnit != "" {
			control = b.o.screenControl
			if control == nil {
				control = newSystemdUnit(b.screenUnit)
			}
		}
		w := &hdmi{
			h:       hotplug{settle: hdmiSettle, read: func() (bool, bool) { return hdmiStatus(b.o.root) }},
			every:   hdmiEvery,
			control: control,
			log:     b.log,
			set:     b.avail.Set,
			wg:      &b.hp.wg,
		}
		if b.cec != nil {
			w.rescan = b.cec.Scan
		}
		if b.o.hdmiEvery > 0 {
			w.every = b.o.hdmiEvery
		}
		if b.o.hdmiSettle > 0 {
			w.h.settle = b.o.hdmiSettle
		}
		w.first(b.hp.ctx)
		t := time.NewTicker(w.every)
		b.hp.wg.Go(func() {
			defer t.Stop()
			w.run(b.hp.ctx, t.C)
		})
	})
}

func (b *Board) closeHDMI() {
	b.hp.once.Do(func() {})
	if b.hp.cancel != nil {
		b.hp.cancel()
	}
	b.hp.wg.Wait()
}
