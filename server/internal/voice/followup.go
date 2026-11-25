package voice

import (
	"math"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// the close of a follow-up that heard only the previous reply's echo
const codeFollowUpEcho = "follow_up_echo"

// followUpFacts are what the server knows when a session closes (M6a-D5,
// Ruling M6a-R1); the device then checks its own facts before it opens one.
type followUpFacts struct {
	settings settings.FollowUp
	trigger  cadencev2.Trigger
	code     string // the close code, "" for ok
	// the reply stream sent at least one audio packet: after a silent reply
	// whoever talks next would not be answering
	spoke bool
	stop  bool // the final transcript asked to stop
	echo  bool // the final transcript was the previous reply's own words
	// command: a direct command answered the turn, whose action is the whole
	// answer — the session ends, the microphone with it
	command bool
	// follow-ups in a row before this close; one nobody offered counts as the limit
	chain int
}

// followUp says whether the close offers a follow-up, and its window.
func followUp(f followUpFacts) (bool, uint32) {
	switch f.trigger {
	case cadencev2.Trigger_TRIGGER_WAKE, cadencev2.Trigger_TRIGGER_BUTTON, cadencev2.Trigger_TRIGGER_FOLLOW_UP:
	default:
		return false, 0
	}
	if !f.settings.Enabled || f.code != "" || !f.spoke || f.stop || f.echo || f.command || f.chain >= f.settings.Limit ||
		f.settings.WindowMs <= 0 || f.settings.WindowMs > math.MaxUint32 {
		return false, 0
	}
	return true, uint32(f.settings.WindowMs)
}

// takeTurns takes what the device's last turn left for this session, under
// the coordinator's lock before the session runs: a wake or press starts the
// chain over, a follow-up counts on from the offer it answers. The offer goes
// with any start, whatever its trigger.
func (s *session) takeTurns(offers map[string]int, echoes map[string]string) {
	prev, offered := offers[s.dev]
	s.echoOf = echoes[s.dev]
	delete(offers, s.dev)
	delete(echoes, s.dev)
	if s.trigger == cadencev2.Trigger_TRIGGER_FOLLOW_UP {
		s.chain, s.unsolicited = prev+1, !offered
	}
}

// leave decides the follow-up the close offers and leaves the device's next
// session the offer and this reply's guarded text. It runs under s.mu before
// the close goes out, so a follow-up start that answers the close finds them;
// a session that is no longer the device's own offers and leaves nothing.
func (s *session) leave(code string) (bool, uint32) {
	chain := s.chain
	if s.unsolicited {
		chain = s.followUp.Limit
	}
	ok, window := followUp(followUpFacts{
		settings: s.followUp, trigger: s.trigger, code: code, spoke: s.seq > 0,
		stop: s.stopped, echo: s.echoed, command: s.commanded, chain: chain,
	})
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[s.dev] != s {
		return false, 0
	}
	if ok {
		c.offers[s.dev] = chain
	} else {
		delete(c.offers, s.dev)
	}
	if s.spoken != "" {
		c.echoes[s.dev] = s.spoken
	} else {
		delete(c.echoes, s.dev)
	}
	return ok, window
}
