// Package fake holds dev-only providers. They produce data only and never
// touch the network.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func init() { providers.SetDevFakes(Modules) }

// Modules returns the fakes; NewRegistry adds them only with devFakes.
func Modules() []providers.Module {
	return []providers.Module{sttModule, llmModule, ttsModule, embedModule}
}

var sttModule = providers.Module{
	ID:    "fake-stt",
	Kind:  providers.KindSTT,
	Probe: probe,
	ConfigSchema: `{"type":"object","additionalProperties":false,"properties":{` +
		`"finalAfterPackets":{"type":"integer","minimum":1},"partialEvery":{"type":"integer","minimum":0},` +
		`"text":{"type":"string"}}}`,
	New: func(cfg json.RawMessage, _ map[string]string) (any, error) {
		c := sttConfig{FinalAfterPackets: 25, Text: "what time is it"}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		return &stt{cfg: c}, nil
	},
}

type sttConfig struct {
	FinalAfterPackets int `json:"finalAfterPackets"`
	// PartialEvery, when set, sends the text's words so far as a partial
	// every so many packets before the final, one more word each time
	PartialEvery int    `json:"partialEvery"`
	Text         string `json:"text"`
}

type stt struct{ cfg sttConfig }

func (s *stt) Start(context.Context, providers.STTOptions) (providers.STTStream, error) {
	words := strings.Fields(s.cfg.Text)
	return &sttStream{cfg: s.cfg, words: words, events: make(chan providers.STTEvent, len(words)+2)}, nil
}

type sttStream struct {
	cfg    sttConfig
	words  []string
	events chan providers.STTEvent

	mu       sync.Mutex
	packets  int
	partials int
	closed   bool
}

func (s *sttStream) Write([]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("fake-stt: closed")
	}
	s.packets++
	// the buffer holds every event, so these sends never block
	if s.packets == 1 {
		s.events <- providers.STTEvent{Kind: providers.STTSpeechStarted}
	}
	if n := s.cfg.PartialEvery; n > 0 && s.packets < s.cfg.FinalAfterPackets && s.packets%n == 0 &&
		s.partials < len(s.words) {
		s.partials++
		s.events <- providers.STTEvent{Kind: providers.STTPartial, Text: strings.Join(s.words[:s.partials], " ")}
	}
	if s.packets == s.cfg.FinalAfterPackets {
		s.events <- providers.STTEvent{Kind: providers.STTFinal, Text: s.cfg.Text}
	}
	return nil
}

func (s *sttStream) Events() <-chan providers.STTEvent { return s.events }

func (s *sttStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

// probe passes: the fakes touch no network
func probe(context.Context, any) error { return nil }
