package policy

import (
	"strings"
	"time"
)

// CommandGate is how long after the last reply frame the player took тише still acts (v1).
const CommandGate = 2 * time.Second

// ActionKind is what a command word does on the speaker.
type ActionKind uint8

const (
	Ignore ActionKind = iota
	// stop the ring that sounds
	StopRing
	// pause the media that plays, offline: no session streams, so the server
	// cannot hear the word
	PauseMedia
	// skip to the next or the previous item of the media that plays, as the pause does
	MediaNext
	MediaPrevious
	// continue the player, which restarts a queue that ended; nothing plays then
	MediaResume
	// step the level down or up, like a press
	VolumeDown
	VolumeUp
	// a home phrase (включи/выключи свет, телевизор): only the server can act on it,
	// so the speaker hands the heard phrase to it
	ReportWord
)

func (k ActionKind) String() string {
	switch k {
	case StopRing:
		return "stop_ring"
	case PauseMedia:
		return "pause_media"
	case MediaNext:
		return "media_next"
	case MediaPrevious:
		return "media_previous"
	case MediaResume:
		return "media_resume"
	case VolumeDown:
		return "volume_down"
	case VolumeUp:
		return "volume_up"
	case ReportWord:
		return "report_word"
	}
	return "ignore"
}

// CommandAction is what to do about a command word. EndSession also ends the session that streams the microphone:
// the word reached the server too, where it would barge in and be answered. Reason says why an ignored word was:
// unknown, muted, ringing, server, gate or injected.
type CommandAction struct {
	Kind       ActionKind
	EndSession bool
	Reason     string
}

// CommandFacts are what the speaker knows when its command model hears a word.
type CommandFacts struct {
	// the microphone switch is on
	Muted bool
	// an alarm or a timer rings
	Ringing bool
	// the player took a reply frame, SinceReply ago
	Replied    bool
	SinceReply time.Duration
	// test audio (the sim's say file) spoke the last reply's session, or speaks in the one that runs
	Injected bool
	// a session streams the microphone
	Streaming bool
	// the player plays media (music, a cast), whatever session streams
	Playing bool
}

// Command decides what a command word does (M6a-D6, v1). тише steps the level down within CommandGate of the last
// reply frame, and with no session streaming at all — media playing or not, nothing else carries the word to the
// server then; громче steps up under the same conditions (owner 2026-11-21). While a ring
// sounds стоп, хватит and пауза stop it, and while media plays with no session streaming they pause it themselves —
// the server is not needed for either, so they work offline; a song's own words can reach the spotter then, which the
// owner accepted. The media words (дальше/следующий, назад) skip in what plays under the same rule, and продолжи/играй
// continue a player that stands (which restarts a queue that ended); the model's other phrases — navigation, the home's
// включи/выключи, блютуз, лайк — have no action of their own here and wait for the server.
// owner accepted. While a session streams the stop words and every other word are left to the server, which hears them
// on the streamed microphone and tells the owner's speech from the reply's echo. Nothing acts while the switch mutes
// the microphone.
func Command(word string, f CommandFacts) CommandAction {
	kind := classify(word)
	switch {
	case kind == unknownWord:
		return ignore("unknown")
	case f.Muted:
		return ignore("muted")
	case f.Ringing:
		if kind == stopWord {
			return CommandAction{Kind: StopRing}
		}
		return ignore("ringing")
	case kind == stopWord:
		if f.Streaming || f.Injected || !f.Playing {
			return ignore("server")
		}
		return CommandAction{Kind: PauseMedia}
	case kind == nextWord, kind == previousWord:
		// a skip needs something playing; with nothing the server answers «Ничего не играет»
		if f.Streaming || f.Injected || !f.Playing {
			return ignore("server")
		}
		if kind == nextWord {
			return CommandAction{Kind: MediaNext}
		}
		return CommandAction{Kind: MediaPrevious}
	case kind == homeWord:
		// the home's phrases act server-side; a session's microphone carries them there,
		// and with none the speaker reports the heard phrase itself
		if f.Streaming || f.Injected {
			return ignore("server")
		}
		return CommandAction{Kind: ReportWord}
	case kind == resumeWord:
		// what plays needs no resume; the word with something playing may be the model's to hear
		if f.Streaming || f.Injected || f.Playing {
			return ignore("server")
		}
		return CommandAction{Kind: MediaResume}
	case kind != quieterWord && kind != louderWord:
		return ignore("server")
	}
	// тише and громче step the level the same way (owner 2026-11-21: the spoken громче raises it
	// outside sessions too, a reply's own echo of the word accepted as тише's always was)
	step := VolumeDown
	if kind == louderWord {
		step = VolumeUp
	}
	switch {
	case f.Injected:
		return ignore("injected")
	case f.Replied && f.SinceReply <= CommandGate:
		return CommandAction{Kind: step, EndSession: f.Streaming}
	case !f.Streaming:
		// with no session the word reaches nobody else, playing or not: the speaker steps it itself
		return CommandAction{Kind: step}
	}
	return ignore("gate")
}

type wordKind uint8

const (
	unknownWord wordKind = iota
	stopWord
	quieterWord
	louderWord
	nextWord
	previousWord
	resumeWord
	homeWord
)

// classify takes the phrase as the command model gives it, trimmed of ascii whitespace
func classify(word string) wordKind {
	switch strings.Trim(word, " \t\n\v\f\r") {
	case "стоп", "хватит", "пауза":
		return stopWord
	case "тише":
		return quieterWord
	case "громче":
		return louderWord
	case "дальше", "следующий":
		return nextWord
	case "назад":
		return previousWord
	case "продолжи", "играй":
		return resumeWord
	case "включи свет", "выключи свет", "включи телевизор", "выключи телевизор":
		return homeWord
	}
	return unknownWord
}

func ignore(reason string) CommandAction { return CommandAction{Kind: Ignore, Reason: reason} }
