package bluetooth_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func code(err error) string {
	var ce *capability.CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// every start registers our agent and locks the adapter bluez's main.conf leaves always pairable
func TestStartLocksTheAdapter(t *testing.T) {
	h := newHarness(t)
	h.waitCall("SetDiscoverable false")
	calls := h.be.Calls()
	if !slices.Equal(calls, []string{"RegisterAgent", "SetAlias Cadence", "SetPairable false", "SetDiscoverable false"}) {
		t.Fatalf("calls %q", calls)
	}
	waitFor(t, "a locked state", func() bool {
		s := h.state()
		a, _ := s["adapter"].(map[string]any)
		return a != nil && a["present"] == true && a["pairable"] == false && a["discoverable"] == false && s["code"] == ""
	})
	// the device's name is the alias while the config sets none
	h.b.SetDeviceName("Кухня")
	h.waitCall("SetAlias Кухня")
}

// a pair window needs a woken session; bluez's timeouts are set before it opens, our agent accepts inside it alone,
// and a device paired in it is trusted
func TestPairWindow(t *testing.T) {
	h := newHarness(t)
	h.waitCall("SetDiscoverable false")
	if h.be.PairIncoming(phoneAddr, phoneName) {
		t.Fatal("paired outside a window")
	}
	h.waitLog(`"msg":"bluetooth_pair_refused","id":"` + h.phoneID() + `","kind":"pairing","reason":"no_window"`)
	for _, args := range []string{`{"sessionId":"follow-1"}`, `{"sessionId":"nobody"}`} {
		if _, err := h.command("pair_window", args); code(err) != "wake_required" {
			t.Fatalf("%s: %v", args, err)
		}
	}
	if _, err := h.command("pair_window", `{"seconds":10,"sessionId":"wake-1"}`); code(err) != "invalid" {
		t.Fatalf("10 s: %v", err)
	}
	out, err := h.command("pair_window", `{"sessionId":"wake-1"}`)
	if err != nil || out != `{"seconds":120,"name":"Cadence"}` {
		t.Fatalf("pair_window = %s, %v", out, err)
	}
	calls := h.be.Calls()
	if i := slices.Index(calls, "SetWindowTimeouts 120"); i < 0 || !slices.Equal(calls[i:i+3], []string{"SetWindowTimeouts 120", "SetPairable true", "SetDiscoverable true"}) {
		t.Fatalf("calls %q", calls)
	}
	h.waitLog(`"msg":"bluetooth_window_open","seconds":120`)
	if !h.be.PairIncoming(phoneAddr, phoneName) {
		t.Fatal("refused in a window")
	}
	h.waitCall("SetTrusted true " + btfake.DevicePath(phoneAddr))
	h.waitLog(`"msg":"bluetooth_paired","id":"` + h.phoneID() + `","kind":"phone"`)
	// the window ends on our clock, and the adapter is locked again
	n := len(h.be.Calls())
	h.clock.add(121 * time.Second)
	h.b.SetDeviceName("")
	h.waitLog(`"msg":"bluetooth_window_closed","reason":"timeout"`)
	waitFor(t, "the lock", func() bool {
		return slices.Contains(h.be.Calls()[n:], "SetPairable false") && slices.Contains(h.be.Calls()[n:], "SetDiscoverable false")
	})
	if h.be.PairIncoming("AA:BB:CC:DD:EE:02", "other") {
		t.Fatal("paired after the window")
	}
}

// bluez's own timeout closing discoverable ends the window too
func TestWindowEndsWithBluez(t *testing.T) {
	h := newHarness(t)
	h.waitCall("SetDiscoverable false")
	if _, err := h.command("pair_window", `{"seconds":30,"sessionId":"wake-1"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "an open window", func() bool {
		w, _ := h.state()["window"].(map[string]any)
		return w != nil && w["open"] == true
	})
	h.be.Update(func(s *bluetooth.Snapshot) { s.Adapter.Discoverable, s.Adapter.Pairable = false, false })
	h.waitLog(`"msg":"bluetooth_window_closed","reason":"bluez"`)
}

// a running a2dp sink pcm goes to audiod as a stream with its fd: soft volume off first, the phone's name as the
// sender, its player mirrored; its stop ends the stream
func TestStreamGoesToAudiod(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.AddPhone(phoneAddr, phoneName)
	h.be.SetPCM(phoneAddr, func(p *bluetooth.PCM) { p.Running = true })
	pcm := btfake.PCMPath(phoneAddr)
	h.waitCall("Open " + pcm)
	calls := h.be.Calls()
	if i := slices.Index(calls, "SetSoftVolume false "+pcm); i < 0 || i > slices.Index(calls, "Open "+pcm) {
		t.Fatalf("calls %q", calls)
	}
	waitFor(t, "the stream", func() bool { return h.audiod.fd(0) != nil })
	envs := h.audiod.all()
	if c := envs[0].GetBluetoothConfig(); c == nil || !c.GetEnabled() {
		t.Fatalf("first frame %v", envs[0])
	}
	st := envs[1].GetBluetoothStream()
	if st.GetStreamId() != h.phoneID() || st.GetRate() != 48000 || st.GetChannels() != 2 || st.GetSender() != phoneName {
		t.Fatalf("stream %v", st)
	}
	// audiod's copy reads what bluealsa writes
	if _, err := h.be.Writer(pcm).Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if n, err := h.audiod.fd(0).Read(buf); n != 4 || err != nil || buf[3] != 4 {
		t.Fatalf("read %d %v", n, err)
	}
	h.waitLog(`"msg":"bluetooth_stream_open","id":"` + h.phoneID() + `","rate":48000,"channels":2`)
	waitFor(t, "no player yet: playing while pcm flows", func() bool {
		return slices.ContainsFunc(h.audiod.all(), func(e *audiodv1.Envelope) bool {
			p := e.GetBluetoothPlayer()
			return p != nil && p.GetPlaying() && p.GetMetadata() == nil
		})
	})
	h.be.SetPlayer(phoneAddr, bluetooth.Player{Status: "paused", Title: "Song", Artist: "Band", DurationMs: 180000, PositionMs: 5000})
	waitFor(t, "the player", func() bool {
		return slices.ContainsFunc(h.audiod.all(), func(e *audiodv1.Envelope) bool {
			p := e.GetBluetoothPlayer()
			return p != nil && !p.GetPlaying() && p.GetMetadata().GetTitle() == "Song" && p.GetDurationMs() == 180000 && p.GetPositionMs() == 5000
		})
	})
	h.b.BluetoothEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothControl{BluetoothControl: &audiodv1.BluetoothControl{
		StreamId: h.phoneID(), Action: audiodv1.MediaAction_MEDIA_ACTION_RESUME,
	}}})
	h.waitCall("Play " + btfake.DevicePath(phoneAddr) + "/player0")
	h.be.SetPCM(phoneAddr, func(p *bluetooth.PCM) { p.Running = false })
	waitFor(t, "the end", func() bool {
		return slices.ContainsFunc(h.audiod.all(), func(e *audiodv1.Envelope) bool {
			return e.GetBluetoothStreamEnd().GetStreamId() == h.phoneID()
		})
	})
	h.waitLog(`"msg":"bluetooth_stream_end","id":"` + h.phoneID() + `","reason":"idle"`)
}

// a new audiod connection gets the config again and a phone still streaming is opened on it
func TestStreamReopensOnTheNextAudiod(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.AddPhone(phoneAddr, phoneName)
	h.be.SetPCM(phoneAddr, func(p *bluetooth.PCM) { p.Running = true })
	waitFor(t, "the stream", func() bool { return h.audiod.fd(0) != nil })
	h.b.BluetoothGone()
	h.waitLog(`"reason":"audiod"`)
	h.ready()
	waitFor(t, "the stream again", func() bool { return h.audiod.fd(1) != nil })
	n := 0
	for _, e := range h.audiod.all() {
		if e.GetBluetoothConfig() != nil {
			n++
		}
		if e.GetBluetoothStreamEnd() != nil {
			t.Fatalf("an end sent to a gone audiod: %v", e)
		}
	}
	if n != 2 {
		t.Fatalf("%d configs", n)
	}
}

// off: the adapter powered off (attack surface), streams dropped, audiod told, windows refused
func TestDisabled(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.AddPhone(phoneAddr, phoneName)
	h.be.SetPCM(phoneAddr, func(p *bluetooth.PCM) { p.Running = true })
	waitFor(t, "the stream", func() bool { return h.audiod.fd(0) != nil })
	if err := h.b.Capability().Apply(context.Background(), 2, []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	h.waitCall("SetPowered false")
	h.waitLog(`"reason":"off"`)
	if !slices.ContainsFunc(h.audiod.all(), func(e *audiodv1.Envelope) bool {
		c := e.GetBluetoothConfig()
		return c != nil && !c.GetEnabled()
	}) {
		t.Fatal("audiod not told")
	}
	if _, err := h.command("pair_window", `{"sessionId":"wake-1"}`); code(err) != "unavailable" {
		t.Fatalf("pair_window: %v", err)
	}
	if err := h.b.Capability().Apply(context.Background(), 3, []byte(`{"receive":{"enabled":false}}`)); err != nil {
		t.Fatal(err)
	}
	h.waitCall("SetPowered true")
	if _, err := h.command("pair_window", `{"sessionId":"wake-1"}`); code(err) != "receive_off" {
		t.Fatalf("pair_window: %v", err)
	}
}

func TestConfig(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{`[]`, `{"enabled":null}`, `{"x":1}`, `{"pairWindowSeconds":29}`, `{"pairWindowSeconds":301}`,
		`{"name":"` + strings.Repeat("я", 33) + `"}`, `{"receive":{"x":true}}`, `{"receive":true}`} {
		if err := h.b.Capability().Apply(context.Background(), 1, []byte(bad)); err == nil {
			t.Fatalf("%s applied", bad)
		}
	}
	if err := h.b.Capability().Apply(context.Background(), 1, []byte(`{"name":"Гостиная","pairWindowSeconds":60}`)); err != nil {
		t.Fatal(err)
	}
	h.waitCall("SetAlias Гостиная")
	out, err := h.command("pair_window", `{"sessionId":"wake-1"}`)
	if err != nil || out != `{"seconds":60,"name":"Гостиная"}` {
		t.Fatalf("%s %v", out, err)
	}
}

// a phone's first volume is answered with our level, never applied; a later move goes to the sender policy, our
// own write's echo aside; and the policy's level goes back to the phone
func TestVolume(t *testing.T) {
	h := newHarness(t)
	pcm := btfake.PCMPath(phoneAddr)
	h.be.AddPhone(phoneAddr, phoneName)
	// 30 % of 127
	h.waitCall("SetVolume 38 " + pcm)
	if got := h.senders.got(); len(got) != 0 {
		t.Fatalf("the first value applied: %v", got)
	}
	// the phone's slider to 64 of 127: 50 %
	h.be.SetPCM(phoneAddr, func(p *bluetooth.PCM) { p.Volume = 64<<8 | 64 })
	waitFor(t, "the move", func() bool { return slices.Equal(h.senders.got(), []uint32{50}) })
	// the policy refused it (a ring): the level in force goes back
	n := len(h.be.Calls())
	h.setLevel(30)
	waitFor(t, "the level back", func() bool { return slices.Contains(h.be.Calls()[n:], "SetVolume 38 "+pcm) })
	// a command's level reaches the phone too, and its echo moves nothing
	h.setLevel(100)
	h.waitCall("SetVolume 127 " + pcm)
	time.Sleep(50 * time.Millisecond)
	if got := h.senders.got(); !slices.Equal(got, []uint32{50}) {
		t.Fatalf("moves %v", got)
	}
}

func TestCommands(t *testing.T) {
	h := newHarness(t)
	h.be.AddPhone(phoneAddr, phoneName)
	id := h.phoneID()
	waitFor(t, "the phone in state", func() bool {
		d, _ := h.state()["devices"].([]any)
		return len(d) == 1
	})
	out, err := h.command("list", `{}`)
	want := `{"devices":[{"id":"` + id + `","name":"` + phoneName + `","kind":"phone","paired":true,"connected":true,"playing":false}]}`
	if err != nil || out != want {
		t.Fatalf("list = %s, %v", out, err)
	}
	for _, c := range []struct{ name, args, code string }{
		{"connect", `{"id":"` + id + `","sessionId":"follow-1"}`, "wake_required"},
		{"connect", `{"id":"` + id + `","sessionId":""}`, "invalid"},
		{"connect", `{"id":"` + id + `","sessionId":null}`, "invalid"},
		{"connect", `{"id":"000000000000","sessionId":"wake-1"}`, "unknown_device"},
		{"connect", `{"id":"AA","sessionId":"wake-1"}`, "invalid"},
		{"forget", `{"id":"` + id + `","all":true,"sessionId":"wake-1"}`, "invalid"},
		{"forget", `{"all":false,"sessionId":"wake-1"}`, "invalid"},
		{"forget", `{"all":true,"sessionId":"follow-1"}`, "wake_required"},
	} {
		if _, err := h.command(c.name, c.args); code(err) != c.code {
			t.Fatalf("%s %s: %v, want %s", c.name, c.args, err, c.code)
		}
	}
	// a session of another device, vouched for by the server for this one command
	vouched := capability.WithVouched(context.Background(), capability.Vouched{CallerDeviceID: "other", SessionID: "remote-1",
		Trigger: cadencev2.Trigger_TRIGGER_WAKE})
	raw, err := h.b.Capability().Command(vouched, "disconnect", []byte(`{"id":"`+id+`","sessionId":"remote-1"}`))
	if err != nil || string(raw) != `{"connected":false}` {
		t.Fatalf("disconnect = %s, %v", raw, err)
	}
	h.be.Fail("Connect")
	if _, err := h.command("connect", `{"id":"`+id+`","sessionId":"wake-1"}`); code(err) != "connect_failed" {
		t.Fatalf("connect: %v", err)
	}
	h.be.AddPhone("AA:BB:CC:DD:EE:02", "Laptop")
	if out, err := h.command("forget", `{"all":true,"sessionId":"wake-1"}`); err != nil || out != `{"forgotten":2}` {
		t.Fatalf("forget = %s, %v", out, err)
	}
	h.waitLog(`"msg":"bluetooth_forget","removed":2,"kept":0`)
}

// a bus that cannot be dialed is bus_failed, and an adapter bluez does not have no_adapter
func TestCodes(t *testing.T) {
	b := bluetooth.New(bluetooth.Options{
		Dial: func(context.Context) (bluetooth.Backend, error) { return nil, errors.New("no bus") }, RetryEvery: time.Hour,
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	waitFor(t, "bus_failed", func() bool {
		select {
		case v := <-b.Capability().State():
			return strings.Contains(string(v), `"code":"bus_failed"`)
		default:
			return false
		}
	})
	h := newHarness(t)
	h.waitCall("SetDiscoverable false")
	h.be.Update(func(s *bluetooth.Snapshot) { s.Adapter = nil })
	waitFor(t, "no_adapter", func() bool { return h.state()["code"] == "no_adapter" })
	if _, err := h.command("pair_window", `{"sessionId":"wake-1"}`); code(err) != "unavailable" {
		t.Fatalf("pair_window: %v", err)
	}
}

// no session at all is the owner's authenticated call (the dashboard and its chat), as audio.output takes it: the
// pi has no microphone of its own, so this is how its owner opens a window
func TestBluetoothOwnerCallNeedsNoSession(t *testing.T) {
	h := newHarness(t)
	h.waitCall("SetDiscoverable false")
	out, err := h.command("pair_window", `{}`)
	if err != nil || !strings.Contains(out, `"seconds":`) {
		t.Fatalf("pair_window = %s, %v", out, err)
	}
}
