package soloist

import (
	"context"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

// a timer that never needs to fire still wakes the loop this often
const idleWake = time.Hour

// finish is how a job ended, told to the schedule at the time the loop takes it
type finish func(j *updater.Jobs, now time.Time) updater.Job

// runUpdates schedules the updater while soloist is enabled with a key
// (reference §6.7): each job on its own goroutine, one at a time
func (s *Soloist) runUpdates(ctx context.Context) {
	var (
		jobs   updater.Jobs
		active bool
		wg     sync.WaitGroup
		// one job at a time: a result never waits for room
		done = make(chan finish, 1)
	)
	defer wg.Wait()
	run := func(job updater.Job) {
		if job != updater.NoJob {
			wg.Go(func() { done <- s.job(ctx, job) })
		}
	}
	timer := time.NewTimer(idleWake)
	defer timer.Stop()
	for {
		now := time.Now()
		ev := s.updateEvents()
		if ev.wanted != active {
			if active = ev.wanted; active {
				jobs.Start(now)
			} else {
				jobs.Stop()
			}
		}
		if ev.expired {
			run(jobs.Trigger(updater.Expired))
		}
		if ev.noBuild {
			run(jobs.Trigger(updater.NoBuild))
		}
		if ev.silentSeen {
			run(jobs.Silent(ev.silentMin))
			run(jobs.Silent(ev.silentLast))
		}
		run(jobs.Due(now))
		wait := idleWake
		if at, ok := jobs.Wake(); ok {
			wait = max(at.Sub(now), 0)
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-s.updKick:
		case <-timer.C:
		case f := <-done:
			run(f(&jobs, time.Now()))
		}
	}
}

// events are what the updates loop has not seen yet
type events struct {
	wanted                bool
	noBuild, expired      bool
	silentSeen            bool
	silentMin, silentLast uint32
}

func (s *Soloist) updateEvents() events {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := events{
		wanted: s.cfg.Enabled && s.key != "", noBuild: s.noBuild, expired: s.expired,
		silentSeen: s.silentSeen, silentMin: s.silentMin, silentLast: s.status.GetSilentStarts(),
	}
	s.noBuild, s.expired, s.silentSeen = false, false, false
	return ev
}

func (s *Soloist) kickUpdates() {
	select {
	case s.updKick <- struct{}{}:
	default:
	}
}

// job runs one job and switches audiod onto what it left
func (s *Soloist) job(ctx context.Context, job updater.Job) finish {
	if job == updater.RollbackJob {
		r, b := s.upd.RollBack(ctx)
		if r == updater.Deferred {
			s.log.Warn("soloist_rollback_deferred")
			return func(j *updater.Jobs, _ time.Time) updater.Job { return j.RollbackDeferred() }
		}
		s.log.Warn("soloist_rollback", "restored", r == updater.Restored)
		s.installedNow(b)
		s.switchBuild("rollback")
		return func(j *updater.Jobs, now time.Time) updater.Job { return j.RolledBack(now) }
	}
	o := s.upd.Once(ctx)
	switch o.Result {
	case updater.Installed:
		s.log.Info("soloist_update", "result", "ok", "build", o.Installed.Date())
	case updater.Failed:
		s.log.Warn("soloist_update", "result", "failed", "reason", string(o.Failure))
	}
	if o.Known {
		s.installedNow(o.Installed)
	}
	var silent uint32
	if o.Result == updater.Installed {
		// the new build's silent starts count from here, as audiod starts them over at the switch
		s.mu.Lock()
		silent = s.status.GetSilentStarts()
		s.mu.Unlock()
		s.switchBuild("install")
	}
	return func(j *updater.Jobs, now time.Time) updater.Job { return j.Checked(o, now, silent) }
}

// installedNow keeps the installed build for the state's date and expiry
func (s *Soloist) installedNow(b *updater.Build) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installed, s.installedKnown = b, true
	s.publishLocked()
}
