package t3code_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

type sourceRig struct {
	t      *testing.T
	fake   *t3codetest.Server
	client *t3code.Client
	src    *t3code.Source
	ups    <-chan subscriptions.Update

	mu     sync.Mutex
	topics []subscriptions.Topic
}

func newSourceRig(t *testing.T, topics ...string) *sourceRig {
	t.Helper()
	fake := t3codetest.New(t3codetest.Config{Token: testToken})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: testToken, HTTP: srv.Client(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t3code.FastTimings(c, 50*time.Millisecond, time.Second)
	r := &sourceRig{t: t, fake: fake, client: c}
	for _, s := range topics {
		r.topics = append(r.topics, subscriptions.MustTopic(s))
	}
	r.src = t3code.NewSource(t3code.SourceDeps{
		Followed: func(_ context.Context, inst string) ([]subscriptions.Topic, error) {
			if inst != "work" {
				t.Errorf("followed %q", inst)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]subscriptions.Topic(nil), r.topics...), nil
		},
		Log: quiet, Every: 50 * time.Millisecond, Linger: 200 * time.Millisecond,
	})
	changed := make(chan struct{}, 4)
	r.src.OnChange(func() { changed <- struct{}{} })
	r.src.Apply([]t3code.Want{{Instance: "work", Client: c}})
	t.Cleanup(r.src.Close)
	select {
	case <-changed:
	default:
		t.Fatal("no change heard")
	}
	r.ups = r.src.Updates("work")
	waitFor(t, "synced", func() bool { _, _, ok := r.src.Shell("work"); return ok })
	return r
}

// next is the next update of event ev (and thread, when set), skipping
// others.
func (r *sourceRig) next(ev, thread string) subscriptions.Update {
	r.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-r.ups:
			if u.Event == ev && (thread == "" || u.Ref.ID == thread) {
				return u
			}
		case <-deadline:
			r.t.Fatalf("no %s update", ev)
		}
	}
}

func (r *sourceRig) commands(typ string) []t3codetest.Command {
	var out []t3codetest.Command
	for _, c := range r.fake.Commands() {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

var hubTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// a question reaches the hub with its thread and project topics and a token
// in the hub's charset; the answer by option id reaches T3 keyed by
// question id, and the thread's stop and the request's resolution follow
func TestSourceQuestionAndAnswer(t *testing.T) {
	r := newSourceRig(t, "t3code:work:*")
	if !r.fake.Start(t3codetest.ThreadAsk, "ask which color") {
		t.Fatal("not started")
	}
	u := r.next(subscriptions.EventQuestion, t3codetest.ThreadAsk)
	q := u.Question
	if u.Topic.String() != "t3code:work:thread:thread-ask" || len(u.Also) != 1 ||
		u.Also[0].String() != "t3code:work:project:project-cadence:thread:thread-ask" || u.Kind != "thread" || u.Title != "ask" {
		t.Fatalf("update %+v", u)
	}
	if q == nil || q.Kind != subscriptions.QuestionClarify || !hubTokenRe.MatchString(q.Token) || q.Text != "Which color should the button be?" ||
		len(q.Options) != 2 || q.Options[1] != (subscriptions.Option{ID: "2", Label: "Blue"}) || !q.AllowCustom || q.Multi {
		t.Fatalf("question %+v", q)
	}
	ctx := context.Background()
	ref := subscriptions.Ref{Instance: "work", ID: t3codetest.ThreadAsk}
	if err := r.src.Answer(ctx, ref, subscriptions.Answer{Token: "AAAAAAAAAAAAAAAAAAAAAA", Answers: []string{"2"}}); !errors.Is(err, subscriptions.ErrNoAnswer) {
		t.Fatalf("other token %v", err)
	}
	if err := r.src.Answer(ctx, ref, subscriptions.Answer{Token: q.Token, Answers: []string{"2"}}); err != nil {
		t.Fatal(err)
	}
	got := r.commands("runtime-request.respond")
	if len(got) != 1 {
		t.Fatalf("commands %+v", r.fake.Commands())
	}
	var sent struct {
		Answers map[string]any `json:"answers"`
	}
	if json.Unmarshal(got[0].Raw, &sent) != nil || sent.Answers[t3codetest.QuestionID] != "Blue" {
		t.Fatalf("sent %s", got[0].Raw)
	}
	stop := r.next(subscriptions.EventStopped, t3codetest.ThreadAsk)
	if stop.Text != "Answered" && stop.Text != "" {
		t.Fatalf("stop %+v", stop)
	}
	if err := r.src.Answer(ctx, ref, subscriptions.Answer{Token: q.Token, Answers: []string{"1"}}); !errors.Is(err, subscriptions.ErrNoAnswer) {
		t.Fatalf("answered twice %v", err)
	}
}

// an approval maps once to accept; a run that fails tells one error
func TestSourceApprovalAndError(t *testing.T) {
	r := newSourceRig(t, "t3code:work:project:project-cadence:*")
	r.fake.Start(t3codetest.ThreadApprove, "approve the deploy")
	u := r.next(subscriptions.EventApproval, t3codetest.ThreadApprove)
	if u.Question == nil || u.Question.Kind != subscriptions.QuestionApproval || u.Question.Text != "Run make test? (checks the change)" {
		t.Fatalf("approval %+v", u.Question)
	}
	ref := subscriptions.Ref{Instance: "work", ID: t3codetest.ThreadApprove}
	if err := r.src.Answer(context.Background(), ref, subscriptions.Answer{Token: u.Question.Token, Decision: subscriptions.DecisionOnce}); err != nil {
		t.Fatal(err)
	}
	got := r.commands("runtime-request.respond")
	if len(got) != 1 || !strings.Contains(string(got[0].Raw), `"decision":"accept"`) {
		t.Fatalf("commands %+v", got)
	}
	r.next(subscriptions.EventStopped, t3codetest.ThreadApprove)

	r.fake.Start(t3codetest.ThreadFail, "fail now")
	e := r.next(subscriptions.EventError, t3codetest.ThreadFail)
	r.next(subscriptions.EventStopped, t3codetest.ThreadFail)
	if e.Kind != "thread" {
		t.Fatalf("error %+v", e)
	}
	select {
	case u := <-r.ups:
		if u.Event == subscriptions.EventError {
			t.Fatalf("a second error %+v", u)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

// without a subscription that could match, no thread stream is opened: the
// shell alone tells, the question read from the thread's projection
func TestSourceShellAloneWithoutSubscriptions(t *testing.T) {
	r := newSourceRig(t)
	r.fake.Start(t3codetest.ThreadAsk, "ask it")
	u := r.next(subscriptions.EventQuestion, t3codetest.ThreadAsk)
	if u.Question == nil || len(u.Question.Options) != 2 {
		t.Fatalf("question %+v", u.Question)
	}
	for _, s := range r.fake.Subscribes() {
		if s.Method == t3code.MethodSubscribeThread {
			t.Fatalf("followed %s", s.ThreadID)
		}
	}
}

// a thread launched after the subscription is followed too, and goes back to
// the shell alone once it was idle for the linger
func TestSourceFollowsANewThread(t *testing.T) {
	r := newSourceRig(t, "t3code:work:*")
	res, err := r.client.LaunchThread(context.Background(), t3code.LaunchInput{
		ProjectID: t3codetest.ProjectID, Title: "New work", Text: "ask about it",
		ModelSelection: t3code.ModelSelection{InstanceID: "claude", Model: "claude-opus-5-5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := r.next(subscriptions.EventQuestion, res.ThreadID)
	if u.Title != "New work" {
		t.Fatalf("update %+v", u)
	}
	waitFor(t, "followed", func() bool {
		for _, s := range r.fake.Subscribes() {
			if s.Method == t3code.MethodSubscribeThread && s.ThreadID == res.ThreadID {
				return true
			}
		}
		return false
	})
	waitFor(t, "status", func() bool {
		st, ok := r.src.Status("work")
		return ok && st.Connected && st.Synced && st.Threads == 6 && st.Pending == 1 && st.Followed == 1
	})
	if err := r.src.Answer(context.Background(), subscriptions.Ref{Instance: "work", ID: res.ThreadID},
		subscriptions.Answer{Token: u.Question.Token, Message: "green please"}); err != nil {
		t.Fatal(err)
	}
	r.next(subscriptions.EventStopped, res.ThreadID)
	waitFor(t, "unfollowed", func() bool { st, _ := r.src.Status("work"); return st.Followed == 0 })
}

// the module's topics, lists and the instances it takes
func TestSourceValidateAndList(t *testing.T) {
	r := newSourceRig(t)
	for _, path := range [][]string{nil, {"thread"}, {"thread", "a"}, {"project", "p"}, {"project", "p", "thread", "a"}} {
		if err := r.src.Validate("work", path); err != nil {
			t.Errorf("%v refused: %v", path, err)
		}
	}
	for _, path := range [][]string{{"run"}, {"thread", "a b"}, {"project", "p", "run", "a"}, {"thread", "a", "b"}} {
		if err := r.src.Validate("work", path); err == nil {
			t.Errorf("%v taken", path)
		}
	}
	if err := r.src.Validate("home", nil); !errors.Is(err, t3code.ErrNotConfigured) {
		t.Errorf("unconfigured %v", err)
	}
	all, err := r.src.List(context.Background(), "work", nil)
	if err != nil || len(all) != 6 || all[0].Topic != "t3code:work:project:project-cadence:*" || all[0].Title != "Cadence" {
		t.Fatalf("list %+v %v", all, err)
	}
	one, _ := r.src.List(context.Background(), "work", []string{"thread", t3codetest.ThreadAsk})
	if len(one) != 1 || one[0].Topic != "t3code:work:thread:thread-ask" || one[0].Status != "idle" || one[0].Kind != "thread" {
		t.Fatalf("one %+v", one)
	}
	if got, _ := r.src.List(context.Background(), "work", []string{"project", "other"}); len(got) != 0 {
		t.Fatalf("other project %+v", got)
	}
	// stopping the instance closes its stream
	r.src.Apply(nil)
	select {
	case _, ok := <-r.ups:
		if ok {
			t.Fatal("an update after the stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream not closed")
	}
	if r.src.Updates("work") != nil {
		t.Fatal("a stream for a stopped instance")
	}
}

// the hub's token is stable and in its charset; the T3 token is not
func TestHubToken(t *testing.T) {
	a, b := t3code.HubToken("request-1.abcdef012345"), t3code.HubToken("request-1.abcdef012345")
	if a != b || !hubTokenRe.MatchString(a) || t3code.HubToken("request-1.abcdef012346") == a {
		t.Fatalf("%q %q", a, b)
	}
}
