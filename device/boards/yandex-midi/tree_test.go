package midi_test

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// tree is a fake midi: the files the board reads and writes, under a temp root. the stock library is an empty file
// of ours, as the board only looks for it
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree {
	t.Helper()
	return &tree{t: t, root: t.TempDir()}
}

// midiTree is a tree with everything the fingerprint checks
func midiTree(t *testing.T) *tree {
	t.Helper()
	tr := newTree(t)
	tr.write("proc/device-tree/model", "Yandex Station Midi\x00")
	for _, n := range []string{"dev/fb0", "dev/snd/controlC0", "dev/snd/pcmC0D0p", "dev/snd/pcmC0D1c",
		"system/vendor/quasar/libs/libyandex_evil.so"} {
		tr.write(n, "")
	}
	tr.write("sys/class/graphics/fb0/virtual_size", "89,1\n")
	tr.write("sys/class/graphics/fb0/bits_per_pixel", "32\n")
	tr.write("sys/class/graphics/fb0/stride", "356\n")
	return tr
}

func (tr *tree) path(rel string) string { return filepath.Join(tr.root, filepath.FromSlash(rel)) }

func (tr *tree) write(rel, text string) {
	tr.t.Helper()
	tr.writeBytes(rel, []byte(text))
}

func (tr *tree) writeBytes(rel string, b []byte) {
	tr.t.Helper()
	p := tr.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr *tree) read(rel string) string {
	tr.t.Helper()
	b, err := os.ReadFile(tr.path(rel))
	if err != nil {
		tr.t.Fatal(err)
	}
	return string(b)
}

func (tr *tree) remove(rel string) {
	tr.t.Helper()
	if err := os.RemoveAll(tr.path(rel)); err != nil {
		tr.t.Fatal(err)
	}
}

// evdev's struct input_event: a timeval of two words, then type, code and value
type ev struct {
	typ, code uint16
	value     int32
}

const (
	evSyn = 0x00
	evKey = 0x01
	evSw  = 0x05

	synReport  = 0
	synDropped = 3

	keyMute         = 113
	keyVolumeDown   = 114
	keyVolumeUp     = 115
	keyProg1        = 148
	keyProg2        = 149
	keyVoiceCommand = 0x246
	swMuteDevice    = 0x0e
)

func encode(events ...ev) []byte {
	word := strconv.IntSize / 8
	var buf bytes.Buffer
	for _, e := range events {
		buf.Write(make([]byte, 2*word))
		_ = binary.Write(&buf, binary.LittleEndian, e.typ)
		_ = binary.Write(&buf, binary.LittleEndian, e.code)
		_ = binary.Write(&buf, binary.LittleEndian, e.value)
	}
	return buf.Bytes()
}

func syn() ev { return ev{evSyn, synReport, 0} }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newLog() (*slog.Logger, *logBuf) {
	buf := &logBuf{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}
