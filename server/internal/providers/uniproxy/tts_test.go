//go:build cgo

package uniproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// speech is an ogg opus stream of n 20 ms frames of a tone at 48 kHz mono, as uniproxy synthesizes
func speech(t *testing.T, n int) []byte {
	t.Helper()
	enc, err := audio.NewEncoder(audio.EncoderOptions{SampleRate: 48000, Channels: 1, Bitrate: 48000, Complexity: 5, Application: audio.AppAudio})
	if err != nil {
		t.Fatal(err)
	}
	m := newOggMuxer(7)
	out := m.headerPages()
	pcm := make([]int16, enc.FrameSamples())
	for f := range n {
		for i := range pcm {
			pcm[i] = int16(6000 * math.Sin(2*math.Pi*330*float64(f*len(pcm)+i)/48000))
		}
		pkt, err := enc.Encode(pcm)
		if err != nil {
			t.Fatal(err)
		}
		page, err := m.page(pkt)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page...)
	}
	return out
}

// answer serves one TtsGenerate: TtsSpeak on stream, the speech in two media frames, then the stream's close
func answer(t *testing.T, c *fakeConn, stream uint32, frames int) (text, lang, voice string, ok bool) {
	t.Helper()
	h, n, body, ok := c.nextEvent()
	if !ok || n != 13 {
		return "", "", "", false
	}
	g, _ := decode(body)
	c.event(eventHeader{refMessageID: h.messageID, streamID: uint64(stream), hasStreamID: true}, 14,
		str(nil, 1, opusFormat))
	s := speech(t, frames)
	c.media(stream, s[:len(s)/2])
	c.media(stream, s[len(s)/2:])
	c.streamClosed(stream, closeOK)
	return g.string(7), g.string(1), g.string(8), true
}

func drain(t *testing.T, st providers.TTSStream) int {
	t.Helper()
	samples := 0
	timeout := time.After(10 * time.Second)
	for {
		select {
		case pcm, ok := <-st.Audio():
			if !ok {
				return samples
			}
			samples += len(pcm)
		case <-timeout:
			t.Fatal("audio never closed")
		}
	}
}

// each sentence is one request after the previous stream closed; the audio arrives at the asked rate and closes
// cleanly after the flush
func TestTTSSpeaks(t *testing.T) {
	got := make(chan [3]string, 2)
	f := newFake(t, func(c *fakeConn) {
		for _, stream := range []uint32{2, 4} {
			text, lang, voice, ok := answer(t, c, stream, 10)
			if !ok {
				return
			}
			got <- [3]string{text, lang, voice}
		}
		c.next()
	})
	st, err := (&tts{cfg: f.config(t, `,"voice":"shitova"`)}).Open(context.Background(),
		providers.TTSOptions{SampleRate: 24000, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if st.Speak("Привет.") != nil || st.Speak("—") != nil || st.Speak("Как дела?") != nil || st.Flush() != nil {
		t.Fatal("speak refused")
	}
	// 20 frames of 20 ms at 24 kHz
	if n := drain(t, st); n != 20*480 {
		t.Fatalf("decoded %d samples, want %d", n, 20*480)
	}
	if st.Err() != nil {
		t.Fatalf("err %v", st.Err())
	}
	first, second := <-got, <-got
	if first != [3]string{"Привет.", "ru-RU", "shitova"} || second[0] != "Как дела?" {
		t.Fatalf("requests %q %q", first, second)
	}
}

// a lazy stream, which waits for chunk requests cadence never sends, fails with the fixed code and its detail
func TestTTSRefusesALazyStream(t *testing.T) {
	f := newFake(t, func(c *fakeConn) {
		h, _, _, ok := c.nextEvent()
		if !ok {
			return
		}
		c.event(eventHeader{refMessageID: h.messageID, streamID: 2, hasStreamID: true}, 14,
			boolean(str(nil, 1, opusFormat), 2, true))
		c.next()
	})
	st, err := (&tts{cfg: f.config(t, "")}).Open(context.Background(), providers.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_ = st.Speak("Привет.")
	_ = st.Flush()
	drain(t, st)
	err = st.Err()
	var de *providers.DetailError
	if !errors.As(err, &de) || err.Error() != providers.CodeTTSFailed || providers.Detail(err) != "unsupported_stream" {
		t.Fatalf("err %v detail %q", err, providers.Detail(err))
	}
}

// nothing speakable: no request goes out, and the flush ends the stream cleanly
func TestTTSWithNothingToSay(t *testing.T) {
	asked := make(chan bool, 1)
	f := newFake(t, func(c *fakeConn) {
		_, n, _, ok := c.nextEvent()
		asked <- ok && n == 13
	})
	st, err := (&tts{cfg: f.config(t, "")}).Open(context.Background(), providers.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Speak(" … ")
	_ = st.Flush()
	if n := drain(t, st); n != 0 || st.Err() != nil {
		t.Fatalf("samples %d err %v", n, st.Err())
	}
	if <-asked {
		t.Fatal("a request went out for nothing speakable")
	}
}

func (c *fakeConn) media(stream uint32, b []byte) {
	frame := make([]byte, mediaPrefixBytes, mediaPrefixBytes+len(b))
	binary.BigEndian.PutUint32(frame, stream)
	c.send(append(frame, b...))
}
