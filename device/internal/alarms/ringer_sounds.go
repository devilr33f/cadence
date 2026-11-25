package alarms

import (
	"strings"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	// the bundled prompt a reminder speaks when the server's speech of it cannot be heard
	promptReminder = "reminder"
	// until the session config names the device's language
	defaultPromptLang = "ru"
	// audiod takes a ring's url up to this long
	maxSoundURL = 512
	// the pause after a reminder's speech, and after its bundled prompt
	reminderPauseMs = 2000
	promptPauseMs   = 3000
)

// Sounds is where the server keeps its ring sounds (an alarm's music, a reminder's speech): the url audiod streams one
// from and the Authorization header that fetches it, never part of the url (rule 6).
type Sounds interface {
	RingSound(id string) (url, authorization string, ok bool)
}

// RingSpeaker sounds a ring of segments; a Speaker without it loops the ring's tone alone.
type RingSpeaker interface {
	StartRing(start *audiodv1.ToneStart) error
}

type serverSounds struct{ base, authorization string }

// ServerSounds fetches the ring sounds from the server at serverURL (GET /v2/ring-sound/{id}) with the device token;
// nil without either.
func ServerSounds(serverURL, token string) Sounds {
	base := strings.TrimRight(serverURL, "/")
	if token == "" || !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil
	}
	return serverSounds{base: base, authorization: "Bearer " + token}
}

func (s serverSounds) RingSound(id string) (string, string, bool) {
	url := s.base + "/v2/ring-sound/" + id
	if !ValidID(id) || len(url) > maxSoundURL {
		return "", "", false
	}
	return url, s.authorization, true
}

// UseSounds has the rings that name a sound stream it from s; until then, or with nil, they ring their tones.
func (r *Ringer) UseSounds(s Sounds) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sounds = s
}

// UseLanguage reads the language of the bundled prompts a ring speaks (the session config's) from lang at each ring.
func (r *Ringer) UseLanguage(lang func() string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lang = lang
}

// toneKind is the tone a ring loops on a speaker that takes no segments, and a ring's fallback
func toneKind(k Kind) audiodv1.ToneKind {
	switch k {
	case KindAlarm:
		return audiodv1.ToneKind_TONE_KIND_ALARM
	case KindReminder:
		return audiodv1.ToneKind_TONE_KIND_REMINDER
	default:
		return audiodv1.ToneKind_TONE_KIND_TIMER
	}
}

// toneStart is the ring's ToneStart: an alarm's music looped over its tone as the fallback; a reminder's chime, its
// speech and a pause, looped, over its chime, the bundled prompt and a pause; nil for a ring of the tone alone (a
// timer, an alarm without music). callers hold mu
func (r *Ringer) toneStart(c *ring) *audiodv1.ToneStart {
	start := &audiodv1.ToneStart{Id: c.key, Kind: toneKind(c.Kind), Loop: true}
	var url, authorization string
	streams := false
	if c.SoundID != "" && r.sounds != nil {
		url, authorization, streams = r.sounds.RingSound(c.SoundID)
	}
	switch c.Kind {
	case KindAlarm:
		if !streams {
			return nil
		}
		start.Segments = []*audiodv1.RingSegment{{Url: url}}
	case KindReminder:
		chime := &audiodv1.RingSegment{Tone: audiodv1.ToneKind_TONE_KIND_REMINDER}
		lang := defaultPromptLang
		if r.lang != nil {
			if l := r.lang(); l != "" {
				lang = l
			}
		}
		spoken := []*audiodv1.RingSegment{
			chime, {PromptId: promptReminder, Lang: lang}, {SilenceMs: promptPauseMs},
		}
		start.Segments = spoken
		if streams {
			start.Segments = []*audiodv1.RingSegment{chime, {Url: url}, {SilenceMs: reminderPauseMs}}
			start.Fallback = spoken
		}
	default:
		return nil
	}
	if streams {
		start.UrlAuthorization = authorization
	}
	return start
}
