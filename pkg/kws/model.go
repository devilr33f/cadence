package kws

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ModelFormat names the model file's layout; a file of another is refused.
const ModelFormat = "cadence-kws/1"

// MaxModelBytes is past any model this package writes (about 200 KiB of JSON): a bigger file is refused unread.
const MaxModelBytes = 1 << 20

// Model is one trained word: its head, the threshold training calibrated, and what the calibration measured on
// held-out data. Its JSON is the file the server stores and the device fetches; it holds no audio.
type Model struct {
	Format string `json:"format"`
	// the word as the owner named it; a label, never matched against speech
	Word      string    `json:"word"`
	Threshold float32   `json:"threshold"`
	Created   time.Time `json:"created"`
	// the takes training used
	Takes int `json:"takes"`
	// the share of held-out takes detected at Threshold
	Recall float64 `json:"recall"`
	// detections per hour on held-out negatives at Threshold, over EvalHours of them
	FalsePerHour float64 `json:"falsePerHour"`
	EvalHours    float64 `json:"evalHours"`
	// codes about the training (take_skipped, ...), never text
	Notes []string `json:"notes,omitempty"`
	Head  *Head    `json:"head"`
}

// ParseModel reads and checks a model file.
func ParseModel(b []byte) (*Model, error) {
	if len(b) > MaxModelBytes {
		return nil, errors.New("kws: model file too large")
	}
	var m Model
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("kws: model file is not json")
	}
	if m.Format != ModelFormat {
		return nil, fmt.Errorf("kws: model format %q, want %q", m.Format, ModelFormat)
	}
	if !(m.Threshold > 0 && m.Threshold < 1) {
		return nil, errors.New("kws: model threshold out of (0, 1)")
	}
	if err := m.Head.Check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Marshal is the model's file.
func (m *Model) Marshal() ([]byte, error) {
	if m.Format == "" {
		m.Format = ModelFormat
	}
	return json.Marshal(m)
}
