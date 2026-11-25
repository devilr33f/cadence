package cards

import (
	"strconv"
	"strings"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

func itoa(n int) string { return strconv.Itoa(n) }

// taskCards: the task the call started, answered, cancelled or read, as its
// live card (its progress, open and cancel)
func taskCards(t *ToolCards, r ToolResult) []Subject {
	var out struct {
		ID string `json:"id"`
	}
	if t.Task == nil || !decode(r, &out) || out.ID == "" {
		return nil
	}
	if s := t.Task(out.ID); s != nil {
		return []Subject{s}
	}
	return nil
}

// taskStatusKey is a task's status as the catalog words it (tasks' own)
func taskStatusKey(s string) string {
	switch s {
	case "waiting_input":
		return "task.status.waiting"
	case "queued", "running", "done", "failed", "cancelled":
		return "task.status." + s
	}
	return "task.status.unknown"
}

// taskListCards: the tasks the agent listed, each its title and status
func taskListCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Tasks []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("tasks", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		items := make([][2]string, 0, len(out.Tasks))
		for _, x := range out.Tasks {
			title := x.Title
			if strings.TrimSpace(title) == "" {
				title = x.ID
			}
			items = append(items, [2]string{title, rr.T(taskStatusKey(x.Status))})
		}
		kv := rows(items)
		if kv == nil {
			return done(rr, rr.T("task.list.title"), "task", rr.T("task.list.empty"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("task.list.empty")
		}
		b := &cardsv1.CardBlock{Title: rr.T("task.list.title"), Icon: "task", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(kv)}
		return b, rr.T("task.list.title") + ": " + itoa(len(out.Tasks))
	})
}

type subscriptionOut struct {
	Topic    string   `json:"topic"`
	Events   []string `json:"events"`
	Delivery string   `json:"delivery"`
	Until    string   `json:"until"`
}

// module is a topic's module (<module>:<instance>:<path>): the rest names
// someone's work, which the card leaves out
func (s subscriptionOut) module() string {
	m, _, _ := strings.Cut(s.Topic, ":")
	return clip(m, 40)
}

func (s subscriptionOut) rows(rr *Render) *cardsv1.KeyValue {
	return rows([][2]string{
		{rr.T("sub.source"), s.module()},
		{rr.T("sub.events"), clip(strings.Join(s.Events, ", "), 80)},
		{rr.T("sub.delivery"), word(rr, "sub.delivery.", s.Delivery)},
		{rr.T("sub.until"), word(rr, "sub.until.", s.Until)},
	})
}

// subscribeCards: what the device now follows, or stopped following
func subscribeCards(_ *ToolCards, r ToolResult) []Subject {
	var out subscriptionOut
	if !decode(r, &out) {
		return nil
	}
	off := r.Tool == "unsubscribe"
	return card("sub", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state, kind := rr.T("sub.on"), cardsv1.StateKind_STATE_KIND_ON
		if off {
			state, kind = rr.T("sub.off"), cardsv1.StateKind_STATE_KIND_OFF
		}
		b := done(rr, rr.T("sub.title"), "bookmark", state, kind, "")
		if kv := out.rows(rr); kv != nil && !off {
			b.Children = append(b.Children, pcards.N(kv))
		}
		return b, joined(": ", rr.T("sub.title"), joined(", ", state, out.module()))
	})
}

// subscriptionListCards: what the device follows, by module and delivery
func subscriptionListCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Subscriptions []subscriptionOut `json:"subscriptions"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("subs", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		items := make([][2]string, 0, len(out.Subscriptions))
		for _, s := range out.Subscriptions {
			items = append(items, [2]string{s.module(), word(rr, "sub.delivery.", s.Delivery)})
		}
		kv := rows(items)
		if kv == nil {
			return done(rr, rr.T("sub.list"), "bookmark", rr.T("sub.none"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("sub.none")
		}
		b := &cardsv1.CardBlock{Title: rr.T("sub.list"), Icon: "bookmark", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(kv)}
		return b, rr.T("sub.list") + ": " + itoa(len(out.Subscriptions))
	})
}

// subscriptionUpdatesCards: the latest updates the agent read, each its
// title and event
func subscriptionUpdatesCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Updates []struct {
			Event string    `json:"event"`
			Title string    `json:"title"`
			Text  string    `json:"text"`
			At    time.Time `json:"at"`
		} `json:"updates"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("sub-updates", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		entries := make([][2]string, 0, len(out.Updates))
		for _, u := range out.Updates {
			title := u.Title
			if strings.TrimSpace(title) == "" {
				title = u.Text
			}
			entries = append(entries, [2]string{title, clip(u.Event, 40)})
		}
		col := listed(entries, maxListed)
		if len(col.GetChildren()) == 0 {
			return done(rr, rr.T("sub.updates"), "bookmark", rr.T("sub.no_updates"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("sub.no_updates")
		}
		b := &cardsv1.CardBlock{Title: rr.T("sub.updates"), Icon: "bookmark", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(col)}
		return b, rr.T("sub.updates") + ": " + itoa(len(out.Updates))
	})
}

// subscriptionReplyCards: an answer sent to the work the device follows
func subscriptionReplyCards(_ *ToolCards, r ToolResult) []Subject {
	return card("sub-reply", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state := rr.T("sub.answered")
		return done(rr, rr.T("sub.title"), "reply", state, cardsv1.StateKind_STATE_KIND_ON, ""), state
	})
}

// t3ThreadsCards: the T3 Code threads the agent found, each its title and status
func t3ThreadsCards(_ *ToolCards, r ToolResult) []Subject {
	var out struct {
		Threads []struct {
			Title   string `json:"title"`
			Status  string `json:"status"`
			Waiting string `json:"waiting"`
		} `json:"threads"`
		More int `json:"more"`
	}
	if !decode(r, &out) {
		return nil
	}
	return card("t3", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		items := make([][2]string, 0, len(out.Threads))
		for _, th := range out.Threads {
			st := word(rr, "t3.status.", th.Status)
			if th.Waiting != "" {
				st = word(rr, "t3.waiting.", th.Waiting)
			}
			items = append(items, [2]string{th.Title, st})
		}
		kv := rows(items)
		if kv == nil {
			return done(rr, rr.T("t3.title"), "code", rr.T("t3.none"), cardsv1.StateKind_STATE_KIND_OFF, ""), rr.T("t3.none")
		}
		b := &cardsv1.CardBlock{Title: rr.T("t3.title"), Icon: "code", Tone: cardsv1.Tone_TONE_NEUTRAL, Children: pcards.Nodes(kv)}
		return b, rr.T("t3.title") + ": " + itoa(len(out.Threads)+out.More)
	})
}

// t3DoneCards: a thread started, a message sent or a run stopped
func t3DoneCards(_ *ToolCards, r ToolResult) []Subject {
	key := map[string]string{"t3_launch": "t3.started", "t3_send": "t3.sent", "t3_stop": "t3.stopped"}[r.Tool]
	return card("t3-done", r, func(rr *Render) (*cardsv1.CardBlock, string) {
		state := rr.T(key)
		kind := cardsv1.StateKind_STATE_KIND_ON
		if r.Tool == "t3_stop" {
			kind = cardsv1.StateKind_STATE_KIND_OFF
		}
		return done(rr, rr.T("t3.title"), "code", state, kind, ""), rr.T("t3.title") + ": " + state
	})
}
