package voice

import (
	"context"
	"crypto/rand"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// reply hands the transcript to the agent and speaks what it says as it
// comes. The session's own end goes first, then a tts failure, then the
// agent's code; a turn that said nothing opens no stream and needs no tts.
func (s *session) reply(p plan, text string) string {
	// a long utterance must not eat into the agent's time
	s.extend(time.Now())
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// a failed tts ends the agent's turn: nothing more it says could be heard
	actx, stopAgent := context.WithCancel(ctx)
	defer stopAgent()
	outs := make(chan agent.Out)
	// the helpers close their channels on a panic too, so the waits below never block
	results := make(chan agent.Result, 1)
	s.goSafe("agent", func() {
		results <- s.c.d.Agent.Run(actx, agent.Turn{
			ID: s.id, DeviceID: s.dev, SessionID: s.id, Trigger: s.trigger, Text: text, Language: p.language,
			Settings: &p.values, Mode: p.mode, AfterRing: s.afterRing,
		}, outs)
	}, func() { close(results) }, func() { close(outs) })
	// tts opens beside the agent's first step, so its handshake overlaps the model's
	k := newSpeaker(ctx, s, p, stopAgent)
	spoken := make(chan struct{})
	s.goSafe("speak", func() { k.run(outs) }, func() { close(spoken) }, k.done)
	s.channels = p.channels
	tick, stopTick := s.c.tick()
	pace(ctx, k.packets, k.live, s.audio, tick)
	stopTick()
	cancel()
	<-spoken
	s.spoken = k.spoken.String()
	res := <-results
	s.failure = res.Failure
	switch {
	case s.ctx.Err() != nil:
		return s.ctxCode()
	case k.failed.Load():
		return providers.CodeTTSFailed
	}
	return res.Code
}

// audio sends one packet, opening the reply stream before the first.
func (s *session) audio(opus []byte) error {
	if s.streamID == "" {
		id := rand.Text()
		channels := uint32(1)
		if s.channels == 2 {
			channels = 2
		}
		if !s.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_StreamStart{StreamStart: &cadencev2.StreamStart{
			StreamId: id, SessionId: s.id, Purpose: cadencev2.StreamPurpose_STREAM_PURPOSE_REPLY,
			Codec: cadencev2.Codec_CODEC_OPUS, Channels: channels, Trace: gateway.TraceOf(s.ctx),
		}}}) {
			return errNotSent
		}
		s.streamID = id
	}
	if !s.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_AudioDown{AudioDown: &cadencev2.AudioDown{
		StreamId: s.streamID, Seq: s.seq, Opus: opus,
	}}}) {
		return errNotSent
	}
	if s.seq == 0 {
		s.firstAudio = s.c.d.Now()
		s.span.AddEvent("first_audio_sent")
		s.down = newMeter(s.ctx)
	}
	s.down.add(0, len(opus))
	s.seq++
	return nil
}

// encode cuts the mono tts audio into frames, pads the last with silence and
// queues their packets as gen's. Once the audio ended it returns the tts error.
// tt, when set, counts the segment's bytes on its span.
func encode(ctx context.Context, tts providers.TTSStream, enc audio.Encoder, channels int, gen uint64, packets chan<- packet, tt *ttsTrace) error {
	per := enc.FrameSamples() / channels
	frame := make([]int16, 0, per)
	push := func() error {
		pkt, err := enc.Encode(interleave(frame, channels))
		frame = frame[:0]
		if err != nil {
			return err
		}
		tt.packet(len(pkt))
		select {
		case packets <- packet{gen: gen, opus: pkt}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for {
		var pcm []int16
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-tts.Audio():
			if !ok {
				if len(frame) > 0 {
					frame = append(frame, make([]int16, per-len(frame))...)
					if err := push(); err != nil {
						return err
					}
				}
				return tts.Err()
			}
			pcm = p
			tt.pcm(len(p))
		}
		for len(pcm) > 0 {
			n := min(per-len(frame), len(pcm))
			frame = append(frame, pcm[:n]...)
			pcm = pcm[n:]
			if len(frame) == per {
				if err := push(); err != nil {
					return err
				}
			}
		}
	}
}

// interleave copies a mono frame into every channel.
func interleave(mono []int16, channels int) []int16 {
	if channels == 1 {
		return mono
	}
	out := make([]int16, len(mono)*channels)
	for i, v := range mono {
		for c := range channels {
			out[i*channels+c] = v
		}
	}
	return out
}
