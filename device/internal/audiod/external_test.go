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

// an audiod hosted outside cadenced (android's app): the supervisor dials it, sends Configure and, after Ready, the
// level first; a connection that ends is dialled again; ctx's end leaves the external audiod listening, untouched
func TestExternalAudiodIsDialledNeverSpawned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audiod.sock")
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Skipf("unixpacket unsupported here: %v", err)
	}
	defer func() { _ = ln.Close() }()
	conns := make(chan *net.UnixConn, 4)
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			conns <- c
		}
	}()

	logs := &recorder{}
	tm := audiod.Timings{DialEvery: 10 * time.Millisecond, BackoffMin: 20 * time.Millisecond, ReadyTimeout: time.Second}
	// a binary that does not exist: it must never be run
	spec := audiod.Spec{Binary: "/nonexistent/cadence-audiod", Socket: path, External: true, Configure: &audiodv1.Configure{
		Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_AAUDIO},
	}}
	sup := audiod.NewSupervisor(spec, 42, slog.New(logs), audiod.WithTimings(tm))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()

	accept := func() *net.UnixConn {
		t.Helper()
		select {
		case c := <-conns:
			return c
		case <-time.After(5 * time.Second):
			t.Fatalf("no dial\n%s", logs.dump())
			return nil
		}
	}
	serve := func(c *net.UnixConn) {
		t.Helper()
		if env := recvEnv(t, c); env.GetConfigure().GetOutput().GetBackend() != audiodv1.Backend_BACKEND_AAUDIO {
			t.Fatalf("first = %v, want the Configure", env)
		}
		sendEnv(t, c, &audiodv1.Envelope{Body: &audiodv1.Envelope_Ready{Ready: &audiodv1.Ready{Version: "app"}}})
		if env := recvEnv(t, c); env.GetVolumeSet().GetLevel() != 42 {
			t.Fatalf("after Ready = %v, want the level", env)
		}
	}

	first := accept()
	serve(first)
	// the app's audiod went away (its thread restarted): dialled again
	_ = first.Close()
	second := accept()
	serve(second)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if logs.count("audiod_start") != 0 || logs.count("audiod_spawn_failed") != 0 {
		t.Fatalf("spawned:\n%s", logs.dump())
	}
	if logs.count("audiod_dial") < 2 || logs.count("audiod_lost") < 1 || logs.count("audiod_detached") != 1 {
		t.Fatalf("logs:\n%s", logs.dump())
	}
	// the listener is still the app's: a new dial is accepted
	c, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatalf("the external audiod's socket is gone: %v", err)
	}
	_ = c.Close()
	_ = second.Close()
}
