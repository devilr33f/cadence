package policy_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/policy"
)

// the speaker acts on тише alone, within 2 s of the last reply frame or while media plays with nothing streaming, on
// the stop words while a ring sounds, and on the stop words that pause its own media while it plays with no session
// streaming; the streamed microphone carries every other word to the server, which tells the owner's speech from the
// reply's echo
func TestCommandWords(t *testing.T) {
	replied := policy.CommandFacts{Replied: true, SinceReply: 300 * time.Millisecond}
	streaming := replied
	streaming.Streaming = true
	ringing := policy.CommandFacts{Ringing: true}
	playing := policy.CommandFacts{Playing: true}
	ignored := func(reason string) policy.CommandAction {
		return policy.CommandAction{Kind: policy.Ignore, Reason: reason}
	}
	for _, tc := range []struct {
		name  string
		word  string
		facts policy.CommandFacts
		want  policy.CommandAction
	}{
		{"unknown", "алиса", replied, ignored("unknown")},
		{"empty", "", replied, ignored("unknown")},
		{"spaces trimmed", " тише\n", replied, policy.CommandAction{Kind: policy.VolumeDown}},

		{"quieter after a reply", "тише", replied, policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter ends a streaming session", "тише", streaming, policy.CommandAction{Kind: policy.VolumeDown, EndSession: true}},
		{"quieter at the gate's edge", "тише", policy.CommandFacts{Replied: true, SinceReply: 2 * time.Second}, policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter past the gate with nothing streaming", "тише",
			policy.CommandFacts{Replied: true, SinceReply: 2*time.Second + time.Millisecond},
			policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter with nothing playing at all", "тише", policy.CommandFacts{}, policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter before any reply", "тише", policy.CommandFacts{Streaming: true}, ignored("gate")},
		{"quieter in test audio", "тише", policy.CommandFacts{Replied: true, Injected: true, Streaming: true}, ignored("injected")},
		{"quieter while media plays", "тише", playing, policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter while media plays past the gate", "тише",
			policy.CommandFacts{Replied: true, SinceReply: 2*time.Second + time.Millisecond, Playing: true},
			policy.CommandAction{Kind: policy.VolumeDown}},
		{"quieter while media plays and streams", "тише",
			policy.CommandFacts{Replied: true, SinceReply: 2*time.Second + time.Millisecond, Playing: true, Streaming: true},
			ignored("gate")},

		{"louder after a reply", "громче", replied, policy.CommandAction{Kind: policy.VolumeUp}},
		{"louder ends a streaming session", "громче", streaming, policy.CommandAction{Kind: policy.VolumeUp, EndSession: true}},
		{"louder past the gate with nothing streaming", "громче",
			policy.CommandFacts{Replied: true, SinceReply: 2*time.Second + time.Millisecond},
			policy.CommandAction{Kind: policy.VolumeUp}},
		{"louder with nothing playing at all", "громче", policy.CommandFacts{}, policy.CommandAction{Kind: policy.VolumeUp}},
		{"louder while media plays", "громче", playing, policy.CommandAction{Kind: policy.VolumeUp}},
		{"louder in test audio", "громче", policy.CommandFacts{Replied: true, Injected: true, Streaming: true}, ignored("injected")},
		{"louder before any reply, streaming", "громче", policy.CommandFacts{Streaming: true}, ignored("gate")},
		{"stop outside a ring", "стоп", streaming, ignored("server")},
		{"enough outside a ring", "хватит", replied, ignored("server")},
		{"pause outside a ring", "пауза", policy.CommandFacts{}, ignored("server")},
		{"stop pauses playing media", "стоп", playing, policy.CommandAction{Kind: policy.PauseMedia}},
		{"enough pauses playing media", "хватит", playing, policy.CommandAction{Kind: policy.PauseMedia}},
		{"pause pauses playing media", "пауза", playing, policy.CommandAction{Kind: policy.PauseMedia}},
		{"stop while media plays and streams", "стоп",
			policy.CommandFacts{Playing: true, Streaming: true}, ignored("server")},
		{"stop in test audio while media plays", "стоп",
			policy.CommandFacts{Playing: true, Injected: true}, ignored("server")},
		{"muted stop while media plays", "стоп", policy.CommandFacts{Playing: true, Muted: true}, ignored("muted")},

		{"stop rings off", "стоп", ringing, policy.CommandAction{Kind: policy.StopRing}},
		{"enough rings off", "хватит", ringing, policy.CommandAction{Kind: policy.StopRing}},
		{"pause rings off", "пауза", ringing, policy.CommandAction{Kind: policy.StopRing}},
		{"quieter during a ring", "тише", policy.CommandFacts{Ringing: true, Replied: true}, ignored("ringing")},
		{"louder during a ring", "громче", ringing, ignored("ringing")},

		{"muted quieter", "тише", policy.CommandFacts{Muted: true, Replied: true, Streaming: true}, ignored("muted")},
		{"muted stop during a ring", "стоп", policy.CommandFacts{Muted: true, Ringing: true}, ignored("muted")},

		{"next skips playing media", "дальше", playing, policy.CommandAction{Kind: policy.MediaNext}},
		{"the other next skips playing media", "следующий", playing, policy.CommandAction{Kind: policy.MediaNext}},
		{"previous skips playing media back", "назад", playing, policy.CommandAction{Kind: policy.MediaPrevious}},
		{"next with nothing playing", "дальше", replied, ignored("server")},
		{"previous in test audio", "назад", policy.CommandFacts{Playing: true, Injected: true}, ignored("server")},
		{"next while a session streams", "дальше",
			policy.CommandFacts{Playing: true, Streaming: true}, ignored("server")},
		{"next during a ring", "дальше", ringing, ignored("ringing")},

		{"resume continues what stands", "продолжи", policy.CommandFacts{}, policy.CommandAction{Kind: policy.MediaResume}},
		{"play continues what stands", "играй", policy.CommandFacts{}, policy.CommandAction{Kind: policy.MediaResume}},
		{"resume while something plays", "продолжи", playing, ignored("server")},
		{"resume while a session streams", "продолжи", streaming, ignored("server")},
		{"resume in test audio", "играй", policy.CommandFacts{Injected: true}, ignored("server")},
		{"resume during a ring", "продолжи", ringing, ignored("ringing")},

		{"the home's light is reported", "выключи свет", policy.CommandFacts{}, policy.CommandAction{Kind: policy.ReportWord}},
		{"the home's light reported on", "включи свет", playing, policy.CommandAction{Kind: policy.ReportWord}},
		{"the home's tv is reported", "выключи телевизор", playing, policy.CommandAction{Kind: policy.ReportWord}},
		{"the home's light in a session is the server's", "включи свет", streaming, ignored("server")},
		{"the home's light in test audio", "выключи свет", policy.CommandFacts{Injected: true}, ignored("server")},
		{"the home's light during a ring", "включи свет", ringing, ignored("ringing")},
		{"the home's light muted", "выключи свет", policy.CommandFacts{Muted: true}, ignored("muted")},
		{"navigation is the server's", "домой", playing, ignored("unknown")},
		{"bluetooth is the server's", "включи блютуз", playing, ignored("unknown")},
		{"a like is the server's", "лайк", playing, ignored("unknown")},
		{"bluetooth is the server's", "включи блютуз", playing, ignored("unknown")},
	} {
		if got := policy.Command(tc.word, tc.facts); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestCommandActionNames(t *testing.T) {
	for kind, want := range map[policy.ActionKind]string{
		policy.Ignore: "ignore", policy.StopRing: "stop_ring", policy.PauseMedia: "pause_media", policy.VolumeDown: "volume_down",
		policy.MediaNext: "media_next", policy.MediaPrevious: "media_previous", policy.MediaResume: "media_resume",
	} {
		if got := kind.String(); got != want {
			t.Errorf("%d: %q, want %q", kind, got, want)
		}
	}
}
