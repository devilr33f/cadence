package subscriptions

import (
	"testing"
	"time"
)

func sub(id, device, topic, delivery string, at int, events ...string) Subscription {
	if len(events) == 0 {
		events = []string{EventStopped, EventQuestion}
	}
	return Subscription{
		ID: id, Device: device, Topic: topic, Delivery: delivery, Events: events,
		CreatedAt: time.Unix(int64(at), 0), topic: MustTopic(topic),
	}
}

func matched(ms []Match) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		out[m.Device] = m.Sub.ID
	}
	return out
}

func TestMatchMostSpecificWins(t *testing.T) {
	u := Update{Topic: NewTopic("t3code", "work", "thread", "a"), Event: EventStopped}
	subs := []Subscription{
		sub("wide", "d1", "t3code:work:*", DeliverySpeaker, 1),
		sub("exact", "d1", "t3code:work:thread:a", DeliveryAgent, 0),
		sub("other", "d2", "t3code:work:*", DeliverySpeaker, 1),
		sub("elsewhere", "d3", "t3code:work:thread:b", DeliverySpeaker, 1),
	}
	got := matched(match(subs, u))
	if len(got) != 2 || got["d1"] != "exact" || got["d2"] != "other" {
		t.Fatalf("%v", got)
	}
}

// a mute on a narrower topic silences it inside a wider one; a wider mute
// leaves a narrower subscription speaking
func TestMatchMute(t *testing.T) {
	a := Update{Topic: NewTopic("t3code", "work", "thread", "a"), Event: EventStopped}
	b := Update{Topic: NewTopic("t3code", "work", "thread", "b"), Event: EventStopped}
	subs := []Subscription{
		sub("wide", "d1", "t3code:work:*", DeliverySpeaker, 1),
		sub("mute", "d1", "t3code:work:thread:a", DeliveryMute, 2),
	}
	if got := matched(match(subs, a)); len(got) != 0 {
		t.Fatalf("muted thread %v", got)
	}
	if got := matched(match(subs, b)); got["d1"] != "wide" {
		t.Fatalf("other thread %v", got)
	}
	subs = []Subscription{
		sub("mute", "d1", "t3code:work:*", DeliveryMute, 2),
		sub("one", "d1", "t3code:work:thread:a", DeliverySpeaker, 1),
	}
	if got := matched(match(subs, a)); got["d1"] != "one" {
		t.Fatalf("narrower in a muted wide %v", got)
	}
}

// equals: the newest wins, and the winner decides alone, even when it does
// not ask for the event
func TestMatchTiesAndEvents(t *testing.T) {
	u := Update{Topic: NewTopic("t3code", "work", "thread", "a"), Event: EventQuestion}
	// two subscriptions of one topic cannot both be stored; the project path
	// below (Also) gives an equal one
	also := Update{Topic: u.Topic, Also: []Topic{NewTopic("t3code", "work", "project", "p", "thread", "a")}, Event: EventQuestion}
	subs := []Subscription{
		sub("old", "d1", "t3code:work:thread:*", DeliverySpeaker, 1),
		sub("new", "d1", "t3code:work:project:*", DeliveryAgent, 2),
	}
	if got := matched(match(subs, also)); got["d1"] != "new" {
		t.Fatalf("tie %v", got)
	}
	subs = []Subscription{
		sub("wide", "d1", "t3code:work:*", DeliverySpeaker, 1, EventQuestion),
		sub("narrow", "d1", "t3code:work:thread:a", DeliverySpeaker, 0, EventStopped),
	}
	if got := matched(match(subs, u)); len(got) != 0 {
		t.Fatalf("the narrower winner does not ask for questions: %v", got)
	}
	// Also is matched as the topic is: a project subscription covers a thread
	subs = []Subscription{sub("project", "d1", "t3code:work:project:p:*", DeliverySpeaker, 1)}
	if got := matched(match(subs, u)); len(got) != 0 {
		t.Fatalf("no also %v", got)
	}
	if got := matched(match(subs, also)); got["d1"] != "project" {
		t.Fatalf("also %v", got)
	}
}
