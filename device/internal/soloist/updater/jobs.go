package updater

import (
	"math"
	"time"
)

const (
	firstCheck = time.Minute
	// silent starts in a row of a build this run installed that roll it back
	silentStarts = 3
)

// Job is what Jobs wants run: one at a time, as both touch the build files.
type Job int

const (
	NoJob Job = iota
	CheckJob
	RollbackJob
)

// Cause is why soloist wants a build at once.
type Cause int

const (
	NoBuild Cause = iota
	Expired
)

// Jobs is the updater's schedule (reference §6.7), driven with the time by
// its owner, which runs each job it returns and reports how it ended: the
// first check a minute after Start, then as Schedule says; at once for no
// build or an expired one; after an install, the third start in a row without
// an answer rolls the build back, after which a missing build waits for the
// check a day on.
type Jobs struct {
	active bool
	next   time.Time // the next check, zero for none
	busy   bool
	// asked for while busy: a rollback goes first
	checkQueued, rollbackQueued bool
	// a rollback that could not probe the build before runs again at audiod's next status
	retryRollback bool
	// after a rollback no missing build checks before the next check
	quiet bool
	// the silent starts after an install count from base: audiod's count at
	// the switch, or lower when it started over before the check's end (low,
	// the lowest seen since the check began)
	armed bool
	base  uint32
	low   uint32
}

// Start begins the schedule: soloist is enabled with a key.
func (j *Jobs) Start(now time.Time) {
	j.active, j.next = true, now.Add(firstCheck)
}

// Stop ends it until the next Start; a job running ends as it does.
func (j *Jobs) Stop() {
	*j = Jobs{busy: j.busy}
}

// Wake is when Due wants to be called next.
func (j *Jobs) Wake() (time.Time, bool) {
	if !j.active || j.busy || j.next.IsZero() {
		return time.Time{}, false
	}
	return j.next, true
}

// Due starts the check that came due by now.
func (j *Jobs) Due(now time.Time) Job {
	if !j.active || j.busy || j.next.IsZero() || now.Before(j.next) {
		return NoJob
	}
	return j.check()
}

// Trigger asks for a check at once: soloist has no build, or an expired one.
// A build a rollback deleted is missing on purpose until the next check.
func (j *Jobs) Trigger(c Cause) Job {
	if !j.active || j.quiet && c == NoBuild {
		return NoJob
	}
	return j.check()
}

// Silent takes audiod's count of starts in a row without an answer, which
// starts over at an answer, a switch and an audiod restart. Any status means
// audiod is there again, for a rollback that waits for it.
func (j *Jobs) Silent(n uint32) Job {
	if !j.active {
		return NoJob
	}
	j.low = min(j.low, n)
	if j.retryRollback {
		j.retryRollback = false
		return j.rollback()
	}
	if !j.armed {
		return NoJob
	}
	j.base = min(j.base, n)
	if n-j.base < silentStarts {
		return NoJob
	}
	j.armed = false
	return j.rollback()
}

// Checked: a check ended at now; after an install the switch went out with
// audiod's silent starts at silent.
func (j *Jobs) Checked(o Outcome, now time.Time, silent uint32) Job {
	j.busy = false
	if !j.active {
		return NoJob
	}
	j.next = now.Add(Schedule(o))
	if o.Result == Installed {
		// a rollback still waiting would now restore the build that never answered
		j.rollbackQueued, j.retryRollback = false, false
		j.armed, j.base = true, min(silent, j.low)
	}
	return j.queued()
}

// RolledBack: a rollback ended at now.
func (j *Jobs) RolledBack(now time.Time) Job {
	j.busy = false
	if !j.active {
		return NoJob
	}
	j.checkQueued, j.quiet = false, true
	j.next = now.Add(daily)
	return j.queued()
}

// RollbackDeferred: a rollback could not probe the build before, with audiod
// gone; it runs again at audiod's next status.
func (j *Jobs) RollbackDeferred() Job {
	j.busy = false
	if !j.active {
		return NoJob
	}
	j.retryRollback = true
	return NoJob
}

func (j *Jobs) queued() Job {
	if j.rollbackQueued {
		j.rollbackQueued, j.busy = false, true
		return RollbackJob
	}
	if j.checkQueued {
		j.checkQueued = false
		return j.check()
	}
	return NoJob
}

func (j *Jobs) rollback() Job {
	if j.busy {
		j.rollbackQueued = true
		return NoJob
	}
	j.busy = true
	return RollbackJob
}

func (j *Jobs) check() Job {
	j.next = time.Time{}
	if j.busy {
		j.checkQueued = true
		return NoJob
	}
	j.busy, j.quiet, j.low = true, false, math.MaxUint32
	return CheckJob
}
