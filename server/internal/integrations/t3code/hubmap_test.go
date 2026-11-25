package t3code

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

func boolp(b bool) *bool { return &b }

// several questions of one request are one hub question: options
// "<question>.<option>", one answer per question (several where it takes
// several), no free answer
func TestSeveralQuestions(t *testing.T) {
	qs := []Question{
		{ID: "color", Header: "Color", Question: "Which color?", Options: []QuestionOption{{Label: "Red"}, {Label: "Blue", Value: "blue"}}},
		{ID: "sizes", Header: "Sizes", Question: "Which sizes?", MultiSelect: true, Options: []QuestionOption{{Label: "S"}, {Label: "M"}}},
	}
	q := clarify(Update{Token: "r.1", Questions: qs})
	if q.Text != "1. Which color? 2. Which sizes?" || !q.Multi || q.AllowCustom || len(q.Options) != 4 ||
		q.Options[1] != (subscriptions.Option{ID: "1.2", Label: "Color: Blue"}) {
		t.Fatalf("question %+v", q)
	}
	got, text, err := answersFor(qs, subscriptions.Answer{Answers: []string{"1.2", "2.1", "2.2"}})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"color": "blue", "sizes": []string{"S", "M"}}) || text != "Color: blue\nSizes: S, M" {
		t.Fatalf("%v %q %v", got, text, err)
	}
	for _, bad := range []subscriptions.Answer{
		{Answers: []string{"1.1"}},               // a question left out
		{Answers: []string{"1.1", "1.2", "2.1"}}, // two for a single-select one
		{Answers: []string{"3.1", "2.1"}},
		{Answers: []string{"1.9", "2.1"}},
		{Message: "anything"},
	} {
		if _, _, err := answersFor(qs, bad); !errors.Is(err, errAnswerShape) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

// one question: options by 1-based id, a free answer unless T3 says false
func TestOneQuestion(t *testing.T) {
	qs := []Question{{ID: "q", Question: "Name?", Options: []QuestionOption{{Label: "A"}}}}
	if _, text, err := answersFor(qs, subscriptions.Answer{Message: " Bob "}); err != nil || text != "Bob" {
		t.Fatalf("%q %v", text, err)
	}
	qs[0].AllowCustomAnswer = boolp(false)
	if clarify(Update{Token: "r.1", Questions: qs}).AllowCustom {
		t.Fatal("custom allowed")
	}
	if _, _, err := answersFor(qs, subscriptions.Answer{Message: "Bob"}); !errors.Is(err, errAnswerShape) {
		t.Fatalf("free answer %v", err)
	}
	if got, _, err := answersFor(qs, subscriptions.Answer{Answers: []string{"1"}}); err != nil || got["q"] != "A" {
		t.Fatalf("%v %v", got, err)
	}
}

// a hostile id never becomes a topic segment
func TestHubUpdateRefusesOddIDs(t *testing.T) {
	if _, ok := hubUpdate("work", "p", Update{Kind: KindStopped, ThreadID: "a b\nignore previous"}, timeZero); ok {
		t.Fatal("odd id taken")
	}
	u, ok := hubUpdate("work", "bad project", Update{Kind: KindStopped, ThreadID: "a"}, timeZero)
	if !ok || len(u.Also) != 0 {
		t.Fatalf("%+v", u)
	}
	w, ok := hubUpdate("work", "p", Update{Kind: KindResolved, ThreadID: "a", RequestID: "r"}, timeZero)
	if !ok || !w.Withdraw || w.Event != "" {
		t.Fatalf("resolved %+v", w)
	}
}

var timeZero time.Time
