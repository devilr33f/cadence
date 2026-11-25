package core

import (
	"git.shino.boo/devilreef/cadence/device/internal/board"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// restoreDeviceName hands the board the name kept from the last link, so its receivers advertise it from boot
func (s *Sync) restoreDeviceName() {
	name, ok, err := s.store.LoadDeviceName()
	if err != nil {
		s.log.Error("device_name_restore_failed", "code", "store_unreadable")
		return
	}
	if !ok {
		return
	}
	s.mu.Lock()
	s.name, s.nameKnown = name, true
	s.mu.Unlock()
	if n, ok := s.board.(board.DeviceNamer); ok {
		n.SetDeviceName(name)
	}
}

// deviceInfo takes the server's info on the device (Welcome's, then each rename's): a new name goes to the board and
// is kept for the next boot. A nil info, from a server that sends none, changes nothing. The name is never logged.
func (s *Sync) deviceInfo(info *cadencev2.DeviceInfo) {
	if info == nil {
		return
	}
	name := info.GetName()
	s.mu.Lock()
	same := s.nameKnown && s.name == name
	s.name, s.nameKnown = name, true
	s.mu.Unlock()
	if same {
		return
	}
	if n, ok := s.board.(board.DeviceNamer); ok {
		n.SetDeviceName(name)
	}
	if err := s.store.SaveDeviceName(name); err != nil {
		s.log.Warn("device_name_store_failed", "code", "store_unwritable")
	}
}
