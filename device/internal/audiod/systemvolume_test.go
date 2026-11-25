package audiod_test

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type systemCall struct {
	level          int
	muted, initial bool
}

// where the level is the system volume (android): Ready's system level reaches the handler before the first
// VolumeSet, so a level it lowers is the one that VolumeSet carries; a SystemVolume later reaches it as a change
func TestSystemVolumeReconciledBeforeTheFirstLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audiod.sock")
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Skipf("unixpacket unsupported here: %v", err)
	}
	defer func() { _ = ln.Close() }()
	conns := make(chan *net.UnixConn, 1)
	go func() {
		if c, err := ln.AcceptUnix(); err == nil {
			conns <- c
		}
	}()

	calls := make(chan systemCall, 4)
	var sup *audiod.Supervisor
	tm := audiod.Timings{DialEvery: 10 * time.Millisecond, BackoffMin: 20 * time.Millisecond, ReadyTimeout: time.Second}
	spec := audiod.Spec{Socket: path, External: true, Configure: &audiodv1.Configure{
		Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_AAUDIO, SystemVolume: true},
	}}
	sup = audiod.NewSupervisor(spec, 42, slog.New(&recorder{}), audiod.WithTimings(tm),
		audiod.WithSystemVolume(func(level int, muted, initial bool) {
			calls <- systemCall{level, muted, initial}
			// the board's setter lowers the level to the system's and applies it through SetVolume
			if initial && level < 42 {
				sup.SetVolume(level)
			}
		}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.Run(ctx)

	var c *net.UnixConn
	select {
	case c = <-conns:
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	defer func() { _ = c.Close() }()
	if env := recvEnv(t, c); !env.GetConfigure().GetOutput().GetSystemVolume() {
		t.Fatalf("configure = %v", env)
	}
	sendEnv(t, c, &audiodv1.Envelope{Body: &audiodv1.Envelope_Ready{Ready: &audiodv1.Ready{
		Version: "app", SystemVolume: &audiodv1.SystemVolume{Level: 20, Muted: true},
	}}})
	if env := recvEnv(t, c); env.GetVolumeSet() == nil || env.GetVolumeSet().GetLevel() != 20 {
		t.Fatalf("first after Ready = %v, want the reconciled 20", env)
	}
	if got := <-calls; got != (systemCall{20, true, true}) {
		t.Fatalf("initial = %+v", got)
	}
	sendEnv(t, c, &audiodv1.Envelope{Body: &audiodv1.Envelope_SystemVolume{SystemVolume: &audiodv1.SystemVolume{Level: 55}}})
	select {
	case got := <-calls:
		if got != (systemCall{55, false, false}) {
			t.Fatalf("change = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change")
	}
}
