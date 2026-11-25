package updater_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

var (
	t0        = time.Unix(1_793_100_000, 0)
	current   = updater.Outcome{Result: updater.Current}
	installed = updater.Outcome{Result: updater.Installed}
)

func wantJob(t *testing.T, what string, got, want updater.Job) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: job %v, want %v", what, got, want)
	}
}

func wantWake(t *testing.T, j *updater.Jobs, want time.Time) {
	t.Helper()
	at, ok := j.Wake()
	if want.IsZero() && ok || !want.IsZero() && (!ok || !at.Equal(want)) {
		t.Fatalf("wake %v %v, want %v", at, ok, want)
	}
}

func TestFirstCheckSixtySecondsAfterStart(t *testing.T) {
	var j updater.Jobs
	wantWake(t, &j, time.Time{})
	j.Start(t0)
	wantWake(t, &j, t0.Add(time.Minute))
	wantJob(t, "early", j.Due(t0.Add(59*time.Second)), updater.NoJob)
	wantJob(t, "due", j.Due(t0.Add(time.Minute)), updater.CheckJob)
	// one job at a time: nothing else is due while it runs
	wantWake(t, &j, time.Time{})
	wantJob(t, "busy", j.Due(t0.Add(time.Hour)), updater.NoJob)
	wantJob(t, "done", j.Checked(current, t0.Add(2*time.Minute), 0), updater.NoJob)
	wantWake(t, &j, t0.Add(2*time.Minute+24*time.Hour))
}

func TestNextCheckFollowsTheOutcome(t *testing.T) {
	for _, c := range []struct {
		o    updater.Outcome
		next time.Duration
	}{
		{current, 24 * time.Hour},
		{installed, 24 * time.Hour},
		{updater.Outcome{Result: updater.Skipped}, time.Hour},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailTLS}, time.Hour},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailIO}, time.Hour},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailStale}, 24 * time.Hour},
	} {
		var j updater.Jobs
		j.Start(t0)
		wantJob(t, "trigger", j.Trigger(updater.NoBuild), updater.CheckJob)
		wantJob(t, "done", j.Checked(c.o, t0, 0), updater.NoJob)
		wantWake(t, &j, t0.Add(c.next))
	}
}

// no build, or an expired one: a check at once; one asked for while another
// runs follows it
func TestTriggerChecksAtOnce(t *testing.T) {
	var j updater.Jobs
	wantJob(t, "inactive", j.Trigger(updater.NoBuild), updater.NoJob)
	j.Start(t0)
	wantJob(t, "trigger", j.Trigger(updater.NoBuild), updater.CheckJob)
	wantJob(t, "while busy", j.Trigger(updater.NoBuild), updater.NoJob)
	wantJob(t, "queued", j.Checked(updater.Outcome{Result: updater.Failed, Failure: updater.FailDNS}, t0, 0), updater.CheckJob)
	wantJob(t, "done", j.Checked(current, t0, 0), updater.NoJob)
	wantWake(t, &j, t0.Add(24*time.Hour))
}

func TestStopEndsTheSchedule(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	wantJob(t, "trigger", j.Trigger(updater.NoBuild), updater.CheckJob)
	wantJob(t, "while busy", j.Trigger(updater.NoBuild), updater.NoJob)
	j.Stop()
	wantJob(t, "done after stop", j.Checked(current, t0, 0), updater.NoJob)
	wantWake(t, &j, time.Time{})
	wantJob(t, "trigger after stop", j.Trigger(updater.NoBuild), updater.NoJob)
	wantJob(t, "due after stop", j.Due(t0.Add(48*time.Hour)), updater.NoJob)
	j.Start(t0.Add(time.Hour))
	wantWake(t, &j, t0.Add(time.Hour+time.Minute))
}

// only the starts of a build this run installed count, from its switch: the
// third silent one in a row rolls it back, and an answer starts them over
func TestSilentStartsRollBackANewBuild(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	wantJob(t, "an old build's silent starts", j.Silent(5), updater.NoJob)
	wantJob(t, "trigger", j.Trigger(updater.NoBuild), updater.CheckJob)
	// audiod counted 2 before the switch, and starts over at it
	wantJob(t, "installed", j.Checked(installed, t0, 2), updater.NoJob)
	for _, n := range []uint32{2, 3, 4} {
		wantJob(t, "before audiod took the switch", j.Silent(n), updater.NoJob)
	}
	for _, n := range []uint32{0, 1, 2, 0, 1, 2} {
		wantJob(t, "silent", j.Silent(n), updater.NoJob)
	}
	wantJob(t, "the third", j.Silent(3), updater.RollbackJob)
	wantJob(t, "armed once", j.Silent(9), updater.NoJob)
	wantJob(t, "rolled back", j.RolledBack(t0.Add(time.Hour)), updater.NoJob)
	// soloist stays down until the check a day on: no build is no trigger now
	wantWake(t, &j, t0.Add(25*time.Hour))
	wantJob(t, "no build after the rollback", j.Trigger(updater.NoBuild), updater.NoJob)
	wantJob(t, "a day on", j.Due(t0.Add(25*time.Hour)), updater.CheckJob)
	wantJob(t, "done", j.Checked(current, t0.Add(25*time.Hour), 0), updater.NoJob)
	wantJob(t, "a trigger again", j.Trigger(updater.NoBuild), updater.CheckJob)
}

// a rollback waits for the running check and goes before a queued one, which
// it then drops; an install drops a queued rollback
func TestRollbackQueue(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	j.Trigger(updater.NoBuild)
	j.Checked(installed, t0, 0)
	wantJob(t, "a check", j.Due(t0.Add(24*time.Hour)), updater.CheckJob)
	wantJob(t, "a check asked for", j.Trigger(updater.NoBuild), updater.NoJob)
	wantJob(t, "the third silent start", j.Silent(3), updater.NoJob)
	wantJob(t, "the check ends", j.Checked(current, t0.Add(24*time.Hour), 3), updater.RollbackJob)
	wantJob(t, "the rollback ends", j.RolledBack(t0.Add(24*time.Hour)), updater.NoJob)
	wantWake(t, &j, t0.Add(48*time.Hour))

	var k updater.Jobs
	k.Start(t0)
	k.Trigger(updater.NoBuild)
	k.Checked(installed, t0, 0)
	k.Due(t0.Add(24 * time.Hour))
	wantJob(t, "the third silent start", k.Silent(3), updater.NoJob)
	// the newer build replaced the silent one: rolling back would restore the silent build
	wantJob(t, "a newer build installed", k.Checked(installed, t0.Add(24*time.Hour), 3), updater.NoJob)
	wantJob(t, "its own silent starts count", k.Silent(5), updater.NoJob)
	wantJob(t, "its third", k.Silent(6), updater.RollbackJob)
}

// after a rollback only no_build waits for the check a day on: an expired
// build is replaced at once
func TestExpiredChecksAtOnceAfterARollback(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	j.Trigger(updater.NoBuild)
	j.Checked(installed, t0, 0)
	wantJob(t, "the third", j.Silent(3), updater.RollbackJob)
	wantJob(t, "rolled back", j.RolledBack(t0), updater.NoJob)
	wantJob(t, "no build", j.Trigger(updater.NoBuild), updater.NoJob)
	wantJob(t, "expired", j.Trigger(updater.Expired), updater.CheckJob)
	wantJob(t, "done", j.Checked(current, t0.Add(time.Hour), 0), updater.NoJob)
	// that check was the one the rollback waited for
	wantJob(t, "no build after it", j.Trigger(updater.NoBuild), updater.CheckJob)
}

// a rollback that could not probe the build before (audiod gone) runs again
// at audiod's next status, unless a newer build replaced the silent one
func TestDeferredRollbackRunsAgain(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	j.Trigger(updater.NoBuild)
	j.Checked(installed, t0, 0)
	wantJob(t, "the third", j.Silent(3), updater.RollbackJob)
	wantJob(t, "deferred", j.RollbackDeferred(), updater.NoJob)
	wantWake(t, &j, t0.Add(24*time.Hour))
	wantJob(t, "audiod reports again", j.Silent(0), updater.RollbackJob)
	wantJob(t, "rolled back", j.RolledBack(t0), updater.NoJob)

	var k updater.Jobs
	k.Start(t0)
	k.Trigger(updater.NoBuild)
	k.Checked(installed, t0, 0)
	k.Silent(3)
	k.RollbackDeferred()
	wantJob(t, "a check", k.Due(t0.Add(24*time.Hour)), updater.CheckJob)
	wantJob(t, "a newer build", k.Checked(installed, t0.Add(24*time.Hour), 0), updater.NoJob)
	wantJob(t, "audiod reports again", k.Silent(0), updater.NoJob)
}

// silent starts seen while a check runs still lower the count's base: a reset
// that comes before the check's end never delays the rollback
func TestSilentBaseFollowsResetsBeforeTheCheckEnds(t *testing.T) {
	var j updater.Jobs
	j.Start(t0)
	wantJob(t, "trigger", j.Trigger(updater.NoBuild), updater.CheckJob)
	j.Silent(0)
	wantJob(t, "installed", j.Checked(installed, t0, 2), updater.NoJob)
	j.Silent(1)
	j.Silent(2)
	wantJob(t, "the third", j.Silent(3), updater.RollbackJob)
}
