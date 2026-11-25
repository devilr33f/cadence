package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// Tasks lists the background tasks every turn shows (S0 §11.3): the open ones
// and those finished lately that nobody reported yet.
type Tasks interface {
	Active(ctx context.Context) ([]tasks.Task, error)
}

// the block is data (M5a-R5) and carries nothing a task's agent wrote
// (Ruling M5b-R4): no result, no question text, no options of its own; a
// question shows its kind, token and Cadence's two approval answers, and
// task_inspect, which taints the turn, reads the rest
const (
	tasksHeader   = "Background tasks (status only; results arrive separately):"
	tasksAskNote  = "A question's text is the task's own words: task_inspect shows it."
	maxTasksShown = 20
)

// readTasks is the tasks block, read under its bound; failed reports a read
// that did not answer, which leaves the block out.
func (r *Runner) readTasks(ctx context.Context) (note string, failed bool) {
	if r.d.Tasks == nil {
		return "", false
	}
	tctx, cancel := context.WithTimeout(ctx, r.lim.tasks)
	defer cancel()
	list, err := r.d.Tasks.Active(tctx)
	if err != nil {
		return "", true
	}
	return tasksNote(list), false
}

// tasksNote is one line per task: short id, kind, title (from the
// instructions, quoted so it cannot pass for a line of its own), status, an
// open task's progress, a waiting one's question kind and token; empty
// without tasks.
func tasksNote(list []tasks.Task) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(tasksHeader)
	asks := false
	for i, t := range list {
		if i == maxTasksShown {
			fmt.Fprintf(&b, "\n- %d more (task_list shows them)", len(list)-i)
			break
		}
		b.WriteString("\n- " + t.ShortID + " [" + t.Kind + "] " + strconv.Quote(t.Title) + ": " + t.Status)
		if t.Progress != "" && !t.Terminal() && t.Status != tasks.StatusUnknown {
			b.WriteString(", progress " + strconv.Quote(t.Progress))
		}
		if q := t.Question; q != nil && t.Status == tasks.StatusWaitingInput {
			asks = true
			b.WriteString(question(q))
		}
	}
	if asks {
		b.WriteString("\n" + tasksAskNote)
	}
	return b.String()
}

func question(q *tasks.Question) string {
	kind := "a clarify question"
	var parts []string
	if q.Token != "" {
		parts = append(parts, "token "+strconv.Quote(q.Token))
	}
	if q.Kind == tasks.QuestionApproval {
		kind = "an approval question"
		parts = append(parts, `options "once", "deny"`)
	}
	if len(parts) == 0 {
		return "; it asks " + kind
	}
	return "; it asks " + kind + " (" + strings.Join(parts, ", ") + ")"
}

// readInbox is a woken turn's unread updates block, read under its bound. It
// is Cadence's own record (topics, events, counts, tokens), never an outside
// text, so it taints nothing (owner ruling, as the tasks block, Ruling
// M5b-R4); subscription_inspect reads the outside words and taints. A read
// that fails leaves it out.
func (r *Runner) readInbox(ctx context.Context, tr *turn) string {
	if r.d.Inbox == nil || !Woken(tr.Trigger) {
		return ""
	}
	ictx, cancel := context.WithTimeout(ctx, r.lim.inbox)
	defer cancel()
	note, err := r.d.Inbox(ictx, tr.DeviceID, tr.id)
	if err != nil {
		tr.inboxFailed = true
		return ""
	}
	return note
}
