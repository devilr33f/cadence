package audiod_test

import (
	"net"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// fakeAudiod accepts one connection on a real unixpacket socket.
type fakeAudiod struct {
	path string
	conn chan *net.UnixConn
}

func newFake(t *testing.T) *fakeAudiod {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audiod.sock")
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Skipf("unixpacket unsupported here: %v", err)
	}
	f := &fakeAudiod{path: path, conn: make(chan *net.UnixConn, 1)}
	go func() {
		if c, err := ln.AcceptUnix(); err == nil {
			f.conn <- c
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeAudiod) accept(t *testing.T) *net.UnixConn {
	t.Helper()
	c := <-f.conn
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func recvEnv(t *testing.T, c *net.UnixConn) *audiodv1.Envelope {
	t.Helper()
	buf := make([]byte, 20000)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("fake read: %v", err)
	}
	env := &audiodv1.Envelope{}
	if err := proto.Unmarshal(buf[:n], env); err != nil {
		t.Fatal(err)
	}
	return env
}

func sendEnv(t *testing.T, c *net.UnixConn, env *audiodv1.Envelope) {
	t.Helper()
	b, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}
