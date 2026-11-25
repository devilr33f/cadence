package sim

import (
	"context"
	"os"
	"path/filepath"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// startAudiod runs audiod with file capture and output under the sim dir,
// starting at the device's level and following every level set after it.
func (s *sim) startAudiod(ctx context.Context) audio.IO {
	out := filepath.Join(s.audio.dir, "out")
	if err := os.MkdirAll(out, 0o750); err != nil {
		s.log.Warn("sim_dir_failed")
	}
	micPath := filepath.Join(s.audio.dir, "mic.wav")
	spec := audiod.Spec{
		Binary: s.audiodBinary,
		Env:    s.audiodEnv(),
		Socket: filepath.Join(s.dataDir, "run", "audiod.sock"),
		Configure: &audiodv1.Configure{
			Capture: &audiodv1.CaptureConfig{Backend: audiodv1.Backend_BACKEND_FILE, FilePath: micPath, SampleRate: 16000, Channels: 1},
			// stereo music, no speaker eq profile
			Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_FILE, Dir: out, Channels: 2},
			Mic:    &audiodv1.MicConfig{SampleRate: 16000, Bitrate: 24000, FrameMs: 20},
			CaFile: caBundle,
		},
	}
	var sup *audiod.Supervisor
	opts := []audiod.SupervisorOption{
		audiod.WithInput(s.input), audiod.WithMedia(s.media), audiod.WithSoloist(s.soloist), audiod.WithAirplay(s.airplay),
		audiod.WithGroup(s.group), audiod.WithCounts(s.metricsEnv.App),
	}
	if s.ringer != nil {
		opts = append(opts, audiod.WithToneState(s.ringer.ToneState))
	}
	if s.bt != nil {
		opts = append(opts, audiod.WithBluetooth(s.bt))
	}
	if s.logs != nil {
		opts = append(opts, audiod.WithLogs(s.logs, s.logsEnv.Local))
	}
	s.vol.Follow(func(level int) func(int) {
		sup = audiod.NewSupervisor(spec, level, s.log, opts...)
		return sup.SetVolume
	})
	if s.ringer != nil {
		s.ringer.UseSpeaker(sup)
	}
	s.wg.Go(func() { sup.Run(ctx) })
	s.wg.Go(func() { s.soloist.Run(ctx) })
	if s.bt != nil {
		s.btSim.ctx = ctx
		s.wg.Go(func() { s.bt.Run(ctx) })
		s.wg.Go(func() { s.btSim.poll(ctx) })
	}
	aio := sup.IO(s.audio.triggers)
	return audio.IO{
		Triggers: aio.Triggers,
		Mic:      &audiodMic{inner: aio.Mic, path: micPath, say: s.audio},
		Speaker:  &audiodSpeaker{inner: aio.Speaker, out: out},
	}
}

// the sync plane's fake clock in the sim's audiod (containers share one kernel clock): passed on from cadenced's own
// environment, beside the PATH audiod always gets
var simClockEnv = []string{"CADENCE_SIM_CLOCK_OFFSET_MS", "CADENCE_SIM_CLOCK_PPM"}

func (s *sim) audiodEnv() []string {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	for _, k := range simClockEnv {
		if v := s.getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// audiodMic gives audiod the trigger's fixture, or a follow-up's from the say
// file: it plays the one capture file on every CaptureStart.
type audiodMic struct {
	inner audio.Mic
	path  string
	say   *simAudio
}

func (m *audiodMic) Open(ctx context.Context, t audio.Trigger) (<-chan []byte, error) {
	if waitsForSay(t) {
		// no capture before the say file: audiod would play the last utterance again
		return m.say.followUp(ctx, m.capture, m.captureFailed), nil
	}
	return m.capture(ctx, t.Fixture)
}

func (m *audiodMic) capture(ctx context.Context, fixture string) (<-chan []byte, error) {
	if !fixtureName.MatchString(fixture) {
		return nil, errNoFixture
	}
	wav, err := fixtures.ReadFile("fixtures/" + fixture + ".wav")
	if err != nil {
		return nil, errNoFixture
	}
	if err := atomicfile.Write(m.path, wav); err != nil {
		return nil, err
	}
	return m.inner.Open(ctx, audio.Trigger{Fixture: fixture})
}

func (m *audiodMic) MicFailure(packets <-chan []byte) bool {
	return m.say.MicFailure(packets) || m.captureFailed(packets)
}

func (m *audiodMic) captureFailed(packets <-chan []byte) bool {
	r, ok := m.inner.(audio.MicFailureReporter)
	return ok && r.MicFailure(packets)
}

// audiodSpeaker keeps the reply summary; audiod writes <out>/<stream_id>.wav
// itself.
type audiodSpeaker struct {
	inner audio.Speaker
	out   string
}

func (sp *audiodSpeaker) Begin(st *cadencev2.StreamStart) (audio.StreamWriter, error) {
	id := st.GetStreamId()
	if !streamIDChars.MatchString(id) {
		return nil, errBadStream
	}
	w, err := sp.inner.Begin(st)
	if err != nil {
		return nil, err
	}
	return &countingWriter{inner: w, summary: filepath.Join(sp.out, id+".json"), purpose: purposeName(st.GetPurpose())}, nil
}

func (sp *audiodSpeaker) Duck(active bool) {
	if d, ok := sp.inner.(audio.Ducker); ok {
		d.Duck(active)
	}
}

type countingWriter struct {
	inner   audio.StreamWriter
	summary string
	purpose string
	packets int
}

func (w *countingWriter) Write(seq uint32, p []byte) error {
	if err := w.inner.Write(seq, p); err != nil {
		return err
	}
	w.packets++
	return nil
}

func (w *countingWriter) End() error {
	if err := w.inner.End(); err != nil {
		return err
	}
	return writeSummary(w.summary, w.packets, w.purpose)
}

// Abort writes no summary.
func (w *countingWriter) Abort() { w.inner.Abort() }
