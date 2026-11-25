package remote

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
)

// refusal is the card action's refusal token for err (refusal.<token> in the catalog), "" for none
func refusal(err error) string {
	var dev *commands.DeviceError
	var bad *commands.InvalidArgumentsError
	var inv *groups.InvalidError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, commands.ErrOffline):
		return "offline"
	case errors.Is(err, groups.ErrStereoPair):
		return "stereo_pair"
	case errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return "unsupported"
	case errors.As(err, &dev) && (dev.Code == "unsupported" || dev.Code == "no_session"):
		return "unsupported"
	case errors.As(err, &bad), errors.As(err, &inv), errors.As(err, &dev) && dev.Code == "invalid":
		return "invalid"
	case errors.Is(err, devices.ErrNotFound), errors.Is(err, groups.ErrNotFound):
		return "not_found"
	}
	return "failed"
}

// errGone refuses an action on a card whose session ended, or whose transport no longer lists it
var errGone = &commands.DeviceError{Code: "unsupported"}

// transportBinding sends a transport action to the entry's device (a group's leader), only while its session lists it
func (s *Service) transportBinding(caller, id, action string) cards.Binding {
	return cards.Binding{Idempotent: true, Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
		e, ok := s.find(ctx, caller, id)
		var err error
		if !ok || !slices.Contains(e.actions, action) {
			err = errGone
		} else {
			_, err = s.d.Command(ctx, e.DeviceID, "media.player", action, []byte("{}"))
		}
		return s.outcome("transport", caller, e.DeviceID, action, err)
	}}
}

// seekBinding moves the entry's session (a group's leader's) to the slider's value in seconds, only while the session
// lists seek
func (s *Service) seekBinding(caller, id string) cards.Binding {
	return cards.Binding{Idempotent: true, Run: func(ctx context.Context, a cards.Act) cards.Outcome {
		e, ok := s.find(ctx, caller, id)
		ms := math.Round(a.Value.GetNumber() * 1000)
		var err error
		switch {
		case !ok || !slices.Contains(e.actions, "seek"):
			err = errGone
		case math.IsNaN(ms) || ms < 0 || (e.DurationMs > 0 && ms > float64(e.DurationMs)):
			err = &commands.InvalidArgumentsError{Path: "/positionMs"}
		default:
			args, _ := json.Marshal(map[string]uint64{"positionMs": uint64(ms)})
			_, err = s.d.Command(ctx, e.DeviceID, "media.player", "seek", args)
		}
		return s.outcome("seek", caller, e.DeviceID, "seek", err)
	}}
}

// volumeBinding sets one device's level to the slider's value: the press of an authenticated screen's user, sent
// with no session, as an api caller's (rule 7)
func (s *Service) volumeBinding(caller, dev string) cards.Binding {
	return cards.Binding{Idempotent: true, Run: func(ctx context.Context, a cards.Act) cards.Outcome {
		level := int(math.Round(a.Value.GetNumber()))
		if level < 0 || level > 100 {
			return s.outcome("volume", caller, dev, "", &commands.InvalidArgumentsError{Path: "/level"})
		}
		args, _ := json.Marshal(map[string]int{"level": level})
		_, err := s.d.Command(ctx, dev, "audio.output", "set_level", args)
		return s.outcome("volume", caller, dev, "set_level", err)
	}}
}

// joinBinding has the caller play what the entry plays: into the group it is (its lead left alone), or a new one the
// playing device leads
func (s *Service) joinBinding(caller, id string) cards.Binding {
	return cards.Binding{Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
		e, ok := s.find(ctx, caller, id)
		var err error
		switch {
		case !ok:
			err = errGone
		case e.Here:
		case s.d.Groupable != nil && (!s.d.Groupable(ctx, caller) || !s.d.Groupable(ctx, e.DeviceID)):
			err = errGone
		case e.group != "" && e.kind == groups.KindStereoPair:
			err = groups.ErrStereoPair
		case e.group != "":
			err = s.d.Attach(ctx, e.DeviceID, caller)
		default:
			err = s.d.Join(ctx, e.DeviceID, []string{caller})
		}
		if err == nil {
			s.ch.changed()
		}
		return s.outcome("join", caller, e.DeviceID, "", err)
	}}
}

// leaveBinding takes the caller out of its group
func (s *Service) leaveBinding(caller string) cards.Binding {
	return cards.Binding{Run: func(ctx context.Context, _ cards.Act) cards.Outcome {
		err := s.d.Leave(ctx, caller)
		if err == nil {
			s.ch.changed()
		}
		return s.outcome("leave", caller, "", "", err)
	}}
}

func (s *Service) find(ctx context.Context, caller, id string) (entry, bool) {
	es, err := s.entries(ctx, caller, false)
	if err != nil {
		return entry{}, false
	}
	for _, e := range es {
		if e.ID == id {
			return e, true
		}
	}
	return entry{}, false
}

// outcome logs the action by ids and codes alone and maps its error to a refusal
func (s *Service) outcome(op, caller, target, action string, err error) cards.Outcome {
	code := refusal(err)
	attrs := []any{"device_id", caller, "op", op, "code", cmpOr(code, "ok")}
	if target != "" {
		attrs = append(attrs, "target_id", target)
	}
	if action != "" {
		attrs = append(attrs, "action", action)
	}
	s.d.Log.Info("devices_panel_action", attrs...)
	return cards.Outcome{Refused: code}
}
