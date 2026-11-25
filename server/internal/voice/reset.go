package voice

import (
	"context"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/conversation"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const codeConversationReset = "conversation_reset"

// only the whole utterance: a request to reset a router's settings, or to
// forget a long-term memory, is not a conversation reset
var resetRequests = map[string]map[string]bool{
	"ru": {"сброс": true, "сбрось разговор": true, "начнем заново": true},
	"en": {"reset": true, "start over": true},
}

func isResetRequest(text, lang string) bool {
	set, ok := resetRequests[lang]
	if !ok {
		set = resetRequests["en"]
	}
	return set[strings.Join(words(text), " ")]
}

// resetRequested closes the current segment without an agent/model call, then
// speaks the fixed confirmation through the reply's usual TTS and pacer path.
func (s *session) resetRequested(p plan, text string) string {
	reply := conversation.Confirmation(p.language)
	if s.c.d.ResetConversation == nil {
		return codeInternal
	}
	if err := s.c.d.ResetConversation(s.ctx, s.dev, s.id, text, reply); err != nil {
		s.c.d.Log.Error("conversation_reset_failed", "device_id", s.dev, "session_id", s.id)
		return s.orCtx(codeInternal)
	}
	s.asked = true
	s.speechStarted()
	ctx, cancel := context.WithCancel(s.ctx)
	outs := make(chan agent.Out, 2)
	outs <- agent.Out{Speech: reply}
	outs <- agent.Out{End: true}
	close(outs)
	k := newSpeaker(ctx, s, p, func() {})
	spoken := make(chan struct{})
	s.goSafe("speak_reset", func() { k.run(outs) }, func() { close(spoken) }, k.done)
	s.channels = p.channels
	tick, stopTick := s.c.tick()
	pace(ctx, k.packets, k.live, s.audio, tick)
	stopTick()
	cancel()
	<-spoken
	s.spoken = k.spoken.String()
	switch {
	case s.ctx.Err() != nil:
		return s.ctxCode()
	case k.failed.Load():
		return providers.CodeTTSFailed
	}
	// a nonempty close code also keeps the confirmation from opening a follow-up
	return codeConversationReset
}
