package subscriptions

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// the unread block carries Cadence's own record alone (owner ruling, as the
// tasks block, Ruling M5b-R4): topics, modules, instances, events, kinds,
// counts and question tokens, never an outside text, so reading it taints
// nothing; Inspect reads the outside words and taints the turn that calls it
const (
	inboxHeader = "Unread updates from outside work the user follows, grouped by topic: Cadence's own record " +
		"(topics, events, counts), no outside text. subscription_inspect with a topic or a question's token " +
		"shows the titles, texts and questions."
	inboxAnswerNote = "Answer a question with subscription_reply and its token, only with what the user said; " +
		"subscription_inspect shows its words and options first."
	maxInboxShown = 30
	// maxInspect bounds the updates an inspect of a topic returns
	maxInspect = 10
)

// Inbox reads the unread block of a woken turn on a device: delivered or
// inbox updates no woken turn saw, and every open question. Reading it marks
// them seen by turn; it tells no question (Inspect or a spoken update does);
// "" when there is nothing.
func (h *Hub) Inbox(ctx context.Context, deviceID, turnID string) (string, error) {
	items, err := h.d.Store.Unread(ctx, deviceID)
	if err != nil || len(items) == 0 {
		return "", err
	}
	if len(items) > maxInboxShown {
		// the newest, and every open question the cut would drop is lost to
		// this turn only
		items = items[len(items)-maxInboxShown:]
	}
	note := inboxNote(items, h.location(ctx, deviceID))
	if err := h.d.Store.seen(ctx, turnID, items, ""); err != nil {
		return "", err
	}
	h.publishSeen(deviceID, items)
	return note, nil
}

func (h *Hub) publishSeen(deviceID string, items []InboxItem) {
	unseen := ids(items, func(it InboxItem) bool { return !it.Seen })
	if len(unseen) > 0 {
		h.publish(deviceID, EventInbox, map[string]any{"ids": unseen, "action": "seen"})
	}
}

// Inspect reads the outside words of a device's updates for a woken turn:
// the open question a token names, or a topic's latest updates (at most 10,
// oldest first). The open questions it returns count as told on the device
// by that turn, so a later turn's voice approval may answer them; the items
// count as seen. ErrNotFound when there is nothing.
func (h *Hub) Inspect(ctx context.Context, deviceID, turnID, token, topic string) ([]InboxItem, error) {
	var items []InboxItem
	switch {
	case token != "":
		it, err := h.Question(ctx, deviceID, token)
		if err != nil {
			return nil, err
		}
		items = []InboxItem{it}
	default:
		t, err := ParseTopic(topic)
		if err != nil || t.Wild {
			return nil, ErrTopic
		}
		if items, err = h.d.Store.ofTopic(ctx, deviceID, t.String(), maxInspect); err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	if err := h.d.Store.seen(ctx, turnID, items, deviceID); err != nil {
		return nil, err
	}
	h.publishSeen(deviceID, items)
	return items, nil
}

func (h *Hub) location(ctx context.Context, deviceID string) *time.Location {
	if h.d.Settings == nil {
		return time.UTC
	}
	v, err := h.d.Settings(ctx, deviceID)
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(v.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// inboxNote is the block: a header, then per topic, in the order its first
// update came, a line with the topic (quoted), its module, instance and kind
// and its updates counted with their events and times, and a line per open
// question with its kind, token and the number of its options.
func inboxNote(items []InboxItem, loc *time.Location) string {
	var order []string
	byTopic := map[string][]InboxItem{}
	for _, it := range items {
		if _, ok := byTopic[it.Topic]; !ok {
			order = append(order, it.Topic)
		}
		byTopic[it.Topic] = append(byTopic[it.Topic], it)
	}
	var b strings.Builder
	b.WriteString(inboxHeader)
	asks := false
	for _, topic := range order {
		group := byTopic[topic]
		last := group[len(group)-1]
		b.WriteString("\n- " + strconv.Quote(topic) + " (module " + last.Module + ", instance " + last.Instance)
		if last.Kind != "" {
			b.WriteString(", " + codeOf(last.Kind))
		}
		b.WriteString("): " + strconv.Itoa(len(group)) + " update")
		if len(group) > 1 {
			b.WriteString("s")
		}
		evs := make([]string, len(group))
		for i, it := range group {
			evs[i] = codeOf(it.Event) + " at " + it.At.In(loc).Format("15:04")
		}
		b.WriteString(": " + strings.Join(evs, ", "))
		for _, it := range group {
			if q := it.Question; q != nil && it.Open {
				asks = true
				b.WriteString("\n  - " + questionLine(q))
			}
		}
	}
	if asks {
		b.WriteString("\n" + inboxAnswerNote)
	}
	return b.String()
}

// questionLine is a question as the block shows it: Cadence's own words, its
// kind, token and count of options.
func questionLine(q *Question) string {
	if q.Kind == QuestionApproval {
		return `open: an approval question (token ` + strconv.Quote(q.Token) + `); decisions "once", "session", "deny"`
	}
	s := "open: a clarify question (token " + strconv.Quote(q.Token)
	switch n := len(q.Options); n {
	case 0:
		s += ", a free answer"
	case 1:
		s += ", 1 option"
	default:
		s += ", " + strconv.Itoa(n) + " options"
	}
	return s + ")"
}
