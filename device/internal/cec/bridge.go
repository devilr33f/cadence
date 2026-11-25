package cec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// how often the bridge rereads the framework's own state, and how long one call may take
const (
	bridgePoll = 30 * time.Second
	bridgeCall = 3 * time.Second
)

// bridgeInfo is the app's answer about the framework's cec: present when the framework has cec on with a
// sink; fixedVolume when the stream volume is the tv's (AudioService then forwards adjusts as cec volume
// keys); shell when its adb pairing can run the keys that move the tv's power; tvPower from its own status.
// ok is an op's own answer.
type bridgeInfo struct {
	Present     bool   `json:"present"`
	FixedVolume bool   `json:"fixedVolume"`
	Shell       bool   `json:"shell"`
	TvPower     string `json:"tvPower"`
	SystemAudio string `json:"systemAudio"`
	OK          bool   `json:"ok"`
	Code        string `json:"code"`
}

// Bridge is the android app's cec, behind the same Bus seam as the pi's /dev/cec: no raw frames exist there
// (the framework owns the wire and keeps its api from an app), so this bus answers the scan with what the
// framework knows and translates the flows' frames into what it can do — volume keys through AudioService's
// public cec forward, the tv's power through the shell keys the app's adb pairing runs (HOME's one touch
// play, SLEEP's standby), everything else as the wire's own Feature Abort, which is what it would say.
type Bridge struct {
	path string
	log  *slog.Logger

	mu  sync.Mutex
	set bool
	ins bridgeInfo
}

// NewBridge is the bus at the app's socket (an abstract name, "@cadence-cec"); it answers absent until the
// app is heard from. A nil log discards.
func NewBridge(path string, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Bridge{path: path, log: log}
}

// Info is the adapter as the app last described it.
func (b *Bridge) Info() Adapter {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Adapter{Present: b.set && b.ins.Present, DeviceType: "playback", LogicalAddress: 4}
}

// Subscribe keeps the seam whole; the bridge reports nothing as frames.
func (b *Bridge) Subscribe(func(Frame)) {}

// Start asks the app once and follows its state until ctx ends (also the retry while the app is not yet up).
func (b *Bridge) Start(ctx context.Context, wg *sync.WaitGroup) {
	b.poll()
	wg.Go(func() {
		t := time.NewTicker(bridgePoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.poll()
			}
		}
	})
}

func (b *Bridge) poll() {
	info, err := b.call("info", nil)
	b.mu.Lock()
	was := b.set && b.ins.Present
	b.set = err == nil
	if err == nil {
		b.ins = info
	}
	now := b.set && b.ins.Present
	b.mu.Unlock()
	if was && !now {
		b.log.Info("cec_bridge_absent")
	}
}

// call is one request to the app: dial, one line in, one line out
func (b *Bridge) call(op string, args map[string]any) (bridgeInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bridgeCall)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", b.path)
	if err != nil {
		return bridgeInfo{}, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(bridgeCall))
	req := map[string]any{"op": op}
	for k, v := range args {
		req[k] = v
	}
	line, err := json.Marshal(req)
	if err != nil {
		return bridgeInfo{}, err
	}
	if _, err = c.Write(append(line, '\n')); err != nil {
		return bridgeInfo{}, err
	}
	r := bufio.NewReader(io.LimitReader(c, 1<<16))
	raw, err := r.ReadBytes('\n')
	if err != nil && (!errors.Is(err, io.EOF) || len(raw) == 0) {
		return bridgeInfo{}, err
	}
	var info bridgeInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return bridgeInfo{}, fmt.Errorf("cec bridge: %w", err)
	}
	return info, nil
}

// screen is the one op with arguments (on or off); its ok is the pairing's word
func (b *Bridge) screen(on bool) bool {
	answer, err := b.call("screen", map[string]any{"on": on})
	return err == nil && answer.OK
}

// volumeKey is one press through AudioService's forward
func (b *Bridge) volumeKey(action string) bool {
	answer, err := b.call("volume", map[string]any{"action": action})
	return err == nil && answer.OK
}

// Send translates the flows' frames; everything the app cannot do answers as the wire's Feature Abort of it.
func (b *Bridge) Send(ctx context.Context, msg Frame, _ byte) (Frame, bool) {
	if err := ctx.Err(); err != nil {
		return Frame{}, false
	}
	b.mu.Lock()
	set, ins := b.set, b.ins
	b.mu.Unlock()
	if !set || !ins.Present {
		return Frame{}, false
	}
	self := byte(4)
	if msg.Polling {
		return Frame{}, msg.Dest == 0
	}
	switch msg.Opcode {
	case OpGivePhysicalAddress:
		if msg.Dest != 0 {
			return abortOf(self, msg.Dest, OpGivePhysicalAddress), true
		}
		// the tv is the wire's root, always 0.0.0.0
		return Frame{Init: 0, Dest: self, Opcode: OpReportPhysicalAddress, Ops: []byte{0, 0, typeTV}}, true
	case OpGiveDeviceVendorID, OpGiveOSDName, OpGiveAudioStatus, OpGiveSystemAudioModeStatus:
		return abortOf(self, msg.Dest, msg.Opcode), true
	case OpGiveDevicePowerStatus:
		if powerOperandOK(ins.TvPower) {
			return Frame{Init: 0, Dest: self, Opcode: OpReportPowerStatus, Ops: []byte{powerOperand(ins.TvPower)}}, true
		}
		return abortOf(self, msg.Dest, OpGiveDevicePowerStatus), true
	case OpTextViewOn, OpImageViewOn:
		if msg.Dest != 0 {
			return Frame{}, false
		}
		return Frame{}, b.screen(true)
	case OpActiveSource:
		// the framework's one touch play is the home key: it wakes and takes the source in one
		return Frame{}, b.screen(true)
	case OpStandby:
		if msg.Dest != 0 && msg.Dest != 0xf {
			return Frame{}, false
		}
		return Frame{}, b.screen(false)
	case OpUserControlPressed:
		if msg.Dest != 0 || len(msg.Ops) < 1 {
			return Frame{}, false
		}
		switch msg.Ops[0] {
		case UCVolumeUp:
			return Frame{}, b.volumeKey("raise")
		case UCVolumeDown:
			return Frame{}, b.volumeKey("lower")
		case UCMute:
			return Frame{}, b.volumeKey("mute")
		}
		return abortOf(self, msg.Dest, OpUserControlPressed), true
	case OpUserControlReleased:
		return Frame{}, true
	}
	return Frame{}, false
}

// abortOf is the wire's Feature Abort of op, as the app answers what it cannot do
func abortOf(self, dest, op byte) Frame {
	return Frame{Init: dest, Dest: self, Opcode: OpFeatureAbort, Ops: []byte{op, AbortNotInMode}}
}

// powerOperandOK says whether the app knew the tv's power
func powerOperandOK(power string) bool {
	return power == "on" || power == "standby" || power == "to_on" || power == "to_off"
}

var _ Bus = (*Bridge)(nil)
