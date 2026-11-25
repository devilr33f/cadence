package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	// a client frame is at most maxClientFrame bytes, and at most
	// maxClientFrames arrive in a second; past either the feed closes with 1008
	maxClientFrame  = 4 << 10
	maxClientFrames = 10
)

// selection is what one feed writes: the events of its topics, of one device
// when device is set. An admin feed that never subscribed writes every topic
// but the opt-in ones, and the types no topic maps, as the feed did before
// topics.
type selection struct {
	topics     map[Topic]bool
	device     string
	everything bool
}

func (s *selection) wants(e Event) bool {
	if s.device != "" && e.DeviceID != s.device {
		return false
	}
	if t, ok := topicOf[e.Type]; ok {
		return s.topics[t]
	}
	return s.everything
}

// defaultSelection is every topic the scopes allow but the opt-in ones.
func defaultSelection(scopes auth.Scopes, device string) *selection {
	sel := &selection{topics: map[Topic]bool{}, device: device, everything: scopes.Allows(auth.ScopeAdmin)}
	for _, t := range Topics {
		if t.allowed(scopes) && !optIn[t] {
			sel.topics[t] = true
		}
	}
	return sel
}

// subscribeFrame is a client's subscription: it replaces the one before; a
// deviceId, "" for every device, overrides the query's.
type subscribeFrame struct {
	Subscribe *[]string `json:"subscribe"`
	DeviceID  *string   `json:"deviceId"`
}

// subscribedFrame answers a subscription with what the feed now writes.
type subscribedFrame struct {
	Type     string  `json:"type"`
	Topics   []Topic `json:"topics"`
	DeviceID string  `json:"deviceId,omitempty"`
}

// refusalFrame refuses a client frame; the connection stays.
type refusalFrame struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// a detail echoes a topic only when it is a token, as the api's error model
var topicToken = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// parseSubscribe reads a client frame into a new selection, or the refusal to
// answer; forbidden is the topic the scopes do not allow, for the log.
func parseSubscribe(b []byte, scopes auth.Scopes, queryDevice string) (sel *selection, refusal *refusalFrame, forbidden Topic) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f subscribeFrame
	if err := dec.Decode(&f); err != nil || f.Subscribe == nil || dec.More() {
		return nil, &refusalFrame{Error: "invalid"}, ""
	}
	sel = &selection{topics: map[Topic]bool{}, device: queryDevice}
	if f.DeviceID != nil {
		sel.device = *f.DeviceID
	}
	for _, name := range *f.Subscribe {
		if _, ok := topicScopes[Topic(name)]; !ok {
			r := &refusalFrame{Error: "topic_unknown"}
			if topicToken.MatchString(name) {
				r.Detail = name
			}
			return nil, r, ""
		}
	}
	for _, name := range *f.Subscribe {
		t := Topic(name)
		if !t.allowed(scopes) {
			return nil, &refusalFrame{Error: "forbidden", Detail: name}, t
		}
		sel.topics[t] = true
	}
	return sel, nil, ""
}

func (s *selection) answer() subscribedFrame {
	a := subscribedFrame{Type: "subscribed", Topics: []Topic{}, DeviceID: s.device}
	for _, t := range Topics {
		if s.topics[t] {
			a.Topics = append(a.Topics, t)
		}
	}
	return a
}

// errViolation ends the read after the feed closed for a violation
var errViolation = errors.New("events: client frame limit")

// frameLimit counts a client's frames, control frames too.
type frameLimit struct {
	recent [maxClientFrames]time.Time
	n      int
}

// over records a frame and reports whether it is more than maxClientFrames
// within a second.
func (l *frameLimit) over(now time.Time) bool {
	i := l.n % maxClientFrames
	over := l.n >= maxClientFrames && now.Sub(l.recent[i]) < time.Second
	l.recent[i], l.n = now, l.n+1
	return over
}
