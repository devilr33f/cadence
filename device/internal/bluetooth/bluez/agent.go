package bluez

import (
	"strings"

	"github.com/godbus/dbus/v5"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
)

// agent is our org.bluez.Agent1: every request goes to the capability, which accepts inside a pairing window alone
type agent struct{ decide bluetooth.Agent }

var errRejected = dbus.NewError("org.bluez.Error.Rejected", nil)

// address is a device path's address: /org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF
func address(device dbus.ObjectPath) string {
	s := string(device)
	i := strings.LastIndex(s, "/dev_")
	if i < 0 {
		return ""
	}
	return strings.ReplaceAll(s[i+len("/dev_"):], "_", ":")
}

func (a *agent) allow(device dbus.ObjectPath, kind string) bool {
	return a.decide.Authorize(bluetooth.AgentRequest{Device: string(device), Address: address(device), Kind: kind})
}

func (a *agent) pairing(device dbus.ObjectPath) *dbus.Error {
	if !a.allow(device, bluetooth.RequestPairing) {
		return errRejected
	}
	return nil
}

func (*agent) Release() *dbus.Error { return nil }

func (*agent) Cancel() *dbus.Error { return nil }

func (a *agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error { return a.pairing(device) }

func (a *agent) RequestConfirmation(device dbus.ObjectPath, _ uint32) *dbus.Error {
	return a.pairing(device)
}

// a legacy device with no input pairs with the usual fixed pin
func (a *agent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	if err := a.pairing(device); err != nil {
		return "", err
	}
	return "0000", nil
}

func (a *agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	if err := a.pairing(device); err != nil {
		return 0, err
	}
	return 0, nil
}

func (*agent) DisplayPinCode(dbus.ObjectPath, string) *dbus.Error { return nil }

func (*agent) DisplayPasskey(dbus.ObjectPath, uint32, uint16) *dbus.Error { return nil }

func (a *agent) AuthorizeService(device dbus.ObjectPath, _ string) *dbus.Error {
	if !a.allow(device, bluetooth.RequestService) {
		return errRejected
	}
	return nil
}
