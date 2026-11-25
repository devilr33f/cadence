package core_test

import (
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
)

// each hello carries the device's lan addresses as they are then, where a group's members reach it when it leads
func TestHelloCarriesTheLANAddresses(t *testing.T) {
	var n atomic.Int32
	addrs := func() []string {
		if n.Add(1) == 1 {
			return []string{"192.168.1.20", "10.0.0.5"}
		}
		return []string{"192.168.1.21"}
	}
	b := fakeBoard{id: "fake", caps: []capability.Provider{newProvider("a.one")}}
	ls, _ := startSync(t, b, openStore(t), core.WithLANAddresses(addrs))
	p := ls.accept(t)
	if h := p.handshake(false); !slices.Equal(h.GetLanAddresses(), []string{"192.168.1.20", "10.0.0.5"}) {
		t.Fatalf("hello = %v", h)
	}
	// the next link reads them again
	_ = p.c.CloseNow()
	p = ls.accept(t)
	if h := p.handshake(false); !slices.Equal(h.GetLanAddresses(), []string{"192.168.1.21"}) {
		t.Fatalf("second hello = %v", h)
	}
}

type identified struct {
	*fakeProvider
	id string
}

func (p *identified) SetDeviceID(id string) { p.id = id }

// a provider that needs the device's id (media.group's member id) has it before any config applies
func TestDeviceIDReachesTheProvidersThatNeedIt(t *testing.T) {
	p := &identified{fakeProvider: newProvider("media.group")}
	b := fakeBoard{id: "fake", caps: []capability.Provider{p, newProvider("a.one")}}
	if _, err := core.NewSync(b, openStore(t), "v-test", slog.New(slog.DiscardHandler), core.WithDeviceID("dev-1")); err != nil {
		t.Fatal(err)
	}
	if p.id != "dev-1" {
		t.Fatalf("device id = %q", p.id)
	}
}
