package audiod

import (
	"context"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// supervisedIO hands each Open and Begin to the connection that is up; what it
// returns stays bound to that connection and fails once it is gone.
type supervisedIO struct {
	s *Supervisor

	mu sync.Mutex
	// the controller reads one mic at a time and asks about it as it closes
	lastMic   <-chan []byte
	lastMicIO *audioIO
}

func (v *supervisedIO) Open(ctx context.Context, t audio.Trigger) (<-chan []byte, error) {
	a := v.s.current()
	if a == nil {
		return nil, ErrUnavailable
	}
	ch, err := a.Open(ctx, t)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.lastMic, v.lastMicIO = ch, a
	v.mu.Unlock()
	return ch, nil
}

// MicFailure answers for the mic channel opened last.
func (v *supervisedIO) MicFailure(packets <-chan []byte) bool {
	v.mu.Lock()
	a := v.lastMicIO
	ok := v.lastMic != nil && v.lastMic == packets
	v.mu.Unlock()
	return ok && a.MicFailure(packets)
}

func (v *supervisedIO) Begin(st *cadencev2.StreamStart) (audio.StreamWriter, error) {
	a := v.s.current()
	if a == nil {
		return nil, ErrUnavailable
	}
	return a.Begin(st)
}

// Duck tells audiod whether a session is open, now and after every restart.
func (v *supervisedIO) Duck(active bool) { v.s.duck(active) }

// WakeEarcon has audiod play its wake earcon once; nothing while audiod is down.
func (v *supervisedIO) WakeEarcon() { v.s.tone(audiodv1.ToneKind_TONE_KIND_WAKE) }

// ErrorSound has audiod play the spoken prompt id in lang, or its error earcon for "" or a prompt it lacks; nothing
// while audiod is down.
func (v *supervisedIO) ErrorSound(id, lang string) { v.s.prompt(id, lang) }
