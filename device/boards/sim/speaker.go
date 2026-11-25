package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opusframes"
)

// Begin opens <dir>/out/<stream_id>.opusframes. The id comes from the server,
// so it must be a plain file name.
func (a *simAudio) Begin(s *cadencev2.StreamStart) (audio.StreamWriter, error) {
	id := s.GetStreamId()
	if !streamIDChars.MatchString(id) {
		return nil, errBadStream
	}
	out := filepath.Join(a.dir, "out")
	if err := os.MkdirAll(out, 0o750); err != nil {
		return nil, fmt.Errorf("sim: create out dir: %w", err)
	}
	f, err := os.Create(filepath.Join(out, id+".opusframes")) //nolint:gosec // id is validated
	if err != nil {
		return nil, fmt.Errorf("sim: create stream file: %w", err)
	}
	return &fileWriter{f: f, summary: filepath.Join(out, id+".json"), purpose: purposeName(s.GetPurpose())}, nil
}

func purposeName(p cadencev2.StreamPurpose) string {
	switch p {
	case cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY:
		return "reply"
	case cadencev2.StreamPurpose_STREAM_PURPOSE_ANNOUNCE:
		return "announce"
	}
	return "other"
}

type fileWriter struct {
	f       *os.File
	summary string
	purpose string
	packets int
	closed  bool
}

func (w *fileWriter) Write(_ uint32, p []byte) error {
	if err := opusframes.Write(w.f, p); err != nil {
		return err
	}
	w.packets++
	return nil
}

func (w *fileWriter) End() error {
	w.closed = true
	if err := w.f.Close(); err != nil {
		return err
	}
	return writeSummary(w.summary, w.packets, w.purpose)
}

func writeSummary(path string, packets int, purpose string) error {
	b, err := json.Marshal(struct {
		Packets    int    `json:"packets"`
		DurationMS int    `json:"duration_ms"`
		Purpose    string `json:"purpose"`
	}{packets, packets * 20, purpose})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b)
}

// Abort drops the stream: the file is closed and no summary is written.
func (w *fileWriter) Abort() {
	if !w.closed {
		w.closed = true
		_ = w.f.Close()
	}
}
