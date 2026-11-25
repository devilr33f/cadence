package core_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// namedBoard has a real AirPlay receiver on a fake audiod, and records the names core hands it; as a screen's, it
// also records the DeviceInfo frames its relay takes
type namedBoard struct {
	fakeBoard
	air   *airplay.Airplay
	mu    *sync.Mutex
	names *[]string
	sent  *[]string // the names audiod got
	infos *[]string // the DeviceInfo frames the relay took
}

func newNamedBoard(t *testing.T) namedBoard {
	t.Helper()
	b := namedBoard{mu: &sync.Mutex{}, names: &[]string{}, sent: &[]string{}, infos: &[]string{}}
	b.air = airplay.New(airplay.Options{Dir: "/data/airplay", ScratchDir: "/run/airplay"}, nil)
	b.fakeBoard = fakeBoard{id: "speaker", caps: []capability.Provider{b.air.Capability()}}
	b.air.AirplayReady(func(env *audiodv1.Envelope) error {
		if c := env.GetAirplayConfig(); c != nil {
			b.mu.Lock()
			*b.sent = append(*b.sent, c.GetName())
			b.mu.Unlock()
		}
		return nil
	})
	if err := b.air.Capability().Apply(context.Background(), 1, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b namedBoard) SetDeviceName(name string) {
	b.mu.Lock()
	*b.names = append(*b.names, name)
	b.mu.Unlock()
	b.air.SetDeviceName(name)
}

func (b namedBoard) record() (names, sent, infos []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(*b.names), slices.Clone(*b.sent), slices.Clone(*b.infos)
}

type namedPeer struct{ namedBoard }

func (namedPeer) LinkUp(func(*cadencev2.Envelope) error, *cadencev2.DeviceInfo) {}
func (namedPeer) LinkDown()                                                     {}

func (b namedPeer) LinkMessage(env *cadencev2.Envelope) bool {
	info := env.GetDeviceInfo()
	if info == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.infos = append(*b.infos, info.GetName())
	return true
}

func welcomeNamed(p *peer, name string) {
	p.t.Helper()
	if p.recv().GetHello() == nil {
		p.t.Fatal("first frame is not a hello")
	}
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_Welcome{Welcome: &cadencev2.Welcome{
		ServerVersion: "test", DeviceInfo: &cadencev2.DeviceInfo{Name: name, RoomName: "Bedroom"},
	}}})
}

func rename(p *peer, name string) {
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_DeviceInfo{DeviceInfo: &cadencev2.DeviceInfo{Name: name}}})
}

// Welcome's name and each rename reach the board's receiver, which re-advertises; the same name again sends audiod
// nothing, and the name is kept for the next boot, which hands it to the board before any link
func TestDeviceNameReachesTheReceivers(t *testing.T) {
	b := newNamedBoard(t)
	store := openStore(t)
	ls, buf := startSync(t, b, store)
	p := ls.accept(t)
	welcomeNamed(p, "Bedroom Station 2")
	waitLog(t, buf, "link_up")
	rename(p, "Bedroom Station 2")
	rename(p, "Hall")
	eventually(t, "the rename", func() bool {
		names, _, _ := b.record()
		return len(names) == 2
	})
	names, sent, _ := b.record()
	if !slices.Equal(names, []string{"Bedroom Station 2", "Hall"}) {
		t.Fatalf("names %v", names)
	}
	if !slices.Equal(sent, []string{"Cadence", "Cadence", "Bedroom Station 2", "Hall"}) {
		t.Fatalf("audiod got %v", sent)
	}
	// the store follows the board
	eventually(t, "the name kept", func() bool {
		name, ok, err := store.LoadDeviceName()
		return err == nil && ok && name == "Hall"
	})
	if strings.Contains(buf.String(), "Hall") || strings.Contains(buf.String(), "Bedroom") {
		t.Fatalf("the log names the device: %s", buf)
	}

	// the next boot, before any link
	next := newNamedBoard(t)
	s, err := core.NewSync(next, store, "v-test", newLog(&syncBuf{}))
	if err != nil {
		t.Fatal(err)
	}
	s.Restore(context.Background())
	if names, sent, _ := next.record(); !slices.Equal(names, []string{"Hall"}) || sent[len(sent)-1] != "Hall" {
		t.Fatalf("restored names %v, audiod got %v", names, sent)
	}
}

// a screen's relay still takes the DeviceInfo frame, after the receivers had it
func TestDeviceNameReachesAScreensRelayToo(t *testing.T) {
	b := namedPeer{newNamedBoard(t)}
	ls, buf := startSync(t, b, openStore(t))
	p := ls.accept(t)
	welcomeNamed(p, "Bedroom TV")
	waitLog(t, buf, "link_up")
	rename(p, "Living Room TV")
	eventually(t, "the relay", func() bool {
		_, _, infos := b.record()
		return len(infos) == 1
	})
	names, sent, infos := b.record()
	if !slices.Equal(names, []string{"Bedroom TV", "Living Room TV"}) || !slices.Equal(infos, []string{"Living Room TV"}) ||
		sent[len(sent)-1] != "Living Room TV" {
		t.Fatalf("names %v relay %v audiod %v", names, infos, sent)
	}
}

// a Welcome without DeviceInfo (an older server) keeps the name the store had
func TestWelcomeWithoutDeviceInfoKeepsTheName(t *testing.T) {
	store := openStore(t)
	if err := store.SaveDeviceName("Porch"); err != nil {
		t.Fatal(err)
	}
	b := newNamedBoard(t)
	ls, buf := startSync(t, b, store, func(s *core.Sync) { s.Restore(context.Background()) })
	p := ls.accept(t)
	p.handshake(false)
	waitLog(t, buf, "link_up")
	if names, sent, _ := b.record(); !slices.Equal(names, []string{"Porch"}) || sent[len(sent)-1] != "Porch" {
		t.Fatalf("names %v, audiod got %v", names, sent)
	}
}
