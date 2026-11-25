package bluez

import (
	"testing"

	"github.com/godbus/dbus/v5"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
)

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

// bluez's and bluealsa 4.1.1's objects as GetManagedObjects returns them (names and paths per their docs; the
// properties' types per bluez's adapter-api and bluealsa's org.bluealsa.PCM1)
func TestParse(t *testing.T) {
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01")
	objs := managed{
		"/org/bluez": {"org.bluez.AgentManager1": {}},
		"/org/bluez/hci0": {ifaceAdapter: {"Address": v("00:11:22:33:44:55"), "Alias": v("Midi"), "Powered": v(true),
			"Pairable": v(false), "Discoverable": v(false)}},
		dev: {ifaceDevice: {"Address": v("AA:BB:CC:DD:EE:01"), "Alias": v("Pixel"), "Icon": v("phone"), "Class": v(uint32(0x5a020c)),
			"Paired": v(true), "Trusted": v(true), "Connected": v(true), "Adapter": v(dbus.ObjectPath("/org/bluez/hci0"))}},
		dev + "/player0": {ifacePlayer: {"Device": v(dev), "Status": v("playing"), "Position": v(uint32(1500)),
			"Track": v(map[string]dbus.Variant{"Title": v("Song"), "Artist": v("Band"), "Album": v("LP"), "Duration": v(uint32(200000))})}},
		"/org/bluez/hci1/dev_AA_BB_CC_DD_EE_02": {ifaceDevice: {"Address": v("AA:BB:CC:DD:EE:02")}},
	}
	pcms := managed{
		dev + "/a2dpsnk/source": {ifacePCM: {"Device": v(dev), "Transport": v("A2DP-sink"), "Mode": v("source"),
			"Running": v(true), "Sampling": v(uint32(44100)), "Channels": v(byte(2)), "Volume": v(uint16(0x7f7f)),
			"SoftVolume": v(true), "Format": v(uint16(0x8210)), "Codec": v(uint16(0))}},
	}
	s := parse("/org/bluez/hci0", objs, pcms, true)
	if a := s.Adapter; a == nil || a.Address != "00:11:22:33:44:55" || !a.Powered || a.Pairable {
		t.Fatalf("adapter %+v", a)
	}
	if len(s.Devices) != 1 || s.Devices[0] != (bluetooth.Device{Path: string(dev), Address: "AA:BB:CC:DD:EE:01", Name: "Pixel",
		Icon: "phone", Class: 0x5a020c, Paired: true, Trusted: true, Connected: true}) {
		t.Fatalf("devices %+v", s.Devices)
	}
	if len(s.Players) != 1 || s.Players[0] != (bluetooth.Player{Path: string(dev) + "/player0", Device: string(dev), Status: "playing",
		Title: "Song", Artist: "Band", Album: "LP", DurationMs: 200000, PositionMs: 1500}) {
		t.Fatalf("players %+v", s.Players)
	}
	if len(s.PCMs) != 1 || s.PCMs[0] != (bluetooth.PCM{Path: string(dev) + "/a2dpsnk/source", Device: string(dev),
		Transport: "A2DP-sink", Mode: "source", Running: true, Rate: 44100, Channels: 2, Volume: 0x7f7f, SoftVolume: true}) {
		t.Fatalf("pcms %+v", s.PCMs)
	}
	if !s.Bluealsa {
		t.Fatal("no bluealsa")
	}
	if s := parse("/org/bluez/hci0", nil, nil, false); s.Adapter != nil || s.Bluealsa {
		t.Fatalf("empty %+v", s)
	}
}

type decide struct{ yes bool }

func (d decide) Authorize(bluetooth.AgentRequest) bool { return d.yes }

func TestAgent(t *testing.T) {
	if got := address("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01"); got != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("address %q", got)
	}
	no, yes := &agent{decide: decide{false}}, &agent{decide: decide{true}}
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01")
	if no.RequestAuthorization(dev) == nil || no.AuthorizeService(dev, "0000110d-0000-1000-8000-00805f9b34fb") == nil ||
		no.RequestConfirmation(dev, 1234) == nil {
		t.Fatal("accepted outside a window")
	}
	if err := no.RequestAuthorization(dev); err.Name != "org.bluez.Error.Rejected" {
		t.Fatalf("error %v", err)
	}
	if yes.RequestAuthorization(dev) != nil || yes.AuthorizeService(dev, "x") != nil {
		t.Fatal("refused in a window")
	}
	if pin, err := yes.RequestPinCode(dev); err != nil || pin != "0000" {
		t.Fatalf("pin %q %v", pin, err)
	}
}

func TestGone(t *testing.T) {
	if !gone(dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}) || gone(dbus.Error{Name: "org.bluez.Error.Failed"}) || gone(nil) {
		t.Fatal("gone")
	}
}

// a speaker discovery sees: its a2dp sink uuid (any case) makes it a sink, and its rssi is kept
func TestParseSpeaker(t *testing.T) {
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_03")
	objs := managed{
		"/org/bluez/hci0": {ifaceAdapter: {"Address": v("00:11:22:33:44:55")}},
		dev: {ifaceDevice: {"Address": v("AA:BB:CC:DD:EE:03"), "Name": v("JBL"), "Class": v(uint32(0x240414)),
			"UUIDs": v([]string{"0000110B-0000-1000-8000-00805F9B34FB", "0000110e-0000-1000-8000-00805f9b34fb"}),
			"RSSI":  v(int16(-61))}},
		"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_04": {ifaceDevice: {"Address": v("AA:BB:CC:DD:EE:04"),
			"UUIDs": v([]string{"0000110a-0000-1000-8000-00805f9b34fb"})}},
	}
	s := parse("/org/bluez/hci0", objs, nil, true)
	if len(s.Devices) != 2 || !s.Devices[0].AudioSink || s.Devices[0].RSSI != -61 || s.Devices[1].AudioSink {
		t.Fatalf("devices %+v", s.Devices)
	}
}

func TestProfileReason(t *testing.T) {
	if r := profileReason(v([]string{"A2DP-source", "A2DP-sink"})); r != "" {
		t.Fatalf("reason %q", r)
	}
	if r := profileReason(v([]string{"A2DP-sink"})); r != OutputNoProfile {
		t.Fatalf("reason %q", r)
	}
	if r := profileReason(v(uint32(1))); r != OutputNoProfile {
		t.Fatalf("reason %q", r)
	}
}

// a speaker whose transport shows Volume takes avrcp absolute volume, and its source pcm says so
func TestParseHardVolume(t *testing.T) {
	jbl := dbus.ObjectPath("/org/bluez/hci0/dev_78_66_F3_53_D8_03")
	old := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_03")
	objs := managed{
		"/org/bluez/hci0": {ifaceAdapter: {"Address": v("00:11:22:33:44:55")}},
		jbl:               {ifaceDevice: {"Address": v("78:66:F3:53:D8:03"), "Connected": v(true)}},
		jbl + "/sep1/fd0": {ifaceTransport: {"Device": v(jbl), "State": v("active"), "Volume": v(uint16(68)), "Delay": v(uint16(1450))}},
		old:               {ifaceDevice: {"Address": v("AA:BB:CC:DD:EE:03"), "Connected": v(true)}},
		old + "/sep1/fd1": {ifaceTransport: {"Device": v(old), "State": v("idle")}},
	}
	src := func(dev dbus.ObjectPath) map[string]map[string]dbus.Variant {
		return map[string]map[string]dbus.Variant{ifacePCM: {"Device": v(dev), "Transport": v("A2DP-source"), "Mode": v("sink"),
			"Volume": v(uint16(0x7f7f)), "SoftVolume": v(true)}}
	}
	pcms := managed{jbl + "/a2dpsrc/sink": src(jbl), old + "/a2dpsrc/sink": src(old)}
	s := parse("/org/bluez/hci0", objs, pcms, true)
	if len(s.PCMs) != 2 || s.PCMs[0].Device != string(jbl) || !s.PCMs[0].HardVolume || s.PCMs[1].HardVolume {
		t.Fatalf("pcms %+v", s.PCMs)
	}
	// MediaTransport1.Delay is in 1/10 ms; an output without it carries no hint
	if !s.PCMs[0].DelayKnown || s.PCMs[0].DelayHintUs != 145000 || s.PCMs[1].DelayKnown {
		t.Fatalf("delays %+v", s.PCMs)
	}
}

func TestPlayerProps(t *testing.T) {
	p := playerProps()
	for _, k := range []string{"PlaybackStatus", "CanControl", "CanPlay", "CanPause", "CanGoNext", "CanGoPrevious"} {
		if p[k] == nil {
			t.Errorf("no %s", k)
		}
	}
	if p["Volume"] != nil {
		t.Error("a Volume: the transport's absolute volume is bluealsa's")
	}
	if _, ok := p["Position"].Value.(int64); !ok {
		t.Error("Position is not an int64, which bluez reads as x")
	}
}
