package core

import (
	"context"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/alarms"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/policy"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// while media plays the level in force goes back on the hardware this often (v1)
const recheckEvery = time.Minute

// commandWords decides and acts on the command words audiod hears (M6a-D6): тише lowers the level a step within 2 s
// of the last reply frame and ends the session whose microphone carried it, стоп, хватит and пауза stop a ring, and
// with media playing and no session streaming they pause it themselves, so they work offline. It is the input's
// CommandPolicy.
type commandWords struct {
	s *Sync
	// nil: the board keeps no level
	vol *volume.Setter
	// nil: the board plays no media
	mp    board.MediaPlayback
	words []string
}

// how long an offline media action waits for audiod's answer
const mediaBound = 2 * time.Second

// mediaFailed is the reason an audiod media action came back with an error
var mediaFailed = map[policy.ActionKind]string{
	policy.PauseMedia:    "pause_failed",
	policy.MediaNext:     "next_failed",
	policy.MediaPrevious: "previous_failed",
	policy.MediaResume:   "resume_failed",
}

func (c *commandWords) Command(id uint32, muted bool) (action, reason string) {
	var word string
	if uint64(id) < uint64(len(c.words)) {
		word = c.words[id]
	}
	// the lights say the word was taken in, whatever the policy then does with it
	if c.s.ind != nil {
		c.s.ind.Heard()
	}
	now := c.s.activity.Snapshot()
	a := policy.Command(word, policy.CommandFacts{
		Muted: muted, Ringing: c.s.ringing(), Replied: now.Replied, SinceReply: now.SinceReply,
		Injected: now.ReplyInjected || now.SessionInjected, Streaming: now.Session != "",
		Playing: c.mp != nil && c.mp.MediaPlaying(),
	})
	switch a.Kind {
	case policy.StopRing:
		c.s.ringer.Stop(alarms.StopWord)
	case policy.PauseMedia, policy.MediaNext, policy.MediaPrevious, policy.MediaResume:
		if c.mp == nil {
			return "", "unavailable"
		}
		ctx, cancel := context.WithTimeout(context.Background(), mediaBound)
		var err error
		switch a.Kind {
		case policy.PauseMedia:
			err = c.mp.MediaPause(ctx)
		case policy.MediaNext:
			err = c.mp.MediaNext(ctx)
		case policy.MediaPrevious:
			err = c.mp.MediaPrevious(ctx)
		case policy.MediaResume:
			err = c.mp.MediaResume(ctx)
		}
		cancel()
		// a player with nothing to continue answers an error, which only says so in the log
		if err != nil {
			return "", mediaFailed[a.Kind]
		}
	case policy.ReportWord:
		// the home's phrase acts server-side: hand the heard word to it, if a link is up
		c.s.sayWord(word)
	case policy.VolumeDown, policy.VolumeUp:
		if c.vol == nil {
			return "", "unavailable"
		}
		step := -input.VolumeStep
		if a.Kind == policy.VolumeUp {
			step = input.VolumeStep
		}
		// one the store cannot keep is refused, and the lights show the level in force either way, as for a press
		level, _ := c.vol.Step(step, volume.OriginWord)
		if c.s.ind != nil {
			c.s.ind.ShowVolume(level)
		}
		if a.EndSession {
			c.s.endForCommand(now.Session)
		}
	default:
		return "", a.Reason
	}
	return a.Kind.String(), ""
}

// startVoice has the board's input take core's command words, and puts the level in force back on the hardware each
// interval while media plays; call it before the input runs.
func (s *Sync) startVoice(ctx context.Context, wg *sync.WaitGroup, in *input.Input) {
	if vb, ok := s.board.(board.Volume); ok {
		s.vol = vb.Volume()
	}
	showOutsideVolume(s.vol, s.ind)
	mp, _ := s.board.(board.MediaPlayback)
	if in != nil {
		in.UseCommands(&commandWords{s: s, vol: s.vol, mp: mp, words: input.CommandWords()})
	}
	if s.vol == nil || mp == nil {
		return
	}
	wg.Go(func() { s.vol.RecheckWhilePlaying(ctx, s.recheckEvery, mp.MediaPlaying, s.ringing) })
}

// sayWord sends a home phrase the spotter heard to the server, which alone can act on
// it; with no link the word is lost with the session it never had
func (s *Sync) sayWord(word string) {
	s.mu.Lock()
	send := s.wordSend
	s.mu.Unlock()
	if send == nil {
		return
	}
	_ = send(&cadencev2.Envelope{Body: &cadencev2.Envelope_WordHeard{WordHeard: &cadencev2.WordHeard{Word: word}}})
}

// recheck puts the level in force back on the hardware before a reply, unless a ring ramps it
func (s *Sync) recheck() { s.vol.Recheck(s.ringing) }

// ringing is whether an alarm or a timer rings; it never waits
func (s *Sync) ringing() bool { return s.ringer != nil && s.ringer.Ringing() }

// endForCommand ends session id on the link that is up, if it still runs there: a command word reached its microphone
func (s *Sync) endForCommand(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.triggers != nil {
		audio.Enqueue(s.triggers, audio.Trigger{Mode: audio.CommandEnd, Session: id}, s.log)
	}
}

// showOutsideVolume has the lights show a level a sender (AirPlay, Soloist) or an api caller (dashboard, HA) set, as a
// press does; the press and the command word show their own, and a restore is no one's move
func showOutsideVolume(vol *volume.Setter, ind *indicator) {
	if vol == nil || ind == nil {
		return
	}
	vol.Listen(func(level int, origin volume.Origin) {
		if origin == volume.OriginSender || origin == volume.OriginCommand {
			ind.ShowVolume(level)
		}
	})
}
