package sim

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
	"git.shino.boo/devilreef/cadence/device/internal/bluetooth/btfake"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

const (
	simPhoneAddr = "5A:00:00:00:00:01"
	simPhoneName = "Sim Phone"
	// s16le, 48 kHz, stereo: 20 ms a write
	simPCMChunk = 48000 * 4 / 50
	simPCMEvery = 20 * time.Millisecond
	simBTPoll   = 250 * time.Millisecond
)

// btSim is the sim's bluetooth: a fake bluez and bluealsa, no radio (rule 7). While <data>/bluetooth/stream.pcm
// exists, a paired phone named Sim Phone streams it, looped and paced as a phone would; its removal ends the stream.
// For tests only.
type btSim struct {
	be   *btfake.Backend
	path string
	log  *slog.Logger
	// the pumps' context: the board's, set before anything runs
	ctx context.Context
}

func newBTSim(dataDir string, log *slog.Logger) *btSim {
	s := &btSim{be: btfake.New(), path: filepath.Join(dataDir, "bluetooth", "stream.pcm"), log: log, ctx: context.Background()}
	s.be.OpenWith(btfake.PCMPath(simPhoneAddr), s.open)
	return s
}

// profile is the board's: its dial hands out the one fake
func (s *btSim) profile() board.BluetoothProfile {
	return board.BluetoothProfile{Adapter: "hci0", Dial: func(context.Context) (bluetooth.Backend, error) { return s.be, nil }}
}

// poll adds the phone, streaming, once the file is there, and takes it away once it is gone
func (s *btSim) poll(ctx context.Context) {
	t := time.NewTicker(simBTPoll)
	defer t.Stop()
	present := false
	for {
		_, err := os.Stat(s.path)
		switch there := err == nil; {
		case there && !present:
			s.be.AddPhone(simPhoneAddr, simPhoneName)
			s.be.SetPCM(simPhoneAddr, func(p *bluetooth.PCM) { p.Running = true })
			present = true
		case !there && present:
			s.be.RemovePhone(simPhoneAddr)
			present = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// open is the pcm's Open: a pipe the file is pumped into at its real rate
func (s *btSim) open() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	go s.pump(s.ctx, w)
	return r, nil
}

// pump writes the file into w, looped, 20 ms at a time, until the file goes, the reader closes or ctx ends
func (s *btSim) pump(ctx context.Context, w *os.File) {
	defer func() { _ = w.Close() }()
	t := time.NewTicker(simPCMEvery)
	defer t.Stop()
	buf := make([]byte, simPCMChunk)
	var f *os.File
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if f == nil {
			var err error
			if f, err = os.Open(s.path); err != nil {
				return
			}
		}
		n, err := io.ReadFull(f, buf)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			_ = f.Close()
			f = nil
		} else if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
	}
}
