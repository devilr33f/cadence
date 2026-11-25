package sim

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opusframes"
)

// .opusframes for the sim's own mic, .wav for audiod's file capture
//
//go:embed fixtures/*.opusframes fixtures/*.wav
var fixtures embed.FS

const (
	pollEvery   = 200 * time.Millisecond
	packetEvery = 20 * time.Millisecond
	triggerBuf  = 4
)

var (
	fixtureName   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	errNoFixture  = errors.New("sim: unknown fixture")
	errBadStream  = errors.New("sim: unsafe stream id")
	streamIDChars = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// simAudio is the sim's microphone (an embedded fixture), speaker (files) and
// trigger (the say file).
type simAudio struct {
	dir      string
	log      *slog.Logger
	triggers chan audio.Trigger
	// the fixture file a say name needs
	fixtureExt string

	mu sync.Mutex
	// the open follow-up's microphone waits here for a say file, nil while none does
	waiting chan string
	// the last follow-up microphone that closed on a failed capture
	lost <-chan []byte
}

func newSimAudio(dataDir string, log *slog.Logger, viaAudiod bool) *simAudio {
	ext := ".opusframes"
	if viaAudiod {
		ext = ".wav"
	}
	return &simAudio{dir: filepath.Join(dataDir, "sim"), log: log, triggers: make(chan audio.Trigger, triggerBuf), fixtureExt: ext}
}

// pollEach runs f every pollEvery until ctx ends
func pollEach(ctx context.Context, f func()) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}

func (a *simAudio) poll(ctx context.Context) { pollEach(ctx, a.readSay) }

// readSay takes the fixture name from the say file and removes the file. An
// empty file may be half written, so it stays for the next poll. The name
// speaks into the follow-up that waits for one, else it begins a session.
func (a *simAudio) readSay() {
	path := filepath.Join(a.dir, "say")
	raw, err := os.ReadFile(path) //nolint:gosec // under the operator's data dir
	if err != nil {
		return
	}
	name := strings.TrimSpace(string(raw))
	if name == "" {
		return
	}
	if err := os.Remove(path); err != nil {
		return
	}
	if !fixtureName.MatchString(name) {
		a.log.Warn("sim_fixture_unknown")
		return
	}
	if _, err := fixtures.ReadFile("fixtures/" + name + a.fixtureExt); err != nil {
		a.log.Warn("sim_fixture_unknown")
		return
	}
	if a.feedFollowUp(name) {
		return
	}
	select {
	case a.triggers <- audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Fixture: name, Test: true}:
	default:
		a.log.Warn("sim_trigger_dropped")
	}
}

// feedFollowUp gives name to the follow-up whose microphone waits for an utterance, if one does
func (a *simAudio) feedFollowUp(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waiting == nil {
		return false
	}
	// it has room for one, and it is never fed twice
	a.waiting <- name
	a.waiting = nil
	return true
}

// waitsForSay: a follow-up opens the microphone with no utterance of its own, which a say file gives it on the sim
func waitsForSay(t audio.Trigger) bool {
	return t.Kind == cadencev2.Trigger_TRIGGER_FOLLOW_UP && t.Fixture == ""
}

// followUp is a follow-up's microphone (M6a-R4): silent until a say file names an utterance, which open then plays
// into it instead of beginning a session. It closes when ctx ends or the utterance does; failed, when set, tells
// whether the utterance's packets closed on a failed capture.
func (a *simAudio) followUp(ctx context.Context, open func(ctx context.Context, fixture string) (<-chan []byte, error), failed func(<-chan []byte) bool) <-chan []byte {
	names := make(chan string, 1)
	a.mu.Lock()
	a.waiting = names
	a.mu.Unlock()
	out := make(chan []byte)
	go func() {
		defer close(out)
		var name string
		select {
		case <-ctx.Done():
			a.mu.Lock()
			if a.waiting == names {
				a.waiting = nil
			}
			a.mu.Unlock()
			return
		case name = <-names:
		}
		in, err := open(ctx, name)
		if err != nil {
			a.markLost(out)
			return
		}
		for p := range in {
			select {
			case out <- p:
			case <-ctx.Done():
				return
			}
		}
		if failed != nil && failed(in) {
			a.markLost(out)
		}
	}()
	return out
}

func (a *simAudio) markLost(packets <-chan []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lost = packets
}

// MicFailure tells a follow-up's microphone whose utterance failed to play from one that ended normally.
func (a *simAudio) MicFailure(packets <-chan []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lost != nil && a.lost == packets
}

// Open replays the trigger's fixture, or for a follow-up the one a say file names.
func (a *simAudio) Open(ctx context.Context, t audio.Trigger) (<-chan []byte, error) {
	if waitsForSay(t) {
		return a.followUp(ctx, a.replay, nil), nil
	}
	return a.replay(ctx, t.Fixture)
}

// replay plays the fixture, one packet per 20 ms, the first at once.
func (a *simAudio) replay(ctx context.Context, fixture string) (<-chan []byte, error) {
	if !fixtureName.MatchString(fixture) {
		return nil, errNoFixture
	}
	f, err := fixtures.Open("fixtures/" + fixture + ".opusframes")
	if err != nil {
		return nil, errNoFixture
	}
	defer func() { _ = f.Close() }()
	packets, err := opusframes.Read(f)
	if err != nil {
		return nil, err
	}
	out := make(chan []byte)
	go func() {
		defer close(out)
		tick := time.NewTicker(packetEvery)
		defer tick.Stop()
		for i, p := range packets {
			if i > 0 {
				select {
				case <-tick.C:
				case <-ctx.Done():
					return
				}
			}
			select {
			case out <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
