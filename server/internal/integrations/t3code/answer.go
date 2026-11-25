package t3code

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

// errNotOffered is an approval decision the request's card does not offer.
var errNotOffered = errors.New("t3code: decision not offered")

// answer finds the pending request a hub token names in the thread's
// projection, read now, and sends the answer: so an answer reaches only the
// prompt the user heard, across restarts too.
func (in *instance) answer(ctx context.Context, threadID string, a subscriptions.Answer) error {
	p, err := in.c.GetThreadProjection(ctx, threadID)
	if err != nil {
		return err
	}
	tr := NewTracker(threadID)
	var t3Token string
	for _, u := range tr.Apply(ThreadItem{Kind: ThreadSnapshotKind, Projection: p}) {
		if (u.Kind == KindQuestion || u.Kind == KindApproval) && HubToken(u.Token) == a.Token {
			t3Token = u.Token
		}
	}
	pu, ok := tr.Pending(t3Token)
	if t3Token == "" || !ok {
		return subscriptions.ErrNoAnswer
	}
	var item TurnItem
	for _, it := range p.TurnItems {
		if it.RequestID == pu.RequestID && (it.Type == ItemUserInputRequest || it.Type == ItemApprovalRequest) {
			item = it
		}
	}
	var cmd Command
	switch pu.Kind {
	case KindApproval:
		d, ok := decisionFor(a.Decision)
		if !ok || !offered(item.Options, d) {
			return errNotOffered
		}
		cmd = RespondRuntimeRequest{ThreadID: threadID, RequestID: pu.RequestID, Decision: d}
	case KindQuestion:
		answers, text, err := answersFor(pu.Questions, a)
		if err != nil {
			return err
		}
		if item.ResponseMode == "message" {
			cmd = DispatchMessage{ThreadID: threadID, Text: text, DispatchMode: DispatchMode{Type: DispatchQueueAfterActive}}
		} else {
			cmd = RespondRuntimeRequest{ThreadID: threadID, RequestID: pu.RequestID, Answers: answers}
		}
	default:
		return subscriptions.ErrNoAnswer
	}
	if _, err := in.c.DispatchCommand(ctx, cmd, ""); err != nil {
		return err
	}
	in.d.Log.Info("t3code_answered", "instance", in.name, "kind", pu.Kind)
	return nil
}

// offered reports a decision the card offers; a card that lists none takes
// any.
func offered(opts []ApprovalOption, decision string) bool {
	if len(opts) == 0 {
		return true
	}
	for _, o := range opts {
		if o.Decision == decision {
			return true
		}
	}
	return false
}
