package fake_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/fake"
)

func build(t *testing.T, id, cfg string) any {
	t.Helper()
	for _, m := range fake.Modules() {
		if m.ID == id {
			// as an instance's config is checked before New
			sch, err := jsonschemax.Compile(m.ConfigSchema)
			if err != nil {
				t.Fatal(err)
			}
			if path, ok := jsonschemax.Validate(sch, []byte(cfg)); !ok {
				t.Fatalf("config refused at %q", path)
			}
			v, err := m.New(json.RawMessage(cfg), nil)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no module %s", id)
	return nil
}

func TestFakeSTTFinalAfterN(t *testing.T) {
	stt := build(t, "fake-stt", `{"finalAfterPackets":3,"text":"hello"}`).(providers.STT)
	s, err := stt.Start(context.Background(), providers.STTOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = s.Write([]byte{1})
	if e := <-s.Events(); e.Kind != providers.STTSpeechStarted {
		t.Fatalf("first event %+v", e)
	}
	_ = s.Write([]byte{1})
	select {
	case e := <-s.Events():
		t.Fatalf("early event %+v", e)
	default:
	}
	_ = s.Write([]byte{1})
	select {
	case e := <-s.Events():
		if e.Kind != providers.STTFinal || e.Text != "hello" {
			t.Fatalf("final %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no final")
	}
}

func TestFakeSTTDefaults(t *testing.T) {
	stt := build(t, "fake-stt", `{}`).(providers.STT)
	s, _ := stt.Start(context.Background(), providers.STTOptions{})
	defer func() { _ = s.Close() }()
	for range 25 {
		_ = s.Write([]byte{1})
	}
	<-s.Events()
	if e := <-s.Events(); e.Kind != providers.STTFinal || e.Text != "what time is it" {
		t.Fatalf("final %+v", e)
	}
}

func TestFakeLLMChunks(t *testing.T) {
	llm := build(t, "fake-llm", `{}`).(providers.LLM)
	ch, err := llm.Stream(context.Background(), providers.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for c := range ch {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		parts = append(parts, c.Text)
	}
	if len(parts) != 3 || strings.Join(parts, "") != "It is test time. All good." {
		t.Fatalf("parts %q", parts)
	}
}

func TestFakeTTSDuration(t *testing.T) {
	tts := build(t, "fake-tts", `{}`).(providers.TTS)
	s, err := tts.Open(context.Background(), providers.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Speak("hi"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	n := 0
	var nonzero bool
	for a := range s.Audio() {
		n += len(a)
		for _, v := range a {
			nonzero = nonzero || v != 0
		}
	}
	if n != 7200 || !nonzero {
		t.Fatalf("samples %d nonzero %v", n, nonzero)
	}
	if err := s.Speak("late"); err == nil {
		t.Fatal("speak after flush accepted")
	}
}

func TestFakeTTSFailAfterSpeaks(t *testing.T) {
	tts := build(t, "fake-tts", `{"failAfterSpeaks":1}`).(providers.TTS)
	s, err := tts.Open(context.Background(), providers.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for range 2 {
		if err := s.Speak("hi"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for a := range s.Audio() {
		n += len(a)
	}
	if n != 7200 || s.Err() == nil || s.Err().Error() != providers.CodeTTSFailed {
		t.Fatalf("samples %d err %v", n, s.Err())
	}
}

// the fakes touch no network, so their test passes
func TestFakesProbeOK(t *testing.T) {
	for _, m := range fake.Modules() {
		if m.Probe == nil {
			t.Fatalf("%s has no probe", m.ID)
		}
		if err := m.Probe(context.Background(), build(t, m.ID, `{}`)); err != nil {
			t.Fatalf("%s: %v", m.ID, err)
		}
	}
}
