// Package taskcards shows background tasks (Hermes and friends) on screens
// as Cadence Cards (W0-C item 9b): tasks.list answers a page of task cards
// (running and waiting first, then the last finished), tasks.item a task's
// panel, and both, subscribed, push new revisions as the task manager's
// events arrive. A card's actions cancel a task, answer a clarify choice, or
// approve once or deny an approval (always with a confirm, evidence screen).
// Toasts tell a task's room that it waits or ended, held by quiet hours.
//
// A task's title, question, output and result are the user's and the
// agent's text: shown, stored as the task store keeps them (D8), never
// logged (rule 8). Lines carry short ids and codes.
package taskcards

import (
	"context"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// Manager is what the cards read and do through the task manager.
type Manager interface {
	List(ctx context.Context, f tasks.Filter) ([]tasks.Task, error)
	Get(ctx context.Context, shortID string) (tasks.Task, error)
	Inspect(ctx context.Context, shortID string, d tasks.Detail) (tasks.Snapshot, error)
	Approve(ctx context.Context, shortID, token, decision string, by tasks.Evidence) (tasks.Task, error)
	Send(ctx context.Context, shortID, token, msg string) (tasks.Task, error)
	Cancel(ctx context.Context, shortID string) (tasks.Task, error)
	Watch() *events.Subscription
}

// changes wakes the cards of a task (and the list) when the manager reports
// it changed: each waiter holds the channel current when it rendered
type changes struct {
	mu    sync.Mutex
	any   chan struct{}
	tasks map[string]chan struct{}
}

func newChanges() *changes {
	return &changes{any: make(chan struct{}), tasks: map[string]chan struct{}{}}
}

// of is the channel closed at the next change of shortID ("" any task)
func (c *changes) of(shortID string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if shortID == "" {
		return c.any
	}
	ch := c.tasks[shortID]
	if ch == nil {
		ch = make(chan struct{})
		c.tasks[shortID] = ch
	}
	return ch
}

func (c *changes) changed(shortID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.any)
	c.any = make(chan struct{})
	if ch := c.tasks[shortID]; ch != nil {
		close(ch)
		delete(c.tasks, shortID)
	}
}

// wait blocks on ch, false once ctx ends first
func wait(ctx context.Context, ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func statusKey(s string) string {
	switch s {
	case tasks.StatusWaitingInput:
		return "task.status.waiting"
	case tasks.StatusQueued, tasks.StatusRunning, tasks.StatusDone, tasks.StatusFailed, tasks.StatusCancelled:
		return "task.status." + s
	}
	return "task.status.unknown"
}

func active(t tasks.Task) bool {
	return t.Status == tasks.StatusQueued || t.Status == tasks.StatusRunning || t.Status == tasks.StatusWaitingInput
}

// elapsed is how long the task ran, or runs so far
func elapsed(t tasks.Task, now time.Time) time.Duration {
	end := now
	if t.FinishedAt != nil {
		end = *t.FinishedAt
	}
	return max(end.Sub(t.CreatedAt), 0)
}

// toolOf is the tool a running task's progress names (Hermes's "running:
// bash"), "" when it names none
func toolOf(progress string) string {
	if t, ok := strings.CutPrefix(progress, "running: "); ok {
		return t
	}
	return ""
}

// approvalParts splits an approval's text into its description and command,
// as the Hermes kind words it ("description\nCommand: cmd")
func approvalParts(text string) (desc, cmd string) {
	if c, ok := strings.CutPrefix(text, "Command: "); ok {
		return "", c
	}
	if i := strings.LastIndex(text, "\nCommand: "); i >= 0 {
		return strings.TrimSpace(text[:i]), text[i+len("\nCommand: "):]
	}
	return text, ""
}
