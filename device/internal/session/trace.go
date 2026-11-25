package session

import (
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// beginReply plays a reply stream under its own span, voice.playback: a child of the server's span the StreamStart
// names (its tts), else of the turn. audiod gets the playback's context with the stream, so its marks land there, and
// the span ends when audiod says the stream is done, or at its cut; the turn stays open until then.
func (c *Controller) beginReply(s *active, st *cadencev2.StreamStart) (audio.StreamWriter, error) {
	s.turn.Replied()
	pctx, span, release := s.turn.Playback(st)
	if tc := tracing.Proto(pctx); tc != nil {
		st.Trace = tc
	}
	w, err := c.io.Speaker.Begin(st)
	if err != nil {
		tracing.End(span, "speaker")
		release()
		return nil, err
	}
	tracing.Bind(st.GetStreamId(), span, release)
	return &tracedWriter{StreamWriter: w, id: st.GetStreamId()}, nil
}

// tracedWriter ends the playback's span at a cut; a stream that ends normally is released by audiod's StreamDone
type tracedWriter struct {
	audio.StreamWriter
	id string
}

func (w *tracedWriter) Abort() {
	w.StreamWriter.Abort()
	tracing.Release(w.id, "aborted")
}
