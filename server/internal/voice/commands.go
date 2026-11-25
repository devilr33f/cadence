package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// A direct command is a whole utterance the server acts on without the model:
// the volume words, the player's skips and resumes, and a home device turned on
// or off by name. The table is matched whole, after the stop and reset requests;
// anything it does not match, or cannot settle alone (a name several devices
// share, a raise outside a woken session, a command the device refused), is
// answered by the agent as before.

// how far one volume word moves the level: two presses of the button
const volumeWordStep = 10

type directKind uint8

const (
	volumeDown directKind = iota
	volumeUp
	mediaResume
	mediaNext
	mediaPrevious
	homeOn
	homeOff
)

func (k directKind) String() string {
	switch k {
	case volumeDown:
		return "volume_down"
	case volumeUp:
		return "volume_up"
	case mediaResume:
		return "media_resume"
	case mediaNext:
		return "media_next"
	case mediaPrevious:
		return "media_previous"
	case homeOn:
		return "home_on"
	case homeOff:
		return "home_off"
	}
	return ""
}

type directCommand struct {
	kind directKind
	// target is the home device as the user named it
	target string
}

// directPhrases are the whole utterances of each command, as recognition writes
// them back: lower case, ё as е, no punctuation. A language without a table of
// its own uses en's.
var directPhrases = map[string]map[string]directKind{
	"ru": {
		"тише": volumeDown, "потише": volumeDown, "громкость ниже": volumeDown, "громкость меньше": volumeDown,
		"сделай тише": volumeDown, "сделай потише": volumeDown, "убавь громкость": volumeDown, "убавь звук": volumeDown,
		"громче": volumeUp, "погромче": volumeUp, "громкость выше": volumeUp, "громкость больше": volumeUp,
		"сделай громче": volumeUp, "сделай погромче": volumeUp, "прибавь громкость": volumeUp, "прибавь звук": volumeUp,
		"продолжи": mediaResume, "продолжи музыку": mediaResume, "продолжай": mediaResume, "продолжай музыку": mediaResume,
		"дальше": mediaNext, "следующая": mediaNext, "следующая песня": mediaNext, "следующий трек": mediaNext,
		"назад": mediaPrevious, "предыдущая": mediaPrevious, "предыдущая песня": mediaPrevious, "предыдущий трек": mediaPrevious,
	},
	"en": {
		"quieter": volumeDown, "volume down": volumeDown, "turn it down": volumeDown, "lower the volume": volumeDown,
		"louder": volumeUp, "volume up": volumeUp, "turn it up": volumeUp, "raise the volume": volumeUp,
		"resume": mediaResume, "continue": mediaResume, "keep playing": mediaResume,
		"next": mediaNext, "next song": mediaNext, "next track": mediaNext, "skip": mediaNext,
		"previous": mediaPrevious, "previous song": mediaPrevious, "previous track": mediaPrevious, "last song": mediaPrevious,
	},
}

// homePrefixes begin a home command; what follows is the device's name as the
// user said it, which Home Assistant resolves.
var homePrefixes = map[string][]homePrefix{
	"ru": {{prefix: "включи ", kind: homeOn}, {prefix: "выключи ", kind: homeOff}},
	"en": {
		{prefix: "turn on ", kind: homeOn}, {prefix: "turn off ", kind: homeOff},
		{prefix: "switch on ", kind: homeOn}, {prefix: "switch off ", kind: homeOff},
	},
}

type homePrefix struct {
	prefix string
	kind   directKind
}

// politeness a user may end any of these with, which the table forgives
var politeness = map[string]string{"ru": "пожалуйста", "en": "please"}

// matchDirect maps a whole utterance to a direct command, nil when it is none.
func matchDirect(text, lang string) *directCommand {
	base := langBase(lang)
	said := strings.Join(words(text), " ")
	if said == "" {
		return nil
	}
	if said == politeness[base] || said == politeness["en"] {
		return nil
	}
	said = strings.TrimSuffix(said, " "+politeness[base])
	said = strings.TrimSuffix(said, " "+politeness["en"])
	table, ok := directPhrases[base]
	if !ok {
		table, base = directPhrases["en"], "en"
	}
	if kind, ok := table[said]; ok {
		return &directCommand{kind: kind}
	}
	for _, p := range homePrefixes[base] {
		if !strings.HasPrefix(said, p.prefix) {
			continue
		}
		target := strings.TrimSpace(said[len(p.prefix):])
		if target != "" && len(target) <= 128 {
			return &directCommand{kind: p.kind, target: target}
		}
	}
	return nil
}

func langBase(lang string) string {
	base, _, _ := strings.Cut(strings.ToLower(lang), "-")
	return base
}

// HomeCall is a direct command's home action: target turned on or off, asked by
// the speaker whose session heard it.
type HomeCall struct {
	DeviceID, SessionID, TurnID, Target string
	Trigger                             cadencev2.Trigger
	On                                  bool
}

// directCommand runs c without the model. handled false leaves the utterance to
// the agent: the table decides no raise outside a woken session (rule 7), and
// every home name it cannot settle alone, which the model can ask about. A
// command that ran ends the turn silently — the action is its own answer — or
// with one fixed line; its transcript and reply are stored like any turn's.
func (s *session) directCommand(p plan, c *directCommand, text string) (string, bool) {
	d := s.c.d
	line := ""
	switch c.kind {
	case volumeUp:
		// the device checks the session again itself; rule 7 is decided here first
		if !agent.Woken(s.trigger) || d.Command == nil || !s.changeVolume(volumeWordStep) {
			return "", false
		}
	case volumeDown:
		if d.Command == nil || !s.changeVolume(-volumeWordStep) {
			return "", false
		}
	case mediaResume, mediaNext, mediaPrevious:
		if d.Command == nil {
			return "", false
		}
		name := map[directKind]string{mediaResume: "resume", mediaNext: "next", mediaPrevious: "previous"}[c.kind]
		var dev *commands.DeviceError
		if _, err := d.Command(s.ctx, s.dev, "media.player", name, []byte("{}")); errors.As(err, &dev) && dev.Code == "no_session" {
			// a resume with nothing playing may be a queue the model restarts (media_control does)
			if c.kind == mediaResume {
				return "", false
			}
			line = nothingPlaying(p.language)
		} else if err != nil {
			return "", false
		}
	case homeOn, homeOff:
		if d.HomeControl == nil {
			return "", false
		}
		name, done := d.HomeControl(s.ctx, HomeCall{
			DeviceID: s.dev, SessionID: s.id, TurnID: s.id, Target: c.target, Trigger: s.trigger, On: c.kind == homeOn,
		})
		if !done {
			return "", false
		}
		line = homeLine(p.language, c.kind == homeOn, name)
	}
	// the command is the whole answer: no follow-up, the mic stops with the turn
	s.asked, s.commanded = true, true
	if d.StoreCommand != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
		err := d.StoreCommand(ctx, s.dev, s.id, text, line, sessionTrigger(s.trigger))
		cancel()
		if err != nil {
			d.Log.Error("direct_command_store_failed", "device_id", s.dev, "session_id", s.id)
		}
	}
	d.Log.Info("direct_command", "device_id", s.dev, "session_id", s.id, "kind", c.kind.String(), "spoken", line != "")
	if line == "" {
		return "", true
	}
	return s.speakLine(p, line), true
}

// changeVolume moves the device's level by delta through its own audio.output,
// which resolves it against its level under its lock and shows it as a press.
func (s *session) changeVolume(delta int) bool {
	args, _ := json.Marshal(map[string]any{"delta": delta, "sessionId": s.id})
	_, err := s.c.d.Command(s.ctx, s.dev, "audio.output", "change_level", args)
	return err == nil
}

// speakLine says one fixed line through the reply's usual tts and pacer path
// and answers the close code for it ("" when it was spoken whole).
func (s *session) speakLine(p plan, line string) string {
	s.speechStarted()
	s.event(cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING)
	ctx, cancel := context.WithCancel(s.ctx)
	outs := make(chan agent.Out, 2)
	outs <- agent.Out{Speech: line}
	outs <- agent.Out{End: true}
	close(outs)
	k := newSpeaker(ctx, s, p, func() {})
	spoken := make(chan struct{})
	s.goSafe("speak_command", func() { k.run(outs) }, func() { close(spoken) }, k.done)
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
	return ""
}

func nothingPlaying(lang string) string {
	if langBase(lang) == "ru" {
		return "Ничего не играет."
	}
	return "Nothing is playing."
}

func homeLine(lang string, on bool, name string) string {
	if langBase(lang) == "ru" {
		if on {
			return "Включаю " + name + "."
		}
		return "Выключаю " + name + "."
	}
	if on {
		return "Turning on " + name + "."
	}
	return "Turning off " + name + "."
}
