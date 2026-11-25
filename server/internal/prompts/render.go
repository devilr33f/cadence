package prompts

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

const (
	// the rate the voice loop asks its TTS for, which every provider serves
	ttsRate = 24000
	// speech at 24 kbps mono: a few KB a prompt
	bitrate = 24000
	// the device plays at most 3 s; a prompt stays under it with its margins
	maxSeconds = 2.9
	// samples whose magnitude stays under this at either end are the TTS's silence, trimmed to margin
	silence = 300
	margin  = ttsRate / 20 // 50 ms
)

var (
	// ErrTooLong is a phrase the TTS spoke for longer than the device plays
	ErrTooLong = errors.New("prompts: longer than 2.9 s")
	// ErrSpeechTooLong is speech the TTS spoke for longer than RenderSpeech was allowed
	ErrSpeechTooLong = errors.New("prompts: speech too long")
	// ErrNoAudio is a TTS that answered nothing but silence
	ErrNoAudio = errors.New("prompts: the tts answered no audio")
)

// NewEncoder makes the Opus encoder of one prompt; the default is libopus, which a build without cgo lacks.
type NewEncoder func() (audio.Encoder, error)

// DefaultEncoder is libopus at 24 kHz mono, 24 kbps, for speech.
func DefaultEncoder() (audio.Encoder, error) {
	return audio.NewEncoder(audio.EncoderOptions{SampleRate: ttsRate, Channels: 1, Bitrate: bitrate, Complexity: 10, Application: audio.AppVoIP})
}

// Render speaks p through tts in voice ("" for the instance's default) and returns its Ogg Opus file.
func Render(ctx context.Context, tts providers.TTS, voice string, p Prompt, enc NewEncoder) ([]byte, error) {
	return render(ctx, tts, voice, p, maxSeconds, enc)
}

// RenderSpeech speaks text in lang as Render does, for speech longer than a prompt (a reminder's, rung from the
// server): at most maxSecs of it once its silence is trimmed, else ErrSpeechTooLong.
func RenderSpeech(ctx context.Context, tts providers.TTS, voice, lang, text string, maxSecs float64, enc NewEncoder) ([]byte, error) {
	out, err := render(ctx, tts, voice, Prompt{Lang: lang, Text: text}, maxSecs, enc)
	if errors.Is(err, ErrTooLong) {
		return nil, ErrSpeechTooLong
	}
	return out, err
}

func render(ctx context.Context, tts providers.TTS, voice string, p Prompt, maxSecs float64, enc NewEncoder) ([]byte, error) {
	pcm, err := speak(ctx, tts, voice, p)
	if err != nil {
		return nil, err
	}
	pcm = trim(pcm)
	if len(pcm) == 0 {
		return nil, ErrNoAudio
	}
	if float64(len(pcm))/ttsRate > maxSecs {
		return nil, ErrTooLong
	}
	e, err := enc()
	if err != nil {
		return nil, err
	}
	samples48k := int64(len(pcm)) * (48000 / ttsRate)
	// the encoder's delay of silence after the speech, so the decoder gives every sample back, then whole frames
	pcm = append(pcm, make([]int16, preSkip/(48000/ttsRate))...)
	frame := e.FrameSamples()
	if r := len(pcm) % frame; r != 0 {
		pcm = append(pcm, make([]int16, frame-r)...)
	}
	var packets [][]byte
	for i := 0; i < len(pcm); i += frame {
		pkt, err := e.Encode(pcm[i : i+frame])
		if err != nil {
			return nil, err
		}
		packets = append(packets, pkt)
	}
	return oggOpus(packets, ttsRate, samples48k), nil
}

func speak(ctx context.Context, tts providers.TTS, voice string, p Prompt) ([]int16, error) {
	s, err := tts.Open(ctx, providers.TTSOptions{Voice: voice, SampleRate: ttsRate, Language: p.Lang})
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	// as every spoken reply: no command word the device would hear from its own speaker
	if err := s.Speak(cmdguard.Rewrite(p.Text, p.Lang)); err != nil {
		return nil, err
	}
	if err := s.Flush(); err != nil {
		return nil, err
	}
	var pcm []int16
	limit := ttsRate * 30
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-s.Audio():
			if !ok {
				if err := s.Err(); err != nil {
					return nil, err
				}
				return pcm, nil
			}
			pcm = append(pcm, chunk...)
			if len(pcm) > limit {
				return nil, ErrTooLong
			}
		}
	}
}

// trim cuts the silence at both ends down to margin
func trim(pcm []int16) []int16 {
	loud := func(s int16) bool { return s > silence || s < -silence }
	first, last := -1, -1
	for i, s := range pcm {
		if loud(s) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	return pcm[max(0, first-margin):min(len(pcm), last+1+margin)]
}
