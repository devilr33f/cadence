package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var (
	wake     = cadencev2.Trigger_TRIGGER_WAKE
	button   = cadencev2.Trigger_TRIGGER_BUTTON
	followUp = cadencev2.Trigger_TRIGGER_FOLLOW_UP
)

// at night on the device's own clock the update waits: a follow-up alone
// does not wake the user, nor a wake nobody spoke in; a session they woke the
// device for and spoke in does
func TestQuietHoursHoldUntilAWokenSessionEnds(t *testing.T) {
	r := newReportRig(t)
	// 17:30 UTC is 23:30 in Bishkek
	r.clock.Set(time.Date(2026, 11, 9, 17, 30, 0, 0, time.UTC))
	r.setValues(func(v *settings.Values) { v.TimeZone = "Asia/Bishkek" })
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.noneSpoken()
	r.rep.SessionEnded(r.dev.String(), followUp, true)
	r.noneSpoken()
	// a false wake hears no one: the night stays quiet
	r.rep.SessionEnded(r.dev.String(), wake, false)
	r.noneSpoken()
	r.rep.SessionEnded(r.dev.String(), wake, true)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// quiet hours that wrap past midnight hold until their end
func TestQuietHoursReleaseAtTheirEnd(t *testing.T) {
	r := newReportRig(t)
	r.clock.Set(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC))
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.noneSpoken()
	r.clock.Set(time.Date(2026, 11, 9, 7, 59, 0, 0, time.UTC))
	r.rep.sweep(r.ctx)
	r.noneSpoken()
	r.clock.Set(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	r.rep.sweep(r.ctx)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// a woken session's follow-ups keep the user awake; the update waits for the
// end of the last of them, and a while later the night holds again
func TestFollowUpsKeepTheUserAwake(t *testing.T) {
	r := newReportRig(t)
	r.rep.lim.settle = 2 * time.Second
	r.clock.Set(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC))
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.rep.SessionEnded(r.dev.String(), wake, true)
	r.noneSpoken()
	// the follow-up begins while the end settles
	r.voice.set(func(v *fakeVoice) { v.active = true })
	r.clock.Advance(3 * time.Second)
	r.rep.sweep(r.ctx)
	r.noneSpoken()
	r.voice.set(func(v *fakeVoice) { v.active = false })
	r.rep.SessionEnded(r.dev.String(), followUp, true)
	r.noneSpoken()
	r.clock.Advance(3 * time.Second)
	r.rep.sweep(r.ctx)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")

	r.clock.Advance(r.rep.lim.awake)
	later := r.task("y")
	r.finish(later, "r")
	r.waitTurns(2)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// an event turn waits for the voice turn running on its device, and the
// update for the session's end
func TestEventTurnsWaitForAVoiceSession(t *testing.T) {
	r := newReportRig(t)
	r.voice.set(func(v *fakeVoice) { v.active = true })
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	waitUntil(t, "the event queued", func() bool { return r.rep.queued(r.dev.String()) == 1 })
	r.waitIdle()
	if n := len(r.turns.all()); n != 0 {
		t.Fatalf("%d turns beside the voice turn", n)
	}
	r.voice.set(func(v *fakeVoice) { v.active = false })
	r.rep.SessionEnded(r.dev.String(), button, true)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// an offline device gets its update when it is back, after any session it
// is in by then
func TestOfflineHoldUntilConnect(t *testing.T) {
	r := newReportRig(t)
	r.online.Store(false)
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.noneSpoken()
	r.online.Store(true)
	r.voice.set(func(v *fakeVoice) { v.active = true })
	r.rep.Connected(r.dev.String())
	r.noneSpoken()
	r.voice.set(func(v *fakeVoice) { v.active = false })
	r.rep.SessionEnded(r.dev.String(), button, true)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// a failed announcement is tried again after a backoff, and once one went
// out nothing repeats it
func TestFailedAnnouncementRetriedThenDeliveredOnce(t *testing.T) {
	r := newReportRig(t)
	r.voice.set(func(v *fakeVoice) { v.fail = errors.New("tts failed") })
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.noneSpoken()
	if _, tries := r.voice.said(); tries != 1 {
		t.Fatalf("%d tries", tries)
	}
	r.rep.sweep(r.ctx)
	r.waitIdle()
	if _, tries := r.voice.said(); tries != 1 {
		t.Fatalf("%d tries within the backoff", tries)
	}
	r.voice.set(func(v *fakeVoice) { v.fail = nil })
	r.clock.Advance(r.rep.lim.backoff)
	r.rep.sweep(r.ctx)
	want := "NOTICE " + tk.ShortID + " done"
	r.waitSpoken(want)
	for range 3 {
		r.rep.sweep(r.ctx)
		r.rep.Connected(r.dev.String())
		r.rep.AnnounceEnded(r.dev.String())
	}
	r.waitSpoken(want)
	if _, tries := r.voice.said(); tries != 2 {
		t.Fatalf("%d tries", tries)
	}
	if !strings.Contains(r.logs.String(), "task_notice_failed") {
		t.Fatalf("logs %s", r.logs.String())
	}
}

// a newer event replaces the update still waiting for its task; a question
// answered meanwhile is no news at all
func TestNewerEventReplacesAWaitingUpdate(t *testing.T) {
	r := newReportRig(t)
	r.online.Store(false)
	r.start()
	tk := r.task("x")
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "which?", Token: "q1"}})
	r.waitTurns(1)
	if _, err := r.m.Send(context.Background(), tk.ShortID, "q1", "the blue one"); err != nil {
		t.Fatal(err)
	}
	r.finish(tk, "r")
	r.waitTurns(2)
	if n := r.undelivered(); n != 1 {
		t.Fatalf("%d undelivered", n)
	}
	r.online.Store(true)
	r.rep.Connected(r.dev.String())
	r.waitSpoken("NOTICE " + tk.ShortID + " done")

	asks := r.task("y")
	r.voice.set(func(v *fakeVoice) { v.spoken = nil })
	r.online.Store(false)
	r.k.Emit(Event{Ref: asks.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionClarify, Text: "which?", Token: "q2"}})
	r.waitTurns(3)
	if _, err := r.m.Send(context.Background(), asks.ShortID, "q2", "the red one"); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(asks, StatusRunning)
	r.online.Store(true)
	r.rep.Connected(r.dev.String())
	r.noneSpoken()
	if n := r.undelivered(); n != 0 {
		t.Fatalf("%d undelivered", n)
	}
	if !strings.Contains(r.logs.String(), "task_notice_stale") {
		t.Fatalf("logs %s", r.logs.String())
	}
}

// updates never cut each other or anyone else's announcement short: the
// next waits for the end of the one under way, oldest first
func TestUpdatesWaitForTheAnnouncementUnderWay(t *testing.T) {
	r := newReportRig(t)
	r.online.Store(false)
	r.voice.set(func(v *fakeVoice) { v.hold, v.announcing = true, true })
	r.start()
	a, b := r.task("a"), r.task("b")
	r.finish(a, "r")
	r.waitTurns(1)
	r.finish(b, "r")
	r.waitTurns(2)
	r.online.Store(true)
	r.rep.Connected(r.dev.String())
	r.noneSpoken()
	r.voice.set(func(v *fakeVoice) { v.announcing = false })
	r.rep.AnnounceEnded(r.dev.String())
	first := "NOTICE " + a.ShortID + " done"
	r.waitSpoken(first)
	r.voice.set(func(v *fakeVoice) { v.announcing = false })
	r.rep.AnnounceEnded(r.dev.String())
	r.waitSpoken(first, "NOTICE "+b.ShortID+" done")
}

// a question answered meanwhile is no news, and its event counts as told: the
// waiting status that still shows runs no turn again
func TestStaleUpdateMarksItsEventReported(t *testing.T) {
	r := newReportRig(t)
	r.online.Store(false)
	r.start()
	tk := r.task("x")
	r.k.Emit(Event{Ref: tk.ExternalRef, Kind: "waiting_input", Question: &Question{Kind: QuestionApproval, Text: "rm it?", Token: "a1"}})
	r.waitTurns(1)
	// the answer is kept before the kind reports the resume
	answer, _ := answerOf(DecisionOnce, dash)
	if _, err := r.s.Answered(context.Background(), tk.ID, answer); err != nil {
		t.Fatal(err)
	}
	r.online.Store(true)
	r.rep.Connected(r.dev.String())
	r.noneSpoken()
	if got, _ := r.m.Get(context.Background(), tk.ShortID); got.Status != StatusWaitingInput || got.ReportedAt == nil {
		t.Fatalf("task %+v", got)
	}
	r.rep.sweep(r.ctx)
	r.waitIdle()
	if n := len(r.turns.all()); n != 1 {
		t.Fatalf("%d turns", n)
	}
}

// an announcement that went out stays the only one when its delivery does not
// store: the next pass records it
func TestDeliveryStoredOnTheNextPass(t *testing.T) {
	r := newReportRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse BEFORE UPDATE ON task_notices FOR EACH ROW EXECUTE FUNCTION refuse()`); err != nil {
		t.Fatal(err)
	}
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	want := "NOTICE " + tk.ShortID + " done"
	r.waitSpoken(want)
	if r.undelivered() != 1 {
		t.Fatal("the refused delivery stored")
	}
	if _, err := r.pool.Exec(ctx, `DROP TRIGGER refuse ON task_notices`); err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(r.rep.lim.backoff)
	r.rep.sweep(r.ctx)
	waitUntil(t, "the delivery stored", func() bool { return r.undelivered() == 0 })
	r.waitSpoken(want)
	if got, _ := r.m.Get(ctx, tk.ShortID); got.ReportedAt == nil {
		t.Fatal("the task is not reported")
	}
}

// refuseMarks makes every mark of a task as told fail until the returned func
// lifts it
func (r *reportRig) refuseMarks() (lift func()) {
	r.t.Helper()
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `CREATE FUNCTION refuse_mark() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse_mark BEFORE UPDATE OF reported_at ON tasks FOR EACH ROW
		WHEN (OLD.reported_at IS NULL AND NEW.reported_at IS NOT NULL) EXECUTE FUNCTION refuse_mark()`); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		if _, err := r.pool.Exec(ctx, `DROP TRIGGER refuse_mark ON tasks`); err != nil {
			r.t.Fatal(err)
		}
	}
}

// pruneNotices ages every delivered notice past its retention and prunes it.
func (r *reportRig) pruneNotices() {
	r.t.Helper()
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `UPDATE task_notices SET delivered_at = now() - interval '15 days' WHERE delivered_at IS NOT NULL`); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.s.PruneNotices(ctx, time.Now().Add(-NoticeRetention)); err != nil {
		r.t.Fatal(err)
	}
}

// a delivery and its mark store together or not at all, and a failed one is
// tried again: a notice the prune takes was told, so it never runs a second
// turn or speaks twice
func TestDeliveryAndMarkStoreTogether(t *testing.T) {
	r := newReportRig(t)
	lift := r.refuseMarks()
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	want := "NOTICE " + tk.ShortID + " done"
	r.waitSpoken(want)
	if got, _ := r.m.Get(context.Background(), tk.ShortID); r.undelivered() != 1 || got.ReportedAt != nil {
		t.Fatalf("undelivered %d, task %+v", r.undelivered(), got)
	}
	r.pruneNotices()
	r.rep.sweep(r.ctx)
	r.waitIdle()
	if n := len(r.turns.all()); n != 1 {
		t.Fatalf("%d turns", n)
	}
	lift()
	r.clock.Advance(r.rep.lim.backoff)
	r.rep.sweep(r.ctx)
	waitUntil(t, "the delivery stored", func() bool { return r.undelivered() == 0 })
	if got, _ := r.m.Get(context.Background(), tk.ShortID); got.ReportedAt == nil {
		t.Fatal("delivered but not told")
	}
	r.pruneNotices()
	r.rep.sweep(r.ctx)
	r.waitSpoken(want)
	if n := len(r.turns.all()); n != 1 {
		t.Fatalf("%d turns after the prune", n)
	}
}

// a silent turn's mark that fails is tried again, never by a second turn
func TestSilentMarkRetriedWithoutASecondTurn(t *testing.T) {
	r := newReportRig(t)
	r.turns.setSay(func(context.Context, EventTurn) (string, error) { return "", nil })
	lift := r.refuseMarks()
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.rep.sweep(r.ctx)
	r.waitIdle()
	if n := len(r.turns.all()); n != 1 {
		t.Fatalf("%d turns", n)
	}
	if got, _ := r.m.Get(context.Background(), tk.ShortID); got.ReportedAt != nil {
		t.Fatal("the refused mark stored")
	}
	lift()
	r.rep.sweep(r.ctx)
	waitUntil(t, "the mark stored", func() bool { got, _ := r.m.Get(context.Background(), tk.ShortID); return got.ReportedAt != nil })
	r.waitIdle()
	if n := len(r.turns.all()); n != 1 {
		t.Fatalf("%d turns", n)
	}
	r.noneSpoken()
}

// the awake window is 2 min unless the dev stack shortens it
func TestAwakeWindow(t *testing.T) {
	if got := NewReporter(ReporterDeps{}).lim.awake; got != 2*time.Minute {
		t.Fatalf("default %v", got)
	}
	if got := NewReporter(ReporterDeps{Awake: 5 * time.Second}).lim.awake; got != 5*time.Second {
		t.Fatalf("set %v", got)
	}
}
