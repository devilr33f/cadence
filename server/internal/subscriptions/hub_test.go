package subscriptions

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func stopped(id, title string) Update {
	return Update{Topic: thread(id), Event: EventStopped, Kind: "thread", Title: title, Ref: Ref{ID: id}}
}

func asked(id, title, token string, kind string) Update {
	ev := EventQuestion
	if kind == QuestionApproval {
		ev = EventApproval
	}
	return Update{Topic: thread(id), Event: ev, Kind: "thread", Title: title, Ref: Ref{ID: id},
		Question: &Question{Kind: kind, Token: token, Text: "Proceed with deploy?",
			Options: []Option{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}}}
}

// a speaker update is spoken from the template, written into the history as a
// notice the agent's history never reads, and marked delivered
func TestSpeakerDeliveryAndNotice(t *testing.T) {
	r := newRig(t)
	r.subscribe("t3code:work:*", "", "")
	r.push(stopped("a", "Fix login"))
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	if got := r.voice.spoken()[0]; got != "Thread “Fix login” stopped." {
		t.Fatalf("said %q", got)
	}
	r.eventually("delivered", func() bool { it := r.inbox(); return len(it) == 1 && it[0].State == stateDelivered })
	turns, _, err := r.convs.Turns(r.ctx, r.dev, "", 10)
	if err != nil || len(turns) != 1 || turns[0].Trigger != "notice" || len(turns[0].Lines) != 1 ||
		turns[0].Lines[0].Role != "assistant" || turns[0].Lines[0].Text != "Thread “Fix login” stopped." {
		t.Fatalf("history %+v %v", turns, err)
	}
	hist, err := r.convs.History(r.ctx, r.dev, 0, 10)
	if err != nil || len(hist) != 0 {
		t.Fatalf("the agent reads the notice: %+v %v", hist, err)
	}
	after, err := r.convs.After(r.ctx, r.dev, 0, time.Now().Add(time.Hour), 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("learning reads the notice: %+v %v", after, err)
	}
}

// updates within the window go as one announcement, in the device's language
func TestCoalescedAnnouncement(t *testing.T) {
	r := newRig(t)
	r.settings(func(v *settings.Values) { v.Language = "ru" })
	r.subscribe("t3code:work:*", "", "")
	// a session holds them, so a slow store cannot split the window
	r.setSession(true)
	r.push(stopped("a", "A"))
	r.push(stopped("b", "B"))
	r.push(asked("c", "Login", "tok1", QuestionClarify))
	r.push(stopped("d", "D"))
	r.setSession(false)
	r.hub.SessionEnded(r.dev, cadencev2.Trigger_TRIGGER_WAKE, false)
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	if got := r.voice.spoken()[0]; got != "Три треда остановились, тред „Login“ спрашивает." {
		t.Fatalf("said %q", got)
	}
	r.quiet(400*time.Millisecond, 1)
}

// a busy device holds its updates, never drops them; once free they go
// (afterBusy announce) or wait for its next woken turn (afterBusy inbox)
func TestHeldWhileBusy(t *testing.T) {
	r := newRig(t)
	r.subscribe("t3code:work:*", "", "")
	r.busy.set(true)
	r.push(stopped("a", "A"))
	r.eventually("held", func() bool { it := r.inbox(); return len(it) == 1 && it[0].HeldBusy })
	r.quiet(300*time.Millisecond, 0)
	r.push(stopped("b", "B"))
	r.quiet(500*time.Millisecond, 0)
	r.busy.set(false)
	r.hub.BusyChanged(r.dev)
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	if got := r.voice.spoken()[0]; got != "Two threads stopped." {
		t.Fatalf("said %q", got)
	}

	r.mu.Lock()
	r.after = AfterBusyInbox
	r.mu.Unlock()
	r.busy.set(true)
	r.push(stopped("c", "C"))
	r.eventually("held", func() bool {
		for _, it := range r.inbox() {
			if it.Title == "C" && it.HeldBusy {
				return true
			}
		}
		return false
	})
	r.busy.set(false)
	r.hub.BusyChanged("")
	r.eventually("inbox", func() bool {
		for _, it := range r.inbox() {
			if it.Title == "C" && it.State == stateInbox {
				return true
			}
		}
		return false
	})
	r.quiet(300*time.Millisecond, 1)
	note, err := r.hub.Inbox(r.ctx, r.dev, "turn-1")
	if err != nil || !strings.Contains(note, `"t3code:work:thread:c"`) || strings.Contains(note, `"C"`) {
		t.Fatalf("block %q %v", note, err)
	}
}

// quiet hours hold an update until the user is awake: a woken session they
// spoke in
func TestQuietHoursHold(t *testing.T) {
	r := newRig(t)
	now := time.Now().UTC()
	r.settings(func(v *settings.Values) {
		v.TimeZone = "UTC"
		v.QuietHours = settings.QuietHours{Enabled: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04")}
	})
	r.subscribe("t3code:work:*", "", "")
	r.push(stopped("a", "A"))
	r.quiet(600*time.Millisecond, 0)
	// a false wake wakes no one
	r.hub.SessionEnded(r.dev, cadencev2.Trigger_TRIGGER_WAKE, false)
	r.quiet(300*time.Millisecond, 0)
	r.hub.SessionEnded(r.dev, cadencev2.Trigger_TRIGGER_WAKE, true)
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
}

// a session or an announcement under way, or an offline device, holds it too
func TestHeldWhileSessionRuns(t *testing.T) {
	r := newRig(t)
	r.setSession(true)
	r.subscribe("t3code:work:*", "", "")
	r.push(stopped("a", "A"))
	r.quiet(500*time.Millisecond, 0)
	r.setSession(false)
	r.hub.SessionEnded(r.dev, cadencev2.Trigger_TRIGGER_FOLLOW_UP, true)
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
}

// next_stop ends a subscription with its first stop: a question before it
// keeps it, the stop is delivered and the next one is not
func TestNextStop(t *testing.T) {
	r := newRig(t)
	s := r.subscribe("t3code:work:thread:a", DeliverySpeaker, UntilNextStop)
	r.push(asked("a", "A", "tok1", QuestionClarify))
	if subs, _ := r.hub.List(r.ctx, r.dev); len(subs) != 1 {
		t.Fatalf("ended by a question: %+v", subs)
	}
	r.push(stopped("a", "A"))
	if subs, _ := r.hub.List(r.ctx, r.dev); len(subs) != 0 {
		t.Fatalf("still there: %+v", subs)
	}
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) >= 1 })
	r.pushNone(stopped("a", "A"))
	_ = s
}

// the deciding subscription's delivery and events rule: mute silences, an
// event it did not ask for is left out
func TestIngestRules(t *testing.T) {
	r := newRig(t)
	r.subscribe("t3code:work:*", DeliverySpeaker, "", EventStopped)
	r.subscribe("t3code:work:thread:m", DeliveryMute, "")
	r.pushNone(stopped("m", "M"))
	r.pushNone(asked("a", "A", "tok1", QuestionClarify))
	r.push(stopped("a", "A"))
	// another module's topic on this module's stream is refused
	r.pushNone(Update{Topic: NewTopic("hermes", "work", "task", "x"), Event: EventStopped})
	// an event the module does not emit is refused
	r.pushNone(Update{Topic: thread("a"), Event: EventDone})
}

// an agent delivery runs one event turn with the updates as data; its words
// are spoken, a silent turn delivers them untold
func TestAgentDelivery(t *testing.T) {
	r := newRig(t)
	r.turns.say = "Your login thread is done."
	r.subscribe("t3code:work:*", DeliveryAgent, "")
	r.push(asked("a", "Fix login", "tok1", QuestionClarify))
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	if got := r.voice.spoken()[0]; got != "Your login thread is done." {
		t.Fatalf("said %q", got)
	}
	runs := r.turns.all()
	if len(runs) != 1 || runs[0].Text != EventFrame || runs[0].DeviceID != r.dev {
		t.Fatalf("runs %+v", runs)
	}
	var data struct {
		Updates []struct {
			Topic, Event, Title string
			Question            *Question
		}
	}
	if err := json.Unmarshal(runs[0].Data, &data); err != nil || len(data.Updates) != 1 ||
		data.Updates[0].Title != "Fix login" || data.Updates[0].Question.Token != "tok1" {
		t.Fatalf("data %s %v", runs[0].Data, err)
	}
	if strings.Contains(runs[0].Text, "Fix login") {
		t.Fatal("outside text in the framing")
	}
	r.eventually("delivered", func() bool { return r.inbox()[0].State == stateDelivered })
	it := r.inbox()[0]
	if told, _ := r.hub.ToldBefore(r.ctx, it, r.dev, "later"); !told {
		t.Fatal("spoken question not told")
	}

	r.turns.mu.Lock()
	r.turns.say = ""
	r.turns.mu.Unlock()
	r.push(asked("b", "B", "tok2", QuestionApproval))
	r.eventually("delivered", func() bool {
		for _, it := range r.inbox() {
			if it.RefID == "b" && it.State == stateDelivered {
				return true
			}
		}
		return false
	})
	r.quiet(200*time.Millisecond, 1)
	q, err := r.hub.Question(r.ctx, r.dev, "tok2")
	if err != nil {
		t.Fatal(err)
	}
	if told, _ := r.hub.ToldBefore(r.ctx, q, r.dev, "later"); told {
		t.Fatal("a silent turn told the question")
	}
}

// the unread block: by topic, Cadence's own record alone (no title, text,
// question wording or option label); read marks seen but tells nothing, an
// inspect tells its open questions by that turn alone; an open question stays
// until answered or withdrawn
func TestInboxBlock(t *testing.T) {
	r := newRig(t)
	r.busy.set(true)
	r.mu.Lock()
	r.after = AfterBusyInbox
	r.mu.Unlock()
	r.subscribe("t3code:work:*", "", "")
	r.push(stopped("a", "Fix login"))
	r.push(asked("b", "Deploy", "tok-b", QuestionApproval))
	r.push(asked("c", "Pick", "tok-c", QuestionClarify))
	r.eventually("held", func() bool {
		n := 0
		for _, it := range r.inbox() {
			if it.HeldBusy {
				n++
			}
		}
		return n == 3
	})
	r.busy.set(false)
	r.hub.BusyChanged(r.dev)
	r.eventually("inbox", func() bool {
		n := 0
		for _, it := range r.inbox() {
			if it.State == stateInbox {
				n++
			}
		}
		return n == 3
	})
	note, err := r.hub.Inbox(r.ctx, r.dev, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		inboxHeader, `- "t3code:work:thread:a" (module t3code, instance work, thread): 1 update: stopped at `,
		`open: an approval question (token "tok-b"); decisions "once", "session", "deny"`,
		`open: a clarify question (token "tok-c", 2 options)`,
		inboxAnswerNote,
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("block lacks %q:\n%s", want, note)
		}
	}
	for _, outside := range []string{"Fix login", "Deploy", "Pick", "Proceed", "Yes", "No\""} {
		if strings.Contains(note, outside) {
			t.Fatalf("block carries outside text %q:\n%s", outside, note)
		}
	}
	it, _ := r.hub.Question(r.ctx, r.dev, "tok-b")
	if told, _ := r.hub.ToldBefore(r.ctx, it, r.dev, "turn-2"); told {
		t.Fatal("the block told the question")
	}
	// an inspect tells it, by its own turn alone
	got, err := r.hub.Inspect(r.ctx, r.dev, "turn-1", "tok-b", "")
	if err != nil || len(got) != 1 || got[0].Question.Text != "Proceed with deploy?" || got[0].Title != "Deploy" {
		t.Fatalf("inspect %+v %v", got, err)
	}
	if told, _ := r.hub.ToldBefore(r.ctx, it, r.dev, "turn-1"); told {
		t.Fatal("told before the turn that inspected it")
	}
	if told, _ := r.hub.ToldBefore(r.ctx, it, r.dev, "turn-2"); !told {
		t.Fatal("not told before a later turn")
	}
	// a topic's latest updates, oldest first
	byTopic, err := r.hub.Inspect(r.ctx, r.dev, "turn-1", "", "t3code:work:thread:a")
	if err != nil || len(byTopic) != 1 || byTopic[0].Title != "Fix login" {
		t.Fatalf("inspect topic %+v %v", byTopic, err)
	}
	if _, err := r.hub.Inspect(r.ctx, r.dev, "turn-1", "", "t3code:work:*"); !errors.Is(err, ErrTopic) {
		t.Fatalf("wild topic %v", err)
	}
	if _, err := r.hub.Inspect(r.ctx, r.dev, "turn-1", "", "t3code:work:thread:zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty topic %v", err)
	}
	// seen: the stop is gone, the questions stay
	note, _ = r.hub.Inbox(r.ctx, r.dev, "turn-2")
	if strings.Contains(note, "thread:a") || !strings.Contains(note, "tok-b") || !strings.Contains(note, "tok-c") {
		t.Fatalf("second block\n%s", note)
	}
	if _, err := r.hub.Reply(r.ctx, r.dev, Answer{Token: "tok-c", Answers: []string{"yes"}, By: Evidence{Via: ViaDashboard}}); err != nil {
		t.Fatal(err)
	}
	// a stop withdraws the thread's open question
	r.push(stopped("b", "Deploy"))
	r.eventually("withdrawn", func() bool { _, err := r.hub.Question(r.ctx, r.dev, "tok-b"); return err != nil })
	r.eventually("delivered", func() bool {
		for _, it := range r.inbox() {
			if it.RefID == "b" && it.Event == EventStopped && it.State == stateDelivered {
				return true
			}
		}
		return false
	})
	note, _ = r.hub.Inbox(r.ctx, r.dev, "turn-3")
	if strings.Contains(note, "tok-b") || strings.Contains(note, "tok-c") || !strings.Contains(note, "thread:b") {
		t.Fatalf("third block\n%s", note)
	}
	if note, _ = r.hub.Inbox(r.ctx, r.dev, "turn-4"); note != "" {
		t.Fatalf("fourth block\n%s", note)
	}
}

// a withdrawal alone ends the ref's open question and reaches no device
func TestWithdrawAlone(t *testing.T) {
	r := newRig(t)
	r.subscribe("t3code:work:*", "", "")
	r.push(asked("c", "Pick", "tok-c", QuestionClarify))
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	before := r.count()
	r.mod.stream("work") <- Update{Topic: thread("c"), Ref: Ref{ID: "c"}, Withdraw: true}
	r.eventually("withdrawn", func() bool { _, err := r.hub.Question(r.ctx, r.dev, "tok-c"); return err != nil })
	if r.count() != before {
		t.Fatal("the withdrawal was stored as an update")
	}
	r.quiet(300*time.Millisecond, 1)
}

// an answer must fit its question before it reaches the module, with its ref
// and evidence
func TestReplyFits(t *testing.T) {
	r := newRig(t)
	r.subscribe("t3code:work:*", "", "")
	r.push(asked("c", "Pick", "tok-c", QuestionClarify))
	r.push(asked("d", "Deploy", "tok-d", QuestionApproval))
	dash := Evidence{Via: ViaDashboard}
	for _, a := range []Answer{
		{Token: "tok-c", Decision: DecisionOnce, By: dash},
		{Token: "tok-c", Answers: []string{"maybe"}, By: dash},
		{Token: "tok-c", Answers: []string{"yes", "no"}, By: dash},
		{Token: "tok-c", Message: "free", By: dash},
		{Token: "tok-c", By: dash},
		{Token: "tok-d", Answers: []string{"yes"}, By: dash},
		{Token: "tok-d", Decision: "always", By: dash},
		{Token: "tok-d", Decision: DecisionOnce, By: Evidence{Via: "mail"}},
	} {
		if _, err := r.hub.Reply(r.ctx, r.dev, a); err == nil {
			t.Fatalf("%+v taken", a)
		}
	}
	if _, err := r.hub.Reply(r.ctx, r.dev, Answer{Token: "nope", Decision: DecisionOnce, By: dash}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if len(r.mod.answered()) != 0 {
		t.Fatal("the module got a refused answer")
	}
	if _, err := r.hub.Reply(r.ctx, r.dev, Answer{Token: "tok-d", Decision: DecisionSession, By: dash}); err != nil {
		t.Fatal(err)
	}
	got := r.mod.answered()
	if len(got) != 1 || got[0].Ref != (Ref{Instance: "work", ID: "d"}) || got[0].A.Decision != DecisionSession || got[0].A.By.Via != ViaDashboard {
		t.Fatalf("answers %+v", got)
	}
	if _, err := r.hub.Reply(r.ctx, r.dev, Answer{Token: "tok-d", Decision: DecisionOnce, By: dash}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answered twice: %v", err)
	}
}

// subscribe refuses what the module does not have or emit, and a device's
// default delivery comes from its setting
func TestSubscribeChecks(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		req  Request
		want error
	}{
		{Request{Topic: "t3code:work:bad:x"}, ErrTopic},
		{Request{Topic: "t3code:work"}, ErrTopic},
		{Request{Topic: "hermes:work:*"}, ErrUnknownModule},
		{Request{Topic: "t3code:other:*"}, ErrInstance},
		{Request{Topic: "t3code:work:*", Events: []string{EventDone}}, ErrEvent},
		{Request{Topic: "t3code:work:*", Delivery: "mail"}, ErrInvalid},
		{Request{Topic: "t3code:work:*", Until: "forever"}, ErrInvalid},
	} {
		c.req.Device, c.req.CreatedBy = r.dev, ByAPI
		if _, err := r.hub.Subscribe(r.ctx, c.req); !errors.Is(err, c.want) {
			t.Fatalf("%+v: %v, want %v", c.req, err, c.want)
		}
	}
	if _, err := r.hub.Subscribe(r.ctx, Request{Topic: "t3code:work:*", Device: "00000000-0000-0000-0000-000000000009", CreatedBy: ByAPI}); !errors.Is(err, ErrDevice) {
		t.Fatalf("unknown device: %v", err)
	}
	r.settings(func(v *settings.Values) { v.SubscriptionDelivery = settings.SubscriptionAgent })
	s := r.subscribe("t3code:work:*", "", "")
	if s.Delivery != DeliveryAgent || s.Until != UntilUnsubscribed || len(s.Events) != 4 {
		t.Fatalf("defaults %+v", s)
	}
	// the same topic again replaces it
	s2 := r.subscribe("t3code:work:*", DeliverySpeaker, "", EventStopped)
	subs, _ := r.hub.List(r.ctx, r.dev)
	if len(subs) != 1 || subs[0].ID != s.ID || subs[0].Delivery != DeliverySpeaker || s2.Events[0] != EventStopped {
		t.Fatalf("replaced %+v", subs)
	}
	if _, err := r.hub.UnsubscribeTopic(r.ctx, r.dev, "t3code:work:*"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hub.Unsubscribe(r.ctx, s.ID, r.dev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("twice: %v", err)
	}
}

// a module registered while the hub runs is followed once a subscription
// names its instance; a stream the module closes opens again
func TestModuleRegisteredLater(t *testing.T) {
	r := newRig(t)
	ha := newFakeModule()
	ha.name = "homeassistant"
	if _, err := r.hub.Subscribe(r.ctx, Request{Topic: "homeassistant:home:*", Device: r.dev, CreatedBy: ByAPI}); !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("before register: %v", err)
	}
	if err := r.hub.Registry().Register(ha); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.Registry().Register(ha); err == nil {
		t.Fatal("registered twice")
	}
	if _, err := r.hub.Subscribe(r.ctx, Request{Topic: "homeassistant:home:*", Device: r.dev, CreatedBy: ByAPI}); err != nil {
		t.Fatal(err)
	}
	ha.stream("home") <- Update{Topic: NewTopic("homeassistant", "home", "entity", "lock.front"), Event: EventChanged, Title: "Front door"}
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	close(ha.stream("home"))
	ha.mu.Lock()
	delete(ha.streams, "home")
	ha.mu.Unlock()
	r.eventually("pump ended", func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return !r.hub.pumps[instanceKey{"homeassistant", "home"}]
	})
	r.hub.sweep(r.ctx)
	ha.stream("home") <- Update{Topic: NewTopic("homeassistant", "home", "entity", "lock.front"), Event: EventChanged, Title: "Front door"}
	r.eventually("spoken again", func() bool { return len(r.voice.spoken()) == 2 })
}

// updates within the window go as one announcement without anything holding
// them
func TestWindowGathers(t *testing.T) {
	r := newRig(t)
	r.hub.mu.Lock()
	r.hub.lim.window = 2 * time.Second
	r.hub.mu.Unlock()
	r.subscribe("t3code:work:*", "", "")
	r.push(stopped("a", "A"))
	r.push(stopped("b", "B"))
	r.eventually("spoken", func() bool { return len(r.voice.spoken()) == 1 })
	if got := r.voice.spoken()[0]; got != "Two threads stopped." {
		t.Fatalf("said %q", got)
	}
}
