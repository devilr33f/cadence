package agent

import (
	"slices"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// modeIdle ends a mode set by voice under after_idle.
const modeIdle = 10 * time.Minute

// ModeState keeps, in memory, the mode each device was switched to by voice
// and the last session each device ended.
type ModeState struct {
	mu     sync.Mutex
	voiced map[string]voicedMode
	ended  map[string]string
}

type voicedMode struct {
	mode, session string
	lastTurn      time.Time
	sessionEnded  bool
}

func NewModeState() *ModeState {
	return &ModeState{voiced: map[string]voicedMode{}, ended: map[string]string{}}
}

// Current returns the device's mode for a turn at now and counts now as its
// latest turn, so call it exactly once per turn, at the turn's start; read it
// anywhere else with Peek. A mode set by voice holds until the device's revert
// policy (after_idle, the default; after_session; never) ends it or it is no
// longer allowed, and then it is forgotten; otherwise the mode setting
// applies, or the first allowed mode when the setting is not allowed, else
// fast.
func (s *ModeState) Current(deviceID string, v settings.Values, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	vm, ok := s.voiced[deviceID]
	if ok && !holds(vm, v, now) {
		delete(s.voiced, deviceID)
		ok = false
	}
	if !ok {
		return settingMode(v)
	}
	vm.lastTurn = now
	s.voiced[deviceID] = vm
	return vm.mode
}

// Peek returns what Current would, and changes nothing.
func (s *ModeState) Peek(deviceID string, v settings.Values, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if vm, ok := s.voiced[deviceID]; ok && holds(vm, v, now) {
		return vm.mode
	}
	return settingMode(v)
}

// Set records a mode chosen by voice in a turn of sessionID at now. A set that
// arrives after that session already ended counts as ended.
func (s *ModeState) Set(deviceID, sessionID, mode string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.ended[deviceID]
	s.voiced[deviceID] = voicedMode{mode: mode, session: sessionID, lastTurn: now, sessionEnded: ok && last == sessionID}
}

// EndSession records that a device's session ended; under after_session that
// ends a mode this session set. The end of any other session changes nothing.
func (s *ModeState) EndSession(deviceID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended[deviceID] = sessionID
	if vm, ok := s.voiced[deviceID]; ok && vm.session == sessionID {
		vm.sessionEnded = true
		s.voiced[deviceID] = vm
	}
}

func holds(vm voicedMode, v settings.Values, now time.Time) bool {
	return !reverted(vm, v.ModeRevert, now) && slices.Contains(v.AllowedModes, vm.mode)
}

func reverted(vm voicedMode, policy string, now time.Time) bool {
	switch policy {
	case "never":
		return false
	case "after_session":
		return vm.sessionEnded
	default:
		return now.Sub(vm.lastTurn) >= modeIdle
	}
}

func settingMode(v settings.Values) string {
	switch {
	case slices.Contains(v.AllowedModes, v.Mode):
		return v.Mode
	case len(v.AllowedModes) > 0:
		return v.AllowedModes[0]
	default:
		return "fast"
	}
}
