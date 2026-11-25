package screen

import (
	"context"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// how long the screen's stop of a mirror waits for audiod, past media's own wait for its answer
const mirrorStopWait = 10 * time.Second

// stopMirror is the screen's stop key on a mirror (its MirrorStop with a request_id): audiod stops its session, which
// lets the sender go, and the MirrorStop with reason stopped follows from audiod as for any end. a session id that is
// not the active mirror's is stale and answered no_session, so it never stops what came after. ids and codes alone
// are logged, never the sender (D11)
func (s *Server) stopMirror(m *screenv1.MirrorStop) {
	id := m.GetSessionId()
	s.mu.Lock()
	active := s.mirror != nil && id != "" && s.mirror.GetSessionId() == id
	s.mu.Unlock()
	if !active {
		s.log.Info("mirror_stop_stale", "session_id", id)
		s.answerScreen(m.GetRequestId(), "no_session")
		return
	}
	if s.o.Media == nil {
		s.answerScreen(m.GetRequestId(), codeUnavailable)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mirrorStopWait)
		defer cancel()
		code := ""
		if err := s.o.Media.StopScreencast(ctx); err != nil {
			code = "failed"
			if ce := (*capability.CommandError)(nil); errors.As(err, &ce) {
				code = ce.Code
			}
		}
		s.log.Info("mirror_stop_requested", "session_id", id, "code", code)
		s.answerScreen(m.GetRequestId(), code)
	}()
}

// answerScreen answers a request the screen made; code empty when ok
func (s *Server) answerScreen(requestID, code string) {
	if requestID == "" {
		return
	}
	s.send(&screenv1.Envelope{Body: &screenv1.Envelope_CommandResult{CommandResult: &screenv1.CommandResult{
		RequestId: requestID, Code: code,
	}}})
}
