package vkcapsula_test

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

// tree is a fake capsula: the files the board reads and writes, under a temp root
type tree struct {
	t    *testing.T
	root string
}

// capsulaTree has everything the fingerprint checks
func capsulaTree(t *testing.T) *tree {
	t.Helper()
	tr := &tree{t: t, root: t.TempDir()}
	tr.write("proc/device-tree/compatible", "amlogic, axg\x00")
	tr.write("dev/topleds", "")
	tr.write("sys/class/leds/clock-led1/brightness", "0\n")
	tr.write("sys/class/leds/clock-led30/brightness", "0\n")
	tr.write("sys/class/input/event1/device/name", "aml_vkeypad\n")
	tr.write("sys/class/input/event0/device/name", "csk05t\n")
	tr.write("proc/asound/cards", " 0 [AMLAUGESOUND  ]: AML-AUGESOUND - AML-AUGESOUND\n")
	tr.write("usr/share/voice-agent/model.bin", "model")
	return tr
}

func (tr *tree) path(rel string) string { return filepath.Join(tr.root, filepath.FromSlash(rel)) }

func (tr *tree) write(rel, text string) { tr.t.Helper(); tr.writeBytes(rel, []byte(text)) }

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

	keyA          = 30
	keyVolumeDown = 114
	keyVolumeUp   = 115
	keyPower      = 116
	keyPlayPause  = 164
	keyMicMute    = 248
	keyCentre     = 704
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

func syn() ev { return ev{evSyn, 0, 0} }

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
