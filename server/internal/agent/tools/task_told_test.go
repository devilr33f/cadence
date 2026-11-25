package tools_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// toldVoice announces at once on an idle device and counts what it spoke.
type toldVoice struct {
	mu     sync.Mutex
	spoken int
}

func (*toldVoice) SessionActive(string) bool { return false }
func (*toldVoice) Announcing(string) bool    { return false }

func (v *toldVoice) Announce(context.Context, string, string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.spoken++
	return nil
}

func (v *toldVoice) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.spoken
}

// toldTurns says one line about each event, or nothing once silent.
type toldTurns struct {
	mu     sync.Mutex
	silent bool
	ran    int
}

func (f *toldTurns) RunEvent(context.Context, tasks.EventTurn) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran++
	if f.silent {
		return "", nil
	}
	return "A task asks something.", nil
}

func (f *toldTurns) set(silent bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.silent = silent
}

func (f *toldTurns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ran
}

// a voice approval answers only a question the user was told on the device:
// its update spoken there. One held in quiet hours, or that its event turn
// left unsaid, was never heard, so the user's words cannot approve it
// (Ruling M5b-R9)
func TestVoiceApprovalNeedsAToldQuestion(t *testing.T) {
	r := newTaskRig(t)
	ctx := context.Background()
	dev := uuid.MustParse(r.dev)
	voice, turns := &toldVoice{}, &toldTurns{}
	var mu sync.Mutex
	quiet := false
	rep := tasks.NewReporter(tasks.ReporterDeps{
		Manager: r.m, Turns: turns, Voice: voice, Online: func(string) bool { return true },
		Settings: func(context.Context, string) (settings.Values, error) {
			mu.Lock()
			defer mu.Unlock()
			v := settings.Defaults()
			v.QuietHours = settings.QuietHours{}
			if quiet {
				// the hour around now, on the UTC clock
				now := time.Now().UTC()
				v.QuietHours = settings.QuietHours{Enabled: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04")}
			}
			return v, nil
		},
	})
	rctx, cancel := context.WithCancel(ctx)
	stop := rep.Run(rctx)
	t.Cleanup(func() { cancel(); stop() })
	tk, err := r.m.Start(ctx, "hermes", tasks.Spec{Instructions: "clean up", Origin: dev})
	if err != nil {
		t.Fatal(err)
	}
	approve := func(token, code string) {
		t.Helper()
		args := `{"id":"` + tk.ShortID + `","token":"` + token + `","decision":"once","userWords":"delete the old pods"}`
		if code == "" {
			r.ok("task_reply", wake, args)
			return
		}
		if e := r.refused("task_reply", wake, args, code); e.Retryable {
			t.Fatalf("retryable: %+v", e)
		}
	}
	r.text = "Delete the old pods."

	r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods?", Token: "a1"})
	waitUntil(t, "the update spoken and stored", func() bool {
		n, err := r.m.Undelivered(ctx, dev)
		return voice.count() == 1 && err == nil && n == 0
	})
	approve("a1", "")

	// held in quiet hours: never heard
	mu.Lock()
	quiet = true
	mu.Unlock()
	r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods in staging?", Token: "b1"})
	waitUntil(t, "the held update", func() bool { n, err := r.m.Undelivered(ctx, dev); return err == nil && n == 1 })
	approve("b1", "question_not_told")

	// an event turn that says nothing tells nobody
	turns.set(true)
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "progress"})
	r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods in prod?", Token: "c1"})
	waitUntil(t, "the silent turn", func() bool {
		got, err := r.m.Get(ctx, tk.ShortID)
		return turns.count() == 3 && err == nil && got.ReportedAt != nil
	})
	approve("c1", "question_not_told")
	if len(r.k.Approved) != 1 || voice.count() != 1 {
		t.Fatalf("approved %v, spoken %d", r.k.Approved, voice.count())
	}
}

// a question a turn the user spoke in read may be told to them, so they may
// answer it in a later turn: never in the turn that read it, where their
// words came before it, and never from another device or an event turn
func TestReadingAQuestionTellsIt(t *testing.T) {
	r := newTaskRig(t)
	ctx := context.Background()
	other, err := db.New(r.pool).CreateDevice(ctx, db.CreateDeviceParams{Name: "other", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	tk := r.start("x")
	r.text = "Delete the old pods."
	approve := func(token string) string {
		return `{"id":"` + tk.ShortID + `","token":"` + token + `","decision":"once","userWords":"delete the old pods"}`
	}
	tk = r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods?", Token: "a1"})
	r.k.Snapshots[tk.ExternalRef] = tasks.Snapshot{Status: tasks.StatusWaitingInput, Question: tk.Question}
	inspect := `{"id":"` + tk.ShortID + `","detail":"summary"}`

	// told on another device
	if err := r.m.MarkTold(ctx, tk.ShortID, "a1", other.ID.Bytes, ""); err != nil {
		t.Fatal(err)
	}
	r.refused("task_reply", wake, approve("a1"), "question_not_told")
	// read in an event turn
	r.turn, r.taskID = "event-turn", tk.ShortID
	r.ok("task_inspect", event, inspect)
	r.taskID = ""
	r.turn = "turn-2"
	r.refused("task_reply", wake, approve("a1"), "question_not_told")
	// read in this very turn: the user spoke before they heard it
	r.ok("task_inspect", wake, inspect)
	r.refused("task_reply", wake, approve("a1"), "question_not_told")
	// heard, then answered when they next woke the device
	r.turn = "turn-3"
	r.ok("task_reply", wake, approve("a1"))

	// task_list shows the text too
	r.k.Emit(tasks.Event{Ref: tk.ExternalRef, Kind: "progress"})
	r.ask(tk, tasks.Question{Kind: tasks.QuestionApproval, Text: "Delete the old pods again?", Token: "a2"})
	r.ok("task_list", followUp, `{}`)
	r.refused("task_reply", wake, approve("a2"), "question_not_told")
	r.turn = "turn-4"
	r.ok("task_reply", wake, approve("a2"))
	if len(r.k.Approved) != 2 {
		t.Fatalf("approved %v", r.k.Approved)
	}
}
