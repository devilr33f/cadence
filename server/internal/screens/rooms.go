// Package screens makes a speaker and a screen in one room one assistant (W0-C
// items 9a, 13 and 18): a turn on the room's speaker reaches the room's
// screens as AssistantTurn states with its steps, reply and cards; the room's
// playing devices duck while it runs; a screen's assistant button opens the
// room speaker's microphone (MicOpen) for a pressed session; and each
// screen's ScreenContext is kept in memory for the agent's notes.
//
// Transcripts, replies, card text, refs and queries are shown or handed to
// the model, never logged (rule 8): lines carry ids, codes and counts.
package screens

import (
	"context"
	"encoding/json"
	"slices"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Directory says which room each device is in: the device's room field, which
// the owner sets at a claim's approval or in the dashboard (stream H).
type Directory interface {
	// Room is the device's room, "" when it has none.
	Room(ctx context.Context, deviceID string) string
	// Devices lists every enrolled device.
	Devices(ctx context.Context) []string
}

// Screen is what a screen's display.screen state says about it.
type Screen struct {
	// MicKind is none, speaker or local: which microphone serves the screen
	MicKind string
	// MicDevice is the speaker's id when MicKind is speaker
	MicDevice  string
	DrawerOpen bool
	// Off says the screen's display is off (its power state says so)
	Off bool
}

// ParseScreen reads a display.screen state; false for a device whose state
// does not say it is a screen.
func ParseScreen(raw json.RawMessage) (Screen, bool) {
	var st struct {
		Screen     bool   `json:"screen"`
		DrawerOpen bool   `json:"drawerOpen"`
		Power      string `json:"power"`
		Mic        *struct {
			Kind     string `json:"kind"`
			DeviceID string `json:"deviceId"`
		} `json:"mic"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &st) != nil || !st.Screen {
		return Screen{}, false
	}
	s := Screen{DrawerOpen: st.DrawerOpen, Off: st.Power == "off" || st.Power == "standby"}
	if st.Mic != nil {
		s.MicKind, s.MicDevice = st.Mic.Kind, st.Mic.DeviceID
	}
	return s, true
}

// StateFunc reads a device's capability state (devices.Service.State).
type StateFunc func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)

func (s *Service) screen(ctx context.Context, dev string) (Screen, bool) {
	if s.d.State == nil {
		return Screen{}, false
	}
	raw, ok := s.d.State(ctx, dev, capschema.DisplayScreenID)
	if !ok {
		return Screen{}, false
	}
	return ParseScreen(raw)
}

// room is everything a turn of origin reaches: the online screens of its
// room (origin itself when it is one) and the room's other online devices
func (s *Service) room(ctx context.Context, origin string) (screens, others []string) {
	if _, ok := s.screen(ctx, origin); ok {
		screens = append(screens, origin)
	}
	room := s.d.Directory.Room(ctx, origin)
	if room == "" {
		return screens, nil
	}
	devs := s.d.Directory.Devices(ctx)
	slices.Sort(devs)
	for _, id := range devs {
		if id == origin || !s.d.Online(id) || s.d.Directory.Room(ctx, id) != room {
			continue
		}
		if _, ok := s.screen(ctx, id); ok {
			screens = append(screens, id)
		} else {
			others = append(others, id)
		}
	}
	return screens, others
}

// speaker is the microphone that serves a screen: the one its state names,
// else the first online device of its room that is no screen. A state that
// says mic none is the owner's pick (design §21): no microphone serves this
// screen, whatever its room holds.
func (s *Service) speaker(ctx context.Context, screen string, st Screen) (string, string) {
	switch st.MicKind {
	case "none":
		return "", "no_microphone"
	case "speaker":
		if st.MicDevice == "" {
			break
		}
		if !s.d.Online(st.MicDevice) {
			return "", "mic_offline"
		}
		return st.MicDevice, ""
	}
	_, others := s.room(ctx, screen)
	if len(others) == 0 {
		return "", "no_microphone"
	}
	return others[0], ""
}

// speakerMic is the MicSource of a turn a speaker hears: its id and the name its drawer line shows (never logged)
func (s *Service) speakerMic(ctx context.Context, speaker string) *cadencev2.MicSource {
	m := &cadencev2.MicSource{Kind: cadencev2.MicSourceKind_MIC_SOURCE_KIND_SPEAKER, DeviceId: speaker}
	if s.d.Name != nil {
		m.DeviceName = s.d.Name(ctx, speaker)
	}
	return m
}

// RoomScreens are the online screens a device's turns and notices show on:
// its room's, itself when it is one.
func (s *Service) RoomScreens(ctx context.Context, deviceID string) []string {
	screens, _ := s.room(ctx, deviceID)
	return screens
}
