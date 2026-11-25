package sim_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

// the sim's stream.pcm plays as a phone named Sim Phone: its pcm's fd goes to audiod beside a BluetoothStream, and its
// removal ends the stream. the adapter is a fake, and locked at the start as a real one is
func TestBluetoothStreamFile(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	if _, ok := b.(board.Bluetooth); !ok {
		t.Fatal("no bluetooth profile")
	}
	audiodIO(t, b, logs)
	waitRecord(t, dir, "bluetooth_config", "enabled=true")
	waitFor(t, "the lock", func() bool { return strings.Contains(logs.String(), "bluetooth_locked") })
	pcm := filepath.Join(dir, "bluetooth", "stream.pcm")
	if err := os.MkdirAll(filepath.Dir(pcm), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pcm, make([]byte, 48000*4), 0o600); err != nil {
		t.Fatal(err)
	}
	id := bluetooth.ShortID("00:11:22:33:44:55", "5A:00:00:00:00:01")
	waitRecord(t, dir, "bluetooth_stream", "id="+id+" rate=48000 channels=2")
	if err := os.Remove(pcm); err != nil {
		t.Fatal(err)
	}
	waitRecord(t, dir, "bluetooth_stream_end", "id="+id)
	if strings.Contains(logs.String(), "Sim Phone") {
		t.Fatalf("the phone's name in the logs: %s", logs)
	}
}
