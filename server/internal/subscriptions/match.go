package subscriptions

import (
	"slices"
	"time"
)

// Subscription is one device's subscription to a topic.
type Subscription struct {
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	Events    []string  `json:"events" nullable:"false"`
	Delivery  string    `json:"delivery" enum:"speaker,agent,mute"`
	Device    string    `json:"device"`
	Until     string    `json:"until" enum:"unsubscribed,next_stop"`
	CreatedBy string    `json:"createdBy" enum:"voice,api"`
	CreatedAt time.Time `json:"createdAt"`

	topic Topic
}

// Match is the subscription that decides how an update reaches one device.
type Match struct {
	Device string
	Sub    Subscription
}

// match picks, per device, the subscription that decides an update: the
// most specific of its topics that cover the update's (Topic or one of
// Also), the newest of equals. The winner decides alone: one that is mute,
// or does not ask for the event, leaves the device out, whatever wider
// subscriptions say. Devices come in the order of their first subscription.
func match(subs []Subscription, u Update) []Match {
	type best struct {
		sub  Subscription
		spec int
	}
	byDev := map[string]*best{}
	var order []string
	for _, s := range subs {
		spec := -1
		for _, c := range append([]Topic{u.Topic}, u.Also...) {
			if s.topic.Matches(c) {
				spec = max(spec, s.topic.Specificity())
			}
		}
		if spec < 0 {
			continue
		}
		b := byDev[s.Device]
		switch {
		case b == nil:
			byDev[s.Device] = &best{s, spec}
			order = append(order, s.Device)
		case spec > b.spec || spec == b.spec && s.CreatedAt.After(b.sub.CreatedAt):
			*b = best{s, spec}
		}
	}
	var out []Match
	for _, d := range order {
		s := byDev[d].sub
		if s.Delivery == DeliveryMute || !slices.Contains(s.Events, u.Event) {
			continue
		}
		out = append(out, Match{Device: d, Sub: s})
	}
	return out
}
