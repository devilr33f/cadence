package subscriptions

import (
	"context"
	"errors"
	"slices"
	"strings"
)

var (
	// ErrAnswerKind is an answer of the wrong shape for its question: a
	// decision for a clarify question, an answer for an approval, an option
	// the question does not offer.
	ErrAnswerKind = errors.New("subscriptions: answer does not fit the question")
	// ErrAnswerRefused is a module that did not take the answer.
	ErrAnswerRefused = errors.New("subscriptions: the module did not take the answer")
)

const (
	maxAnswerMessage = 2000
	maxAnswers       = 16
)

// Question is the open question a token names on a device ("" any device):
// ErrNotFound when none is open.
func (h *Hub) Question(ctx context.Context, deviceID, token string) (InboxItem, error) {
	if !tokenRe.MatchString(token) {
		return InboxItem{}, ErrNotFound
	}
	return h.d.Store.openQuestion(ctx, token, deviceID)
}

// ToldBefore reports whether the question was told on the device before the
// turn turn names: spoken there, or read by another woken turn there. A voice
// approval answers only such a question.
func (h *Hub) ToldBefore(ctx context.Context, it InboxItem, deviceID, turnID string) (bool, error) {
	return h.d.Store.toldBefore(ctx, it, deviceID, turnID)
}

// Reply passes an answer to the open question its token names on a device
// ("" any device) to the question's module, once it fits the question, and
// marks the question answered. The policy of who may answer (a voice
// approval) is the caller's.
func (h *Hub) Reply(ctx context.Context, deviceID string, a Answer) (InboxItem, error) {
	it, err := h.Question(ctx, deviceID, a.Token)
	if err != nil {
		return InboxItem{}, err
	}
	if err := fits(it.Question, a); err != nil {
		return InboxItem{}, err
	}
	m, ok := h.d.Registry.Get(it.Module)
	if !ok {
		return InboxItem{}, ErrUnknownModule
	}
	if err := m.Answer(ctx, it.ref(), a); err != nil {
		if errors.Is(err, ErrNoAnswer) {
			return InboxItem{}, ErrNoAnswer
		}
		if ctx.Err() != nil {
			return InboxItem{}, ctx.Err()
		}
		h.d.Log.Warn("subscription_answer_failed", "module", it.Module, "via", a.By.Via)
		return InboxItem{}, ErrAnswerRefused
	}
	if err := h.d.Store.answered(ctx, it); err != nil {
		h.failed(ctx, "answered", err)
	}
	h.d.Log.Info("subscription_answered", "module", it.Module, "item_id", it.ID, "via", a.By.Via)
	h.publish(it.Device, EventInbox, map[string]any{"ids": []int64{it.ID}, "action": "answered"})
	it.Open = false
	return it, nil
}

// fits checks an answer against its question: an approval takes a decision
// alone, a clarify question option ids it offers (one unless multi) or a
// message (when free answers are allowed, or it offers none).
func fits(q *Question, a Answer) error {
	if q == nil {
		return ErrNotFound
	}
	if a.By.Via != ViaVoice && a.By.Via != ViaDashboard && a.By.Via != ViaScreen {
		return ErrInvalid
	}
	if q.Kind == QuestionApproval {
		if len(a.Answers) > 0 || a.Message != "" {
			return ErrAnswerKind
		}
		switch a.Decision {
		case DecisionOnce, DecisionSession, DecisionDeny:
			return nil
		}
		return ErrAnswerKind
	}
	if a.Decision != "" {
		return ErrAnswerKind
	}
	msg := strings.TrimSpace(a.Message)
	switch {
	case len(a.Answers) == 0 && msg == "":
		return ErrAnswerKind
	case len(a.Answers) > 0 && msg != "":
		return ErrAnswerKind
	case len([]rune(msg)) > maxAnswerMessage:
		return ErrAnswerKind
	case msg != "" && len(q.Options) > 0 && !q.AllowCustom:
		return ErrAnswerKind
	case len(a.Answers) > maxAnswers || len(a.Answers) > 1 && !q.Multi:
		return ErrAnswerKind
	}
	for i, id := range a.Answers {
		if slices.Contains(a.Answers[:i], id) || !slices.ContainsFunc(q.Options, func(o Option) bool { return o.ID == id }) {
			return ErrAnswerKind
		}
	}
	return nil
}
