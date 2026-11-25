package fake

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	toneHz     = 440
	toneMillis = 300
	toneAmp    = 0.3
)

var ttsModule = providers.Module{
	ID:           "fake-tts",
	Kind:         providers.KindTTS,
	Probe:        probe,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{"failAfterSpeaks":{"type":"integer","minimum":0}}}`,
	New: func(cfg json.RawMessage, _ map[string]string) (any, error) {
		var c struct {
			FailAfterSpeaks int `json:"failAfterSpeaks"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		return &tts{failAfter: c.FailAfterSpeaks}, nil
	},
}

type tts struct{ failAfter int }

func (t *tts) Open(ctx context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	if o.SampleRate <= 0 {
		return nil, errors.New("fake-tts: sample rate required")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &ttsStream{rate: o.SampleRate, failAfter: t.failAfter, out: make(chan []int16), cancel: cancel}
	s.cond = sync.NewCond(&s.mu)
	go s.pump(ctx)
	return s, nil
}

type ttsStream struct {
	rate      int
	failAfter int
	out       chan []int16
	cancel    context.CancelFunc

	mu       sync.Mutex
	cond     *sync.Cond
	pending  int
	flushed  bool
	closed   bool
	produced int
	err      error
}

func (s *ttsStream) Speak(string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushed || s.closed {
		return errors.New("fake-tts: stream finished")
	}
	s.pending++
	s.cond.Broadcast()
	return nil
}

func (s *ttsStream) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushed = true
	s.cond.Broadcast()
	return nil
}

func (s *ttsStream) Audio() <-chan []int16 { return s.out }

// Err is read after Audio closes, which orders it after the pump's write
func (s *ttsStream) Err() error { return s.err }

func (s *ttsStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
	return nil
}

func (s *ttsStream) pump(ctx context.Context) {
	defer close(s.out)
	for {
		s.mu.Lock()
		for s.pending == 0 && !s.flushed && !s.closed {
			s.cond.Wait()
		}
		if s.closed || (s.pending == 0 && s.flushed) {
			s.mu.Unlock()
			return
		}
		if s.failAfter > 0 && s.produced >= s.failAfter {
			s.err = errors.New(providers.CodeTTSFailed)
			s.mu.Unlock()
			return
		}
		s.pending--
		s.produced++
		s.mu.Unlock()
		select {
		case s.out <- s.tone():
		case <-ctx.Done():
			return
		}
	}
}

func (s *ttsStream) tone() []int16 {
	n := s.rate * toneMillis / 1000
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(toneAmp * math.MaxInt16 * math.Sin(2*math.Pi*toneHz*float64(i)/float64(s.rate)))
	}
	return pcm
}
