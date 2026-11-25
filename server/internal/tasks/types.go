// Package tasks stores background tasks and runs their kinds (Hermes and
// friends) behind one manager.
//
// Two notions of "done": Task.Terminal is done, failed or cancelled, which never
// change again. A task that is unknown (its kind cannot confirm it) is not
// Terminal, so a kind may still resolve it, but it counts as finished: it has
// finished_at, so retention and Unreported cover it.
package tasks

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	StatusQueued       = "queued"
	StatusRunning      = "running"
	StatusWaitingInput = "waiting_input"
	StatusDone         = "done"
	StatusFailed       = "failed"
	StatusCancelled    = "cancelled"
	StatusUnknown      = "unknown"

	QuestionClarify  = "clarify"
	QuestionApproval = "approval"

	// Retention is how long a finished task stays.
	Retention = 30 * 24 * time.Hour

	MaxTitle        = 120
	MaxProgress     = 200
	MaxResult       = 64 << 10
	MaxQuestionText = 1000
	MaxOptions      = 8
	MaxOption       = 100
	// MaxWords caps the user's words an approval keeps as its evidence, and
	// WordsRetention is how long it keeps them: as long as a transcript (D8).
	MaxWords       = 500
	WordsRetention = 14 * 24 * time.Hour

	// ReportWindow is how long a finished task nobody reported stays in view.
	ReportWindow = time.Hour

	ViaVoice     = "voice"
	ViaDashboard = "dashboard"
	// ViaScreen is a press on a screen's remote, its confirm accepted (W0-C
	// item 9b): the evidence names the screen
	ViaScreen = "screen"
)

var (
	ErrNotFound    = errors.New("tasks: not found")
	ErrInvalid     = errors.New("tasks: invalid")
	ErrTransition  = errors.New("tasks: invalid transition")
	ErrUnknownKind = errors.New("tasks: unknown kind")
	ErrNotActive   = errors.New("tasks: task is not active")
	ErrStartFailed = errors.New("tasks: start failed")
	ErrNoApproval  = errors.New("tasks: no approval pending")
	ErrStale       = errors.New("tasks: the question changed")
	// ErrInspect is a kind that failed to show a task; it carries no kind text.
	ErrInspect = errors.New("tasks: inspect failed")
)

// Question is what a waiting task asks.
// Token is the kind's opaque name for the exact prompt asked: an answer
// carries it back, so it only ever lands on the prompt its giver saw.
type Question struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	Token   string   `json:"token,omitempty"`
}

// Evidence is who answered an approval question: the dashboard, a voice
// session on a device with the user's own words, or a screen's remote. It is kept on the task's
// answered event, in the DB only, never in a log.
type Evidence struct {
	Via    string `json:"via"`
	Device string `json:"device,omitempty"`
	Words  string `json:"words,omitempty"`
}

// answerOf is the answered event's payload, false when the evidence does not
// say who answered: a voice answer needs the user's words, a screen's its device.
func answerOf(decision string, by Evidence) ([]byte, bool) {
	by.Words = truncRunes(strings.TrimSpace(noNUL(by.Words)), MaxWords)
	by.Device = noNUL(by.Device)
	switch {
	case by.Via == ViaDashboard, by.Via == ViaVoice && by.Words != "", by.Via == ViaScreen && by.Device != "" && by.Words == "":
	default:
		return nil, false
	}
	b, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Evidence
	}{decision, by})
	return b, err == nil
}

// Task is one stored task. Origin is the zero UUID when the device is gone.
type Task struct {
	ID          uuid.UUID
	ShortID     string
	Kind        string
	Title       string
	Origin      uuid.UUID
	Status      string
	Progress    string
	Question    *Question
	Result      string
	ErrorCode   string
	ExternalRef string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
	ReportedAt  *time.Time
}

// Terminal tasks never change again.
func (t Task) Terminal() bool { return isTerminal(t.Status) }

func isTerminal(s string) bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled
}

// Filter narrows List; zero values match everything.
type Filter struct {
	Kind     string
	Statuses []string
	Origin   uuid.UUID
	Limit    int // default 50, at most 200
}

// Live is the data of a TaskEvent: ids and codes only. Kind is the event kind,
// TaskKind the task's own (hermes, …).
type Live struct {
	ShortID  string `json:"short_id"`
	TaskKind string `json:"task_kind"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Seq      int    `json:"seq"`
}

func validQuestion(q *Question) bool {
	if q == nil || (q.Kind != QuestionClarify && q.Kind != QuestionApproval) {
		return false
	}
	if n := len([]rune(q.Text)); n < 1 || n > MaxQuestionText || len(q.Options) > MaxOptions || !tokenPattern.MatchString(q.Token) {
		return false
	}
	for _, o := range q.Options {
		if len([]rune(o)) > MaxOption {
			return false
		}
	}
	return true
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// KindError is a kind's refusal of a send, approval or cancel. Code is the
// kind's own code when it gave a plain one (ErrorCode() string), else empty:
// a kind's error text never passes.
type KindError struct{ Op, Code string }

func (e *KindError) Error() string {
	if e.Code == "" {
		return "tasks: " + e.Op + " failed"
	}
	return "tasks: " + e.Op + " failed: " + e.Code
}

func kindError(op string, err error) *KindError {
	var c interface{ ErrorCode() string }
	if errors.As(err, &c) {
		return &KindError{Op: op, Code: cleanCode(c.ErrorCode(), "")}
	}
	return &KindError{Op: op}
}
