package remote

import (
	"bytes"
	"context"
	"encoding/json"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// MediaMethod serves devices.media {}: what the household's other devices play; subscribed, the whole list again
// after each change that is more than a position moving on.
func (s *Service) MediaMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		if !emptyArgs(c.Args) {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		ch := s.ch.of()
		es, err := s.entries(ctx, c.DeviceID, false)
		if err != nil {
			s.d.Log.Info("devices_media_failed", "device_id", c.DeviceID)
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		s.d.Log.Info("devices_media", "device_id", c.DeviceID, "items", len(es))
		return calls.Reply{Status: rpc.StatusOK, JSON: items(es), Follow: func(ctx context.Context, st *calls.Stream) {
			last := es
			for {
				if !next(ctx, ch) {
					return
				}
				ch = s.ch.of()
				cur, err := s.entries(ctx, c.DeviceID, false)
				if err != nil || !differ(last, cur, false) {
					continue
				}
				last = cur
				if st.Event(items(cur), nil) != nil {
					return
				}
			}
		}}
	}
}

// PanelMethod serves devices.panel {focus?}: a page of cards, one per item of devices.media (the focused device's
// first, then the caller's room, then by name); subscribed, the page again after each change, a level's included; a
// position only when it jumps (a seek), never as it plays on: the screen runs each position slider's timeline itself.
func (s *Service) PanelMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		var a struct {
			Focus string `json:"focus,omitempty"`
		}
		if len(c.Args) > 0 && !strictArgs(c.Args, &a) || len(a.Focus) > 64 {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		ch := s.ch.of()
		es, page, err := s.panelPage(ctx, c.DeviceID, a.Focus)
		if err != nil {
			s.d.Log.Info("devices_panel_failed", "device_id", c.DeviceID)
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		s.d.Log.Info("devices_panel", "device_id", c.DeviceID, "cards", len(page.GetCards()))
		return calls.Reply{Status: rpc.StatusOK, Page: page, Follow: func(ctx context.Context, st *calls.Stream) {
			last := es
			for {
				if !next(ctx, ch) {
					return
				}
				ch = s.ch.of()
				cur, err := s.entries(ctx, c.DeviceID, true)
				if err != nil || !differ(last, cur, true) {
					continue
				}
				_, p, err := s.panelPage(ctx, c.DeviceID, a.Focus)
				if err != nil {
					continue
				}
				last = cur
				if st.Event(nil, p) != nil {
					return
				}
			}
		}}
	}
}

func (s *Service) panelPage(ctx context.Context, caller, focus string) ([]entry, *cardsv1.Page, error) {
	es, err := s.entries(ctx, caller, true)
	if err != nil {
		return nil, nil, err
	}
	page, err := s.d.Cards.Page(ctx, caller, s.panel(caller, focused(es, focus), s.timeline(ctx, caller)))
	return es, page, err
}

// focused moves the entry focus names (its device, a member's, or its id) first
func focused(es []entry, focus string) []entry {
	if focus == "" {
		return es
	}
	for i, e := range es {
		hit := e.ID == focus || e.DeviceID == focus
		for _, m := range e.members {
			hit = hit || m.id == focus
		}
		if hit {
			out := append([]entry{e}, es[:i]...)
			return append(out, es[i+1:]...)
		}
	}
	return es
}

// emptyArgs is {} or nothing
func emptyArgs(raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && len(m) == 0
}

func strictArgs(raw []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	return !dec.More()
}
