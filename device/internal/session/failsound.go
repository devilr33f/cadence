package session

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
)

// the spoken prompts a failure plays (share/prompts/<lang>/<id>.opus); audiod plays its error earcon for one it lacks
const (
	PromptOffline  = "offline"
	PromptFailed   = "failed"
	PromptNotHeard = "not_heard"
)

// the session capability's errorSound: the spoken prompt, the error earcon alone, or nothing
const (
	ErrorSoundPrompt = "prompt"
	ErrorSoundBeep   = "beep"
	ErrorSoundOff    = "off"
)

// DefaultLanguage is the prompts' language until the server sends the device's
const DefaultLanguage = "en"

// offlineEvery is how often repeated presses without a link may say so; the lights blink at every one
const offlineEvery = 10 * time.Second

// FailurePrompt is the prompt the end of a session with code calls for, "" for none: the link lost (offline), the
// server's speech recognition failed (not_heard), the device's microphone or any other step of the server failed
// (failed). A session that ended as it should (ok, no_speech, a follow-up's own ends) or by the owner's hand
// (stopped, cancelled, superseded, a command word, the switch, a ring) says nothing.
func FailurePrompt(code string) string {
	switch {
	case code == "send_failed":
		return PromptOffline
	case code == "mic_failed":
		return PromptFailed
	case strings.HasPrefix(code, "stt_"):
		return PromptNotHeard
	case strings.HasPrefix(code, "tts_"):
		return PromptFailed
	}
	switch code {
	case "llm_failed", "provider_error", "rate_limited", "auth", "timeout", "internal", "not_configured":
		return PromptFailed
	}
	return ""
}

// ErrorLights shows a failure on the lights: Offline blinks for want of a link, Error pulses red once.
type ErrorLights interface {
	Offline()
	Error()
}

// ErrorSounds tells the owner a request failed, by the prompt, the beep or nothing as the session capability's
// errorSound says, and on the lights whatever it says. One serves the device: core's refusals without a link and every
// link's controller. Callers decide whether a failure is told at all (never during a ring, never for test audio); it
// holds offline prompts to one per offlineEvery. Safe for concurrent use.
type ErrorSounds struct {
	speaker audio.ErrorSounder
	lights  ErrorLights
	log     *slog.Logger
	now     func() time.Time

	mu          sync.Mutex
	mode        string
	lang        string
	lastOffline time.Time
}

// NewErrorSounds plays nothing and shows nothing until UseSpeaker and UseLights.
func NewErrorSounds(log *slog.Logger) *ErrorSounds {
	return &ErrorSounds{log: log, now: time.Now, mode: ErrorSoundPrompt, lang: DefaultLanguage}
}

// UseSpeaker plays on speaker from now on, when it is an audio.ErrorSounder.
func (e *ErrorSounds) UseSpeaker(speaker audio.Speaker) {
	sp, _ := speaker.(audio.ErrorSounder)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.speaker = sp
}

// UseLights shows on lights from now on; nil shows nothing.
func (e *ErrorSounds) UseLights(lights ErrorLights) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lights = lights
}

// Set is the session capability's config: an unknown mode is the prompt, an empty lang the default.
func (e *ErrorSounds) Set(mode, lang string) {
	if mode != ErrorSoundBeep && mode != ErrorSoundOff {
		mode = ErrorSoundPrompt
	}
	if lang == "" {
		lang = DefaultLanguage
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode, e.lang = mode, lang
}

// Language is the prompts' language, the session config's.
func (e *ErrorSounds) Language() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lang
}

// Play tells the failure id; nil plays nothing.
func (e *ErrorSounds) Play(id string) {
	if e == nil || id == "" {
		return
	}
	e.mu.Lock()
	speaker, lights, mode, lang := e.speaker, e.lights, e.mode, e.lang
	quiet := false
	if id == PromptOffline {
		now := e.now()
		quiet = !e.lastOffline.IsZero() && now.Sub(e.lastOffline) < offlineEvery
		if !quiet {
			e.lastOffline = now
		}
	}
	e.mu.Unlock()
	if lights != nil {
		if id == PromptOffline {
			lights.Offline()
		} else {
			lights.Error()
		}
	}
	if quiet || mode == ErrorSoundOff || speaker == nil {
		return
	}
	e.log.Info("error_prompt", "id", id, "sound", mode)
	if mode == ErrorSoundBeep {
		speaker.ErrorSound("", "")
		return
	}
	speaker.ErrorSound(id, lang)
}
