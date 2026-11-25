// Package input acts on a board's buttons and mute switch and on the wake engine audiod runs: it begins and cancels
// sessions through triggers, steps the volume, and keeps audiod's microphone muted while the switch is on (v1).
package input

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const (
	// VolumeStep is how far a press moves the level, and how far тише lowers it (v1)
	VolumeStep = 5
	triggerBuf = 4
	// LongPress is how long the action button is held, from the press that stopped a ring, to snooze that ring
	LongPress = time.Second
)

// Lights shows what the input does: core's indicator maps it onto the board's lights.
type Lights interface {
	Muted(muted bool)
	ShowVolume(level int)
}

// Ringer is the alarms' side (M6): while it rings, any press stops the ring instead of acting, and the action button
// held down for LongPress snoozes the ring it stopped.
type Ringer interface {
	Ringing() bool
	StopRing(by Kind)
	SnoozeStopped()
}

// Counts hears what the input saw, for the metrics: each wake audiod heard, and each command word acted on or ignored.
// It must never block.
type Counts interface {
	WakeHit()
	CommandWord(acted bool)
}

// CommandPolicy decides and acts on a command word audiod heard (M6a-D6), told whether the switch mutes the
// microphone: action names what it did, else reason says why it ignored the word. Both are tokens a log may carry; the
// phrase never is. It runs on audiod's goroutine and may wait briefly on the ring or volume lock; never on the network.
type CommandPolicy interface {
	Command(id uint32, muted bool) (action, reason string)
}

type Options struct {
	// nil: none
	Buttons Buttons
	// where the mute switch stands at boot: the board reads it before audio starts, so the first MuteState any audiod
	// gets is the real position
	Muted bool
	// nil: the volume buttons do nothing
	Volume *volume.Setter
	// nil: nothing ever rings
	Ringer Ringer
	// nil: nothing is counted
	Counts Counts
	// nil discards
	Log *slog.Logger
	// defaults to time.Now
	Now func() time.Time
}

// Input turns the board's buttons and audiod's wake and command events into triggers for the session, which core
// hands to the controller of the link that is up. It is audiod's InputHandler.
type Input struct {
	buttons  Buttons
	vol      *volume.Setter
	ringer   Ringer
	counts   Counts
	log      *slog.Logger
	now      func() time.Time
	triggers chan audio.Trigger
	state    chan json.RawMessage
	provider *provider

	mu    sync.Mutex
	muted bool
	// audiod's engines hear the wake word: false until its VendorStatus says so
	wake bool
	// when the capture last opened for the engines, or the switch let it hear again
	opened time.Time
	// audiod's connection, nil while it is down
	send func(*audiodv1.Envelope) error
	// what decides the command words, nil until core gives its own: every word is ignored
	commands CommandPolicy
	// the custom wake word (wake.custom), nil without one
	listener Listener
	// the command words acted on, and ignored
	acted, ignored uint64
}

func New(o Options) *Input {
	in := &Input{buttons: o.Buttons, vol: o.Volume, ringer: o.Ringer, counts: o.Counts, log: o.Log, now: o.Now,
		triggers: make(chan audio.Trigger, triggerBuf), state: make(chan json.RawMessage, 1), muted: o.Muted}
	if in.log == nil {
		in.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if in.now == nil {
		in.now = time.Now
	}
	in.provider = &provider{in: in}
	in.mu.Lock()
	in.publish()
	in.mu.Unlock()
	return in
}

// Triggers is what the session controller acts on; core takes them from the board's input beside its audio's.
func (in *Input) Triggers() <-chan audio.Trigger { return in.triggers }

// UseCommands has p decide every command word from now on: core's, which knows the ring and the sessions.
func (in *Input) UseCommands(p CommandPolicy) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.commands = p
}

// Listener is the custom wake word's side of audiod (wake.custom): ListenReady runs within InputReady, after the mute
// switch went out, with send bound to the new connection, and must never block; ListenGone within InputGone; MicPCM
// gets each MicPcm frame and must never block (it may drop).
type Listener interface {
	ListenReady(send func(*audiodv1.Envelope) error)
	ListenGone()
	MicPCM(frame *audiodv1.MicPcm)
}

// UseListener has l hear audiod's microphone; called before audio starts.
func (in *Input) UseListener(l Listener) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.listener = l
}

// CustomWake begins a session as a wake engine's hit does, from the custom wake word's detector: unless muted, and
// ignored while a session runs. atMs is the instant the word ended (the MicPcm frame that completed it), from which
// the capture's pre-roll starts.
func (in *Input) CustomWake(score float32, atMs int64) {
	if in.counts != nil {
		in.counts.WakeHit()
	}
	now := in.now()
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.muted {
		in.log.Info("wake_ignored", "reason", "muted", "engine", CustomEngine)
		return
	}
	at := time.UnixMilli(atMs)
	if atMs <= 0 || at.After(now) || now.Sub(at) > maxWakeAge {
		at, atMs = now, 0
	}
	in.emit(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle,
		Wake: &cadencev2.Wake{Engine: CustomEngine, Score: score}, At: at, PrerollFromMs: atMs})
}

// CustomEngine is the wake token of the custom wake word, as the server and the logs see it
const CustomEngine = "custom"

// Muted is whether the switch mutes the microphone; it never waits for audiod.
func (in *Input) Muted() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.muted
}

// Run acts on the buttons until ctx ends or they close; lights may be nil.
func (in *Input) Run(ctx context.Context, lights Lights) {
	if lights == nil {
		lights = noLights{}
	}
	in.mu.Lock()
	muted := in.muted
	in.mu.Unlock()
	if muted {
		lights.Muted(true)
	}
	if in.buttons == nil {
		return
	}
	events := in.buttons.Events()
	// when the action button went down on a ring, until it comes up; zero while it did not
	var stoppedAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			switch e.Kind {
			case MuteOn, MuteOff:
				in.setMuted(e.Kind == MuteOn, lights)
			case Action, VolumeUp, VolumeDown:
				switch {
				case e.Pressed:
					stopped := in.press(e.Kind, lights)
					if e.Kind == Action {
						stoppedAt = time.Time{}
						if stopped {
							stoppedAt = in.now()
						}
					}
				case e.Kind == Action && !stoppedAt.IsZero():
					// a board that reports no release never snoozes: its press only stops
					if in.now().Sub(stoppedAt) >= LongPress {
						in.ringer.SnoozeStopped()
					}
					stoppedAt = time.Time{}
				}
			}
		}
	}
}

// press acts on a key going down; true when it stopped a ring instead
func (in *Input) press(k Kind, lights Lights) bool {
	if in.ringer != nil && in.ringer.Ringing() {
		in.ringer.StopRing(k)
		return true
	}
	switch k {
	case Action:
		in.action()
	case VolumeUp:
		in.step(VolumeStep, lights)
	case VolumeDown:
		in.step(-VolumeStep, lights)
	}
	return false
}

// action begins a session, or cancels the one that runs; while muted it does neither (v1): audiod lets no
// microphone frame out
func (in *Input) action() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.muted {
		in.log.Info("activation_refused", "reason", "muted", "source", "button")
		return
	}
	in.emit(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle})
}

// step moves the level from the one in force; one the store cannot keep is refused, and the lights show the level
// in force either way
func (in *Input) step(delta int, lights Lights) {
	if in.vol == nil {
		return
	}
	level, _ := in.vol.Step(delta, volume.OriginButton)
	lights.ShowVolume(level)
}

func (in *Input) setMuted(muted bool, lights Lights) {
	in.mu.Lock()
	if in.muted == muted {
		in.mu.Unlock()
		return
	}
	in.muted = muted
	if !muted {
		// the engines warm up again, as after the capture reopens
		in.opened = in.now()
	}
	// audiod first, so no frame leaves once the switch is on, then the session
	in.sendMute()
	in.publish()
	if muted {
		in.emit(audio.Trigger{Mode: audio.Mute})
	}
	in.mu.Unlock()
	if muted {
		in.log.Info("microphone_muted")
	} else {
		in.log.Info("microphone_unmuted")
	}
	lights.Muted(muted)
}

// sendMute tells audiod where the switch stands; a send that fails is repeated after its next Ready. callers hold mu,
// so a Ready's resend never overtakes a change
func (in *Input) sendMute() {
	if in.send != nil {
		_ = in.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MuteState{MuteState: &audiodv1.MuteState{Muted: in.muted}}})
	}
}

// emit never blocks, and never drops the switch: core reads the triggers all the time. callers hold mu, so triggers
// keep the order of what made them and one sender at a time enqueues
func (in *Input) emit(t audio.Trigger) { audio.Enqueue(in.triggers, t, in.log) }

type noLights struct{}

func (noLights) Muted(bool)     {}
func (noLights) ShowVolume(int) {}
