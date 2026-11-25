package taskcards

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

const (
	// a live tail stays well inside a card's 16 KB
	maxTailBytes = 12 << 10
	maxListed    = 20
)

// part is one card of a task: its list card, or a part of its panel
type part int

const (
	partListed part = iota + 1
	partHead
	partNow
	partQuestion
	partResult
)

var partNames = map[part]string{partListed: "card", partHead: "head", partNow: "now", partQuestion: "ask", partResult: "result"}

type taskCard struct {
	s     *Service
	short string
	part  part
}

// Card is a task's live list card (its status, progress, open and cancel):
// a turn shows it for the task its tool started, answered or read.
func (s *Service) Card(shortID string) cards.Subject {
	return &taskCard{s: s, short: shortID, part: partListed}
}

func (c *taskCard) ID() string { return "task-" + c.short + "-" + partNames[c.part] }

func (c *taskCard) Changed(ctx context.Context) bool { return wait(ctx, c.s.ch.of(c.short)) }

func (c *taskCard) Render(ctx context.Context, r *cards.Render) (*cardsv1.Card, error) {
	t, err := c.s.d.Manager.Get(ctx, c.short)
	if err != nil {
		return nil, err
	}
	switch c.part {
	case partListed:
		return c.listed(r, t), nil
	case partHead:
		return c.head(r, t), nil
	case partNow:
		return c.now(ctx, r, t), nil
	case partQuestion:
		return c.question(r, t), nil
	}
	return c.result(r, t), nil
}

func (c *taskCard) title(t tasks.Task) string {
	if s := clip(t.Title, 80); s != "" {
		return s
	}
	return t.ShortID
}

func (c *taskCard) status(r *cards.Render, t tasks.Task) string {
	return r.T("task.elapsed", "status", r.T(statusKey(t.Status)), "elapsed", duration(r, elapsed(t, c.s.d.Now())))
}

func tone(status string) cardsv1.Tone {
	switch status {
	case tasks.StatusWaitingInput:
		return cardsv1.Tone_TONE_WARNING
	case tasks.StatusFailed:
		return cardsv1.Tone_TONE_DANGER
	case tasks.StatusDone:
		return cardsv1.Tone_TONE_SUCCESS
	case tasks.StatusRunning, tasks.StatusQueued:
		return cardsv1.Tone_TONE_INFO
	}
	return cardsv1.Tone_TONE_NEUTRAL
}

func (c *taskCard) fallback(r *cards.Render, t tasks.Task) string {
	return clip(r.T("task.fallback", "title", c.title(t), "status", r.T(statusKey(t.Status))), 280)
}

func (c *taskCard) block(r *cards.Render, t tasks.Task) *cardsv1.CardBlock {
	return &cardsv1.CardBlock{Title: c.title(t), Subtitle: clip(c.status(r, t), 80), Icon: "task", Tone: tone(t.Status)}
}

// progressLine is what the task does now: its current tool, else its phase
func progressLine(r *cards.Render, t tasks.Task) *cardsv1.Text {
	if !active(t) || t.Progress == "" {
		return nil
	}
	text := clip(t.Progress, 280)
	if tool := toolOf(t.Progress); tool != "" {
		text = r.T("task.tool", "tool", clip(tool, 64))
	}
	return &cardsv1.Text{Text: text, Style: cardsv1.TextStyle_TEXT_STYLE_META}
}

func (c *taskCard) listed(r *cards.Render, t tasks.Task) *cardsv1.Card {
	b := c.block(r, t)
	if p := progressLine(r, t); p != nil {
		b.Children = append(b.Children, pcards.N(p))
	}
	b.Actions = []*cardsv1.Button{{Label: r.T("task.open"), Icon: "open", Action: r.Bind(c.s.openBinding(t.ShortID))}}
	return pcards.NewCard("", 0, c.fallback(r, t), b)
}

func (c *taskCard) head(r *cards.Render, t tasks.Task) *cardsv1.Card {
	b := c.block(r, t)
	b.Children = append(b.Children, pcards.N(&cardsv1.KeyValue{Items: []*cardsv1.KeyValueItem{
		{Key: clip(r.T("task.request"), 40), Value: clip(c.title(t), 80)},
	}}))
	if p := progressLine(r, t); p != nil {
		b.Children = append(b.Children, pcards.N(p))
	}
	if t.Status == tasks.StatusRunning || t.Status == tasks.StatusQueued || t.Status == tasks.StatusWaitingInput {
		b.Actions = []*cardsv1.Button{{Label: r.T("task.cancel"), Icon: "close",
			Action: r.BindConfirm(c.s.cancelBinding(t.ShortID), r.T("task.confirm.cancel"))}}
	}
	return pcards.NewCard("", 0, c.fallback(r, t), b)
}

// now is the live tail of the run's output: a new revision appends below
func (c *taskCard) now(ctx context.Context, r *cards.Render, t tasks.Task) *cardsv1.Card {
	b := &cardsv1.CardBlock{Title: clip(r.T("task.now"), 80), Icon: "terminal", Tone: cardsv1.Tone_TONE_NEUTRAL}
	text := ""
	if snap, err := c.s.d.Manager.Inspect(ctx, t.ShortID, tasks.DetailTranscript); err == nil {
		text = tail(cleanTail(snap.Transcript), 8000, maxTailBytes)
	}
	if text == "" {
		b.Children = pcards.Nodes(&cardsv1.Text{Text: r.T(statusKey(t.Status)), Style: cardsv1.TextStyle_TEXT_STYLE_META})
	} else {
		b.Children = pcards.Nodes(&cardsv1.LongText{Text: text, Tail: true})
	}
	return pcards.NewCard("", 0, c.fallback(r, t), b)
}

func (c *taskCard) question(r *cards.Render, t tasks.Task) *cardsv1.Card {
	q := t.Question
	b := &cardsv1.CardBlock{Title: clip(r.T("task.question"), 80), Icon: "question", Tone: cardsv1.Tone_TONE_WARNING}
	if q == nil || t.Status != tasks.StatusWaitingInput {
		b.Children = pcards.Nodes(&cardsv1.Text{Text: r.T(statusKey(t.Status)), Style: cardsv1.TextStyle_TEXT_STYLE_META})
		return pcards.NewCard("", 0, c.fallback(r, t), b)
	}
	switch q.Kind {
	case tasks.QuestionApproval:
		b.Title = clip(r.T("task.approval"), 80)
		desc, cmd := approvalParts(q.Text)
		if desc != "" {
			b.Children = append(b.Children, pcards.N(&cardsv1.Text{Text: clip(desc, 280), Style: cardsv1.TextStyle_TEXT_STYLE_BODY}))
		}
		if cmd != "" {
			b.Children = append(b.Children, pcards.N(&cardsv1.Code{Text: truncRunes(cmd, 4000)}))
		}
		b.Actions = []*cardsv1.Button{
			{Label: r.T("task.approve"), Primary: true,
				Action: r.BindConfirm(c.s.approveBinding(t.ShortID, q.Token, tasks.DecisionOnce), r.T("task.confirm.approve"))},
			{Label: r.T("task.deny"),
				Action: r.BindConfirm(c.s.approveBinding(t.ShortID, q.Token, tasks.DecisionDeny), r.T("task.confirm.deny"))},
		}
	default:
		b.Children = append(b.Children, pcards.N(&cardsv1.Text{Text: clip(q.Text, 280), Style: cardsv1.TextStyle_TEXT_STYLE_BODY}))
		if len(q.Options) > 0 {
			// single choices: one button per option, no submit
			ch := &cardsv1.Choices{}
			for i, o := range q.Options {
				ch.Options = append(ch.Options, &cardsv1.Choice{Id: "o" + strconv.Itoa(i), Label: clip(o, 40)})
			}
			ch.Action = r.BindConfirm(c.s.answerBinding(t.ShortID, q.Token, q.Options), r.T("task.confirm.answer"))
			b.Children = append(b.Children, pcards.N(ch))
		}
	}
	return pcards.NewCard("", 0, c.fallback(r, t), b)
}

func (c *taskCard) result(r *cards.Render, t tasks.Task) *cardsv1.Card {
	b := &cardsv1.CardBlock{Title: clip(r.T("task.result"), 80), Icon: "result", Tone: tone(t.Status)}
	switch {
	case t.Status == tasks.StatusDone && strings.TrimSpace(t.Result) != "":
		b.Children = pcards.Nodes(&cardsv1.LongText{Text: tail(t.Result, 8000, maxTailBytes)})
	case t.ErrorCode != "":
		b.Children = pcards.Nodes(&cardsv1.Text{Text: clip(r.T("task.error", "code", t.ErrorCode), 280),
			Style: cardsv1.TextStyle_TEXT_STYLE_META, Tone: cardsv1.Tone_TONE_DANGER})
	default:
		b.Children = pcards.Nodes(&cardsv1.Text{Text: r.T(statusKey(t.Status)), Style: cardsv1.TextStyle_TEXT_STYLE_META})
	}
	return pcards.NewCard("", 0, c.fallback(r, t), b)
}

// panel is a task's page: its head, what it does now, its question while
// it waits, and its result once it ended
func (s *Service) panel(ctx context.Context, short string) (*cards.Panel, error) {
	t, err := s.d.Manager.Get(ctx, short)
	if err != nil {
		return nil, err
	}
	p := &cards.Panel{Cards: []cards.Subject{&taskCard{s: s, short: short, part: partHead}}}
	if t.Status == tasks.StatusWaitingInput && t.Question != nil {
		p.Cards = append(p.Cards, &taskCard{s: s, short: short, part: partQuestion})
	}
	if active(t) {
		p.Cards = append(p.Cards, &taskCard{s: s, short: short, part: partNow})
	} else {
		p.Cards = append(p.Cards, &taskCard{s: s, short: short, part: partResult})
	}
	return p, nil
}

// list is the tasks page: the open ones, then the last finished
func (s *Service) list(ctx context.Context) (*cards.Panel, error) {
	open, err := s.d.Manager.List(ctx, tasks.Filter{
		Statuses: []string{tasks.StatusWaitingInput, tasks.StatusRunning, tasks.StatusQueued}, Limit: maxListed,
	})
	if err != nil {
		return nil, err
	}
	// waiting first: it needs the user
	var waiting, running []tasks.Task
	for _, t := range open {
		if t.Status == tasks.StatusWaitingInput {
			waiting = append(waiting, t)
		} else {
			running = append(running, t)
		}
	}
	shown := append(waiting, running...)
	if len(shown) < maxListed {
		done, err := s.d.Manager.List(ctx, tasks.Filter{
			Statuses: []string{tasks.StatusDone, tasks.StatusFailed, tasks.StatusCancelled, tasks.StatusUnknown},
			Limit:    maxListed - len(shown),
		})
		if err != nil {
			return nil, err
		}
		shown = append(shown, done...)
	}
	p := &cards.Panel{}
	for _, t := range shown {
		p.Cards = append(p.Cards, &taskCard{s: s, short: t.ShortID, part: partListed})
	}
	if len(p.Cards) == 0 {
		p.Cards = []cards.Subject{emptyList{}}
	}
	return p, nil
}

type emptyList struct{}

func (emptyList) ID() string { return "tasks-empty" }

func (emptyList) Render(_ context.Context, r *cards.Render) (*cardsv1.Card, error) {
	text := r.T("task.list.empty")
	return pcards.NewCard("", 0, text, &cardsv1.CardBlock{Title: clip(r.T("task.list.title"), 80), Icon: "task",
		Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(&cardsv1.Text{Text: text, Style: cardsv1.TextStyle_TEXT_STYLE_META})}), nil
}

// refusal maps a manager error onto the catalog's toasts
func refusal(err error) string {
	var ke *tasks.KindError
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		return "not_found"
	case errors.Is(err, tasks.ErrNoApproval):
		return "no_approval"
	case errors.Is(err, tasks.ErrStale):
		return "stale"
	case errors.Is(err, tasks.ErrNotActive), errors.Is(err, tasks.ErrTransition):
		return "not_active"
	case errors.As(err, &ke):
		return "failed"
	}
	return "internal"
}

func (s *Service) openBinding(short string) cards.Binding {
	return cards.Binding{Idempotent: true, Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
		p, err := s.panel(ctx, short)
		if err != nil {
			return cards.Outcome{Refused: refusal(err)}
		}
		return cards.Outcome{Panel: p}
	}}
}

func (s *Service) cancelBinding(short string) cards.Binding {
	return cards.Binding{Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
		_, err := s.d.Manager.Cancel(ctx, short)
		s.d.Log.Info("task_card_action", "short_id", short, "op", "cancel", "code", code(err))
		rec := &cards.Record{Tool: "task_cancel", Args: map[string]string{"task": short}, Result: result(err)}
		if err != nil {
			return cards.Outcome{Refused: refusal(err), Record: rec}
		}
		return cards.Outcome{Record: rec}
	}}
}

func (s *Service) approveBinding(short, token, decision string) cards.Binding {
	return cards.Binding{Run: func(ctx context.Context, a cards.Act) cards.Outcome {
		_, err := s.d.Manager.Approve(ctx, short, token, decision, tasks.Evidence{Via: tasks.ViaScreen, Device: a.DeviceID})
		s.d.Log.Info("task_card_action", "short_id", short, "op", "approve", "decision", decision, "code", code(err))
		rec := &cards.Record{Tool: "task_approve", Args: map[string]string{"task": short, "decision": decision}, Result: result(err)}
		if err != nil {
			return cards.Outcome{Refused: refusal(err), Record: rec}
		}
		return cards.Outcome{Record: rec}
	}}
}

func (s *Service) answerBinding(short, token string, options []string) cards.Binding {
	return cards.Binding{Run: func(ctx context.Context, a cards.Act) cards.Outcome {
		i, err := strconv.Atoi(strings.TrimPrefix(a.Value.GetId(), "o"))
		if err != nil || i < 0 || i >= len(options) {
			return cards.Outcome{Refused: "unknown"}
		}
		_, err = s.d.Manager.Send(ctx, short, token, options[i])
		s.d.Log.Info("task_card_action", "short_id", short, "op", "answer", "code", code(err))
		rec := &cards.Record{Tool: "task_reply", Args: map[string]string{"task": short, "message": options[i]}, Result: result(err)}
		if err != nil {
			return cards.Outcome{Refused: refusal(err), Record: rec}
		}
		return cards.Outcome{Record: rec}
	}}
}

func code(err error) string {
	if err == nil {
		return "ok"
	}
	return refusal(err)
}

func result(err error) any {
	if err == nil {
		return map[string]bool{"ok": true}
	}
	return map[string]any{"error": map[string]string{"code": refusal(err)}}
}

func duration(r *cards.Render, d time.Duration) string {
	switch {
	case d < time.Minute:
		return r.T("duration.s", "n", strconv.Itoa(int(d.Seconds())))
	case d < time.Hour:
		return r.T("duration.m", "n", strconv.Itoa(int(d.Minutes())))
	}
	return r.T("duration.h", "h", strconv.Itoa(int(d.Hours())), "m", strconv.Itoa(int(d.Minutes())%60))
}

// cleanTail drops control characters but the line breaks
func cleanTail(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

// tail keeps the newest n runes and at most b bytes of s
func tail(s string, n, b int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[len(r)-n:])
	}
	for len(s) > b {
		_, size := utf8.DecodeRuneInString(s)
		s = s[size:]
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(cleanTail(s), "")), " ")
	return truncRunes(s, n)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
