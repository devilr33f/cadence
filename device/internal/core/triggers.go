package core

import (
	"context"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/session"
)

// a link's controller takes its triggers from a queue this long
const linkTriggers = 4

// StartAudio runs the board's input and audio from boot, before any link: the buttons, the mute switch and the volume
// work offline, alarms and timers ring offline, and a trigger with no link up is refused at once. The input's triggers
// are taken here beside the audio's. Call it once, after Restore and before the link runs; the func it returns waits,
// once ctx is done, for what it started.
func (s *Sync) StartAudio(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	s.startRinger(ctx, &wg)
	if ib, ok := s.board.(board.Inputs); ok {
		s.input = ib.Input()
	}
	s.startVoice(ctx, &wg, s.input)
	var fromInput <-chan audio.Trigger
	if in := s.input; in != nil {
		var lights input.Lights
		if s.ind != nil {
			lights = s.ind
		}
		fromInput = in.Triggers()
		// before audiod starts, so the switch's first position is known when it comes up
		wg.Go(func() { in.Run(ctx, lights) })
	}
	var fromAudio <-chan audio.Trigger
	if aio, ok := s.board.AudioIO(); ok {
		s.aio, s.hasAudio, fromAudio = aio, true, aio.Triggers
		s.errors.UseSpeaker(aio.Speaker)
	}
	if fromInput != nil || fromAudio != nil {
		wg.Go(func() { s.route(ctx, fromInput, fromAudio) })
	}
	return wg.Wait
}

// route hands each trigger to the controller of the link that is up, until ctx ends. It and a ring that begins send on
// the link's queue, one at a time under mu.
func (s *Sync) route(ctx context.Context, fromInput, fromAudio <-chan audio.Trigger) {
	for fromInput != nil || fromAudio != nil {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-fromInput:
			if !ok {
				fromInput = nil
				continue
			}
			s.dispatch(t)
		case t, ok := <-fromAudio:
			if !ok {
				fromAudio = nil
				continue
			}
			s.dispatch(t)
		}
	}
}

// dispatch refuses an activation while no link is up, so a press while offline never begins a session later, and
// while a ring sounds, which a wake stops first; the switch reaches a link's controller whatever is queued there.
func (s *Sync) dispatch(t audio.Trigger) {
	t, ok := s.ringAdmits(t)
	if !ok {
		return
	}
	s.mu.Lock()
	if out := s.triggers; out != nil {
		audio.Enqueue(out, t, s.log)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// the switch has no session to end
	if t.Mode == audio.Mute {
		return
	}
	s.log.Info("activation_refused", "reason", "not_connected", "trigger", t.Kind.String())
	// test audio says nothing; ringAdmits let no activation through during a ring
	if t.Test {
		if s.ind != nil {
			s.ind.Offline()
		}
		return
	}
	s.errors.Play(session.PromptOffline)
}
