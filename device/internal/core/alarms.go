package core

import (
	"context"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// startRinger runs the board's ringer from boot, offline too, once the stored configs gave its book its zone, and
// shows the alarms on the lights.
func (s *Sync) startRinger(ctx context.Context, wg *sync.WaitGroup) {
	ab, ok := s.board.(board.Alarms)
	if !ok {
		return
	}
	r := ab.Alarms()
	if r == nil {
		return
	}
	s.ringer = r
	// a reminder's bundled prompt in the session config's language, the server's ring sounds through its url
	r.UseLanguage(s.errors.Language)
	if s.ringSounds != nil {
		r.UseSounds(s.ringSounds)
	}
	hooks := alarms.Hooks{EndSession: s.ringBegan}
	if ind := s.ind; ind != nil {
		hooks.Ringing = ind.Ringing
		wg.Go(func() { showAlarms(ctx, r.Book(), ind) })
	}
	wg.Go(func() { r.Run(ctx, hooks) })
}

// ringBegan ends the session of the link that is up, its reply and the announcement: a ring begins. Under mu, as
// dispatch, so one sender at a time enqueues
func (s *Sync) ringBegan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.triggers != nil {
		audio.Enqueue(s.triggers, audio.Trigger{Mode: audio.Ring}, s.log)
	}
}

// ringAdmits says whether t may go on while an alarm or a timer rings: a wake stops the ring and begins its session,
// which names the ring it stopped (so the server hears «ещё пять минут» as a snooze), unless another ring starts in
// its place; any other activation is refused, test audio included. The switch goes on.
func (s *Sync) ringAdmits(t audio.Trigger) (audio.Trigger, bool) {
	if s.ringer == nil || t.Ends() || !s.ringer.Ringing() {
		return t, true
	}
	source := "button"
	switch {
	case t.Test:
		source = "inject"
	case t.Kind == cadencev2.Trigger_TRIGGER_WAKE:
		if !s.ringer.Stop(alarms.StopWake) {
			t.AfterRing = afterRing(s.ringer)
			return t, true
		}
		source = "wake"
	}
	s.log.Info("activation_refused", "reason", "ringing", "source", source)
	return t, false
}

// afterRing is the ring a wake just stopped, as the session names it
func afterRing(r *alarms.Ringer) *cadencev2.AfterRing {
	last, ago, ok := r.LastStopped()
	if !ok || last.StoppedBy != alarms.StopWake {
		return nil
	}
	ago = min(max(ago, 0), time.Minute)
	ms := uint32(ago.Milliseconds()) //nolint:gosec // 0-60000, clamped above
	return &cadencev2.AfterRing{Kind: string(last.Kind), Id: last.ID, StoppedMsAgo: ms}
}

// showAlarms keeps the lights' dot, countdown and clock zone on what the book holds until ctx ends
func showAlarms(ctx context.Context, book *alarms.Book, ind *indicator) {
	changes := book.Changes()
	for {
		end, _ := book.NearestTimerEnd()
		ind.Alarms(book.Armed(), end)
		if loc, ok := book.Zone(); ok {
			ind.Zone(loc)
		}
		select {
		case <-ctx.Done():
			return
		case <-changes:
		}
	}
}
