package pickle_test

import (
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/input"
)

// a node that stays open, as evdev's do: events arrive while the reader waits, and closing the board ends it quietly.
// the real ioctl runs against it and fails, as a node that cannot tell the switch does
func TestLiveEventsUntilClose(t *testing.T) {
	tr := pickleTree(t)
	tr.write("sys/class/input/event1/device/name", "mute\n")
	node := tr.path("dev/input/event1")
	if err := os.MkdirAll(tr.path("dev/input"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(node, 0o600); err != nil {
		t.Fatal(err)
	}
	// read-write, so opening it waits for no reader and the board's reader never sees an end
	w, err := os.OpenFile(node, os.O_RDWR, 0) //nolint:gosec // the test's own fifo
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	b, logs := newBoard(t, tr)
	events := pickle.ButtonEvents(b)
	waitLog(t, logs, "msg=mute_switch_unknown")
	if _, err := w.Write(encode(ev{evSw, swMuteDevice, 1}, syn())); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, 1); !slices.Equal(got, []press{{input.MuteOn, true}}) {
		t.Fatalf("events = %v", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "input_lost") {
		t.Fatalf("a close on purpose logged a loss: %q", logs.String())
	}
}
