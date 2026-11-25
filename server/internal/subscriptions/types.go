// Package subscriptions is the hub that follows work in outside systems (a
// T3 Code thread, later a Hermes task or a Home Assistant entity) for the
// devices: a module of each system emits updates on topics, a device's
// subscriptions pick the ones it hears, and the hub speaks them, runs an
// event turn on them or leaves them for the device's next woken turn.
//
// What an outside system wrote (titles, texts, questions) is user content:
// the DB, the authenticated API and the live feed's admin topic only, never a
// log; logs carry ids, codes, counts and durations.
package subscriptions

import (
	"context"
	"errors"
	"time"
)

// The shared event names; each module declares the ones it emits.
const (
	EventStopped  = "stopped"
	EventQuestion = "question"
	EventApproval = "approval"
	EventError    = "error"
	EventDone     = "done"
	EventChanged  = "changed"
)

// Events are every shared event, in the order a list shows them.
var Events = []string{EventStopped, EventQuestion, EventApproval, EventError, EventDone, EventChanged}

// How a subscription's updates reach its device.
const (
	DeliverySpeaker = "speaker"
	DeliveryAgent   = "agent"
	DeliveryMute    = "mute"
)

// How long a subscription lasts.
const (
	UntilUnsubscribed = "unsubscribed"
	UntilNextStop     = "next_stop"
)

// Who created a subscription.
const (
	ByVoice = "voice"
	ByAPI   = "api"
)

// The kinds of question an update may ask.
const (
	QuestionClarify  = "clarify"
	QuestionApproval = "approval"
)

// The answers to an approval question.
const (
	DecisionOnce    = "once"
	DecisionSession = "session"
	DecisionDeny    = "deny"
)

// Who gave an answer: a voice in a woken turn, the dashboard or a screen.
const (
	ViaVoice     = "voice"
	ViaDashboard = "dashboard"
	ViaScreen    = "screen"
)

// Subscribable is a module whose instances can be followed. Module is its
// provider catalog name, Events the shared events it emits. Validate refuses
// a path its topics never have. List shows what can be followed under a path
// prefix (threads, projects), for the tools, the dashboard and a TV. Updates
// is the instance's stream, opened once per instance while subscriptions name
// it and read until the module closes it (a later call opens it again).
// Answer replies to a question an update asked, by the question's token; a
// module that asks none returns ErrNoAnswer.
type Subscribable interface {
	Module() string
	Events() []string
	Validate(instance string, path []string) error
	List(ctx context.Context, instance string, prefix []string) ([]Entry, error)
	Updates(instance string) <-chan Update
	Answer(ctx context.Context, ref Ref, a Answer) error
}

// ErrNoAnswer is a module that takes no answers, or a question it no longer
// asks.
var ErrNoAnswer = errors.New("subscriptions: no such question")

// Ref names the thing an update is about inside its module's instance (a
// thread), so an answer reaches it; ID is the module's own.
type Ref struct {
	Instance string `json:"instance"`
	ID       string `json:"id"`
}

// Update is one change a module saw. Topic is concrete (no wildcard); Also
// are other topics naming the same thing (a thread's place under its
// project), matched as Topic is. Kind names what changed for the spoken
// templates ("thread", "task"). Title and Text are the outside system's
// words. Question is set on a question or approval event. Withdraw ends the
// Ref's open questions (but Question's own): the thread moved on; with no
// Event it does only that and reaches no device.
type Update struct {
	Topic    Topic     `json:"-"`
	Also     []Topic   `json:"-"`
	Event    string    `json:"event"`
	Kind     string    `json:"kind,omitempty"`
	Title    string    `json:"title,omitempty"`
	Text     string    `json:"text,omitempty"`
	Question *Question `json:"question,omitempty"`
	Ref      Ref       `json:"ref"`
	Withdraw bool      `json:"-"`
	At       time.Time `json:"at"`
}

// Question is what an update asks. Token ties an answer to this exact prompt.
type Question struct {
	Kind        string   `json:"kind" enum:"clarify,approval"`
	Token       string   `json:"token"`
	Text        string   `json:"text"`
	Options     []Option `json:"options,omitempty"`
	Multi       bool     `json:"multi,omitempty"`
	AllowCustom bool     `json:"allowCustom,omitempty"`
}

// Option is one answer a question offers.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Answer replies to a question by its token: Answers are option ids, Message
// a free answer, Decision an approval's once, session or deny. By says who
// gave it.
type Answer struct {
	Token    string
	Answers  []string
	Message  string
	Decision string
	By       Evidence
}

// Evidence is who answered: Via voice (with the device and the user's own
// words of the turn), dashboard or screen.
type Evidence struct {
	Via    string `json:"via"`
	Device string `json:"device,omitempty"`
	Words  string `json:"words,omitempty"`
}

// Entry is one thing List shows: its topic, title and kind, and its status
// as the module says it.
type Entry struct {
	Topic   string    `json:"topic"`
	Kind    string    `json:"kind,omitempty"`
	Title   string    `json:"title,omitempty"`
	Status  string    `json:"status,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
}

// Busy reports whether a device is busy (a call, a screenshare, do not
// disturb), which holds unasked speech; reason is a code. Presence implements
// it; a nil Busy is never busy. Its change notification calls
// Hub.BusyChanged, so held updates go once the device is free.
type Busy interface {
	Busy(ctx context.Context, deviceID string) (busy bool, reason string)
}

// AfterBusy is what becomes of the updates a busy device held, by its
// afterBusy setting: AfterBusyAnnounce speaks them once it is free,
// AfterBusyInbox leaves them for its next woken turn.
type AfterBusy func(ctx context.Context, deviceID string) string

const (
	AfterBusyAnnounce = "announce"
	AfterBusyInbox    = "inbox"
)
