package audiod_test

import (
	"context"
	"os"
	"syscall"
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// a bluetooth stream's pcm fd rides in the same frame as its BluetoothStream: audiod reads the pipe through its own
// copy while cadenced closes its own
func TestSendWithFDPassesThePipe(t *testing.T) {
	f := newFake(t)
	c, err := audiod.Dial(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	conn := f.accept(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	env := &audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothStream{BluetoothStream: &audiodv1.BluetoothStream{
		StreamId: "a1b2c3d4e5f6", Rate: 44100, Channels: 2,
	}}}
	if err := c.SendWithFD(env, int(r.Fd())); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	buf, oob := make([]byte, 4096), make([]byte, syscall.CmsgSpace(4*4))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	got := &audiodv1.Envelope{}
	if err := proto.Unmarshal(buf[:n], got); err != nil || !proto.Equal(got, env) {
		t.Fatalf("frame %v, %v", got, err)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		t.Fatalf("control messages %d, %v", len(msgs), err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("fds %v, %v", fds, err)
	}
	passed := os.NewFile(uintptr(fds[0]), "pcm")
	defer func() { _ = passed.Close() }()
	if _, err := w.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 4)
	if n, err := passed.Read(pcm); err != nil || n != 4 || pcm[3] != 4 {
		t.Fatalf("read %d %v %v", n, pcm, err)
	}
	// a plain frame carries none
	if err := c.Send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Ping{Ping: &audiodv1.HealthPing{Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, oobn, _, _, err := conn.ReadMsgUnix(buf, oob); err != nil || oobn != 0 {
		t.Fatalf("oob %d, %v", oobn, err)
	}
}

func TestSendWithFDRefusesNoFD(t *testing.T) {
	f := newFake(t)
	c, err := audiod.Dial(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.SendWithFD(&audiodv1.Envelope{}, -1); err == nil {
		t.Fatal("sent without an fd")
	}
}
