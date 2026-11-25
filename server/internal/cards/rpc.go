package cards

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// actionArgs are card.action's: {cardId, revision, actionId, value?} for a
// card, {pageId, actionId} for a page's next action. value is a number (a
// slider), a string (single choices) or a list of strings (multiple choices).
// A page's next action with neither id (a screen before page ids) is taken
// for one release: the server knows the page by the action id it issued.
type actionArgs struct {
	CardID   string          `json:"cardId"`
	PageID   string          `json:"pageId"`
	Revision uint32          `json:"revision"`
	ActionID string          `json:"actionId"`
	Value    json.RawMessage `json:"value,omitempty"`
}

// ActionAnswer is card.action's json result, kept for one release beside the
// typed answer (RpcResponse.card_action, a cards.v1 CardActionResult: the
// card's next revision, a page, or a refusal with the card to roll back to):
// Revision is the card's next revision (0 for a page), and Toast the refusal
// in the screen's language.
type ActionAnswer struct {
	Code     string `json:"code,omitempty"`
	Toast    string `json:"toast,omitempty"`
	Revision uint32 `json:"revision,omitempty"`
}

func parseValue(raw json.RawMessage) (*cardsv1.ActionValue, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return pcards.NumberValue(n), true
	}
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return pcards.IDValue(id), true
	}
	var ids []string
	if json.Unmarshal(raw, &ids) == nil {
		return pcards.IDsValue(ids...), true
	}
	return nil, false
}

// parseRequest checks the request's fields as pkg/cards does a screen's
func parseRequest(raw json.RawMessage) (*cardsv1.ActionRequest, bool) {
	var a actionArgs
	if json.Unmarshal(raw, &a) != nil || a.ActionID == "" {
		return nil, false
	}
	v, ok := parseValue(a.Value)
	if !ok {
		return nil, false
	}
	b, err := proto.Marshal(&cardsv1.ActionRequest{
		CardId: a.CardID, PageId: a.PageID, Revision: a.Revision, ActionId: a.ActionID, Value: v,
	})
	if err != nil {
		return nil, false
	}
	req, err := pcards.DecodeActionRequest(b)
	if err != nil {
		return nil, false
	}
	return req, true
}

// ActionMethod serves card.action: it runs an issued action without the
// model, answers the card's next revision (or a page), and, subscribed,
// pushes the card's later revisions while it changes, until cancelled or its
// ttl.
func (s *Service) ActionMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		req, ok := parseRequest(c.Args)
		if !ok {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		t, err := s.take(c.DeviceID, req)
		if err != nil {
			code, status := "invalid", rpc.StatusInvalid
			switch {
			case errors.Is(err, errUnknown):
				code, status = "unknown", rpc.StatusNotFound
			case errors.Is(err, errExpired):
				code, status = "expired", rpc.StatusExpired
			}
			s.d.Log.Info("card_action", "device_id", c.DeviceID, "rpc_id", c.ID, "code", code)
			toast := s.toast(ctx, c.DeviceID, code)
			return calls.Reply{Status: status, JSON: ActionAnswer{Code: code, Toast: toast},
				CardAction: pcards.RefusalResult(code, toast, nil)}
		}
		out := t.b.Run(ctx, Act{DeviceID: c.DeviceID, Value: req.GetValue()})
		if out.Record != nil && s.d.Record != nil {
			s.d.Record(context.WithoutCancel(ctx), c.DeviceID, *out.Record)
		}
		code := out.Refused
		if code == "" {
			code = "ok"
		}
		s.d.Log.Info("card_action", "device_id", c.DeviceID, "rpc_id", c.ID, "card_id", t.cardID, "code", code)
		rep := calls.Reply{Status: rpc.StatusOK}
		ans := ActionAnswer{}
		var next *cardsv1.Card
		// drawn again unless a panel opened (its opener is idempotent, so the card the screen holds keeps working); a
		// refusal rolls the screen back to the server's state
		if t.subject != nil && (out.Refused != "" || out.Panel == nil) {
			if n, err := s.Show(ctx, c.DeviceID, t.subject); err == nil {
				next = n
				ans.Revision = n.GetRevision()
			}
		}
		switch {
		case out.Refused != "":
			rep.Status = rpc.StatusRefused
			ans.Code, ans.Toast = out.Refused, s.toast(ctx, c.DeviceID, out.Refused)
			rep.CardAction = pcards.RefusalResult(ans.Code, ans.Toast, next)
		case out.Panel != nil:
			p, err := s.Page(ctx, c.DeviceID, out.Panel)
			if err != nil {
				return calls.Fail(rpc.StatusInternal, "internal")
			}
			rep.CardAction = pcards.PageResult(p)
		case next != nil:
			rep.CardAction = pcards.CardResult(next)
		}
		rep.JSON = ans
		if t.subject != nil {
			if _, live := t.subject.(Live); live {
				rep.Follow = s.follow([]string{t.cardID})
			}
		}
		return rep
	}
}

func (s *Service) toast(ctx context.Context, dev, code string) string {
	return s.d.Messages.Format(s.lang(ctx, dev), "refusal."+code, nil)
}

// WatchMethod serves cards.watch {cardIds}: subscribed, it pushes a new
// revision of each live card the device holds whenever its subject changes,
// until cancelled or every card's ttl ran out. Its answer counts the cards it
// watches.
func (s *Service) WatchMethod() calls.Method {
	return func(_ context.Context, c calls.Call) calls.Reply {
		var a struct {
			CardIDs []string `json:"cardIds"`
		}
		if json.Unmarshal(c.Args, &a) != nil || len(a.CardIDs) > pcards.MaxDrawerCards*4 {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		var live []string
		for _, id := range a.CardIDs {
			if s.live(c.DeviceID, id) {
				live = append(live, id)
			}
		}
		rep := calls.OK(map[string]int{"watching": len(live)})
		if len(live) > 0 {
			rep.Follow = s.follow(live)
		}
		return rep
	}
}

func (s *Service) live(dev, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devs[dev]
	if d == nil {
		return false
	}
	sh := d.cards[id]
	if sh == nil || !s.d.Now().Before(sh.expires) {
		return false
	}
	_, ok := sh.subject.(Live)
	return ok
}

func (s *Service) subject(dev, id string) (Subject, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devs[dev]
	if d == nil || d.cards[id] == nil {
		return nil, time.Time{}, false
	}
	sh := d.cards[id]
	return sh.subject, sh.expires, s.d.Now().Before(sh.expires)
}

// follow pushes each card's next revision whenever its subject changes,
// while the card has not expired
func (s *Service) follow(ids []string) func(ctx context.Context, st *calls.Stream) {
	return func(ctx context.Context, st *calls.Stream) {
		done := make(chan struct{}, len(ids))
		for _, id := range ids {
			go func() {
				defer func() { done <- struct{}{} }()
				s.watchOne(ctx, st, id)
			}()
		}
		for range ids {
			<-done
		}
	}
}

func (s *Service) watchOne(ctx context.Context, st *calls.Stream, id string) {
	dev := st.DeviceID()
	for {
		subj, expires, ok := s.subject(dev, id)
		if !ok {
			return
		}
		l, ok := subj.(Live)
		if !ok {
			return
		}
		wctx, cancel := context.WithDeadline(ctx, expires)
		changed := l.Changed(wctx)
		cancel()
		if !changed {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		c, err := s.Show(ctx, dev, subj)
		if err != nil {
			return
		}
		if st.Cards(c) != nil {
			return
		}
	}
}
