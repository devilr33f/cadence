package tasks

import (
	"regexp"
	"strings"
)

// allowed lists the event kinds a task accepts per status. Terminal statuses
// accept none; waiting_input only comes from running; an unknown task can only
// be resolved by a kind that can confirm its end, never resumed.
var allowed = map[string]map[string]string{
	StatusRunning: {
		"progress": StatusRunning, "waiting_input": StatusWaitingInput, "done": StatusDone,
		"failed": StatusFailed, "cancelled": StatusCancelled, "unknown": StatusUnknown,
	},
	StatusWaitingInput: {
		"progress": StatusRunning, "done": StatusDone, "failed": StatusFailed,
		"cancelled": StatusCancelled, "unknown": StatusUnknown,
	},
	StatusUnknown: {"done": StatusDone, "failed": StatusFailed, "cancelled": StatusCancelled},
}

// keep carries a stored task's fields into an update.
func keep(cur Task) Update {
	u := Update{Status: cur.Status, Progress: cur.Progress}
	if cur.Result != "" {
		u.Result = &cur.Result
	}
	if cur.ErrorCode != "" {
		u.ErrorCode = &cur.ErrorCode
	}
	return u
}

var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// tokenPattern keeps a question token a plain word, or empty for a kind without them.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// cleanCode keeps a kind's error code only if it is a plain token: kinds such
// as Hermes are untrusted, and a code reaches logs and the store.
func cleanCode(code, fallback string) string {
	if codePattern.MatchString(code) {
		return code
	}
	return fallback
}

// noNUL drops NUL bytes: postgres text cannot hold them.
func noNUL(s string) string { return strings.ReplaceAll(s, "\x00", "") }

func cleanQuestion(q *Question) *Question {
	c := Question{Kind: q.Kind, Text: noNUL(q.Text), Token: q.Token}
	for _, o := range q.Options {
		c.Options = append(c.Options, noNUL(o))
	}
	return &c
}

// nextState applies a kind's event to a stored task: the update to store and
// the task_events kind, or ErrTransition. Text fields are cleaned and capped
// here; the error code is kept only for failed and unknown.
func nextState(cur Task, ev Event) (Update, string, error) {
	to, ok := allowed[cur.Status][ev.Kind]
	if !ok {
		return Update{}, "", ErrTransition
	}
	u := keep(cur)
	u.Status = to
	if ev.Progress != "" {
		u.Progress = truncRunes(noNUL(ev.Progress), MaxProgress)
	}
	switch ev.Kind {
	case "waiting_input":
		if !validQuestion(ev.Question) {
			return Update{}, "", ErrTransition
		}
		u.Question = cleanQuestion(ev.Question)
		if !validQuestion(u.Question) {
			return Update{}, "", ErrTransition
		}
	case "done":
		u.ErrorCode = nil
		if ev.Result != "" {
			r := truncBytes(noNUL(ev.Result), MaxResult)
			u.Result = &r
		}
	case "failed":
		code := cleanCode(ev.ErrorCode, "failed")
		u.ErrorCode = &code
	case "cancelled":
		u.ErrorCode = nil
	case "unknown":
		code := cleanCode(ev.ErrorCode, "unknown")
		u.ErrorCode = &code
	}
	u.Finished = to != StatusRunning && to != StatusWaitingInput
	return u, ev.Kind, nil
}
