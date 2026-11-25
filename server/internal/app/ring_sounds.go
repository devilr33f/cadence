package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// how often unused ring sounds are looked for
const ringSoundsGCEvery = time.Hour

// ringVoice is the device's tts as its turns speak: the instance, voice and language its settings name
func ringVoice(effective func(ctx context.Context, deviceID string) (settings.Values, error),
	tts func(ctx context.Context, name string) (providers.TTS, error)) ringsounds.Voice {
	return func(ctx context.Context, deviceID string) (providers.TTS, string, string, error) {
		v, err := effective(ctx, deviceID)
		if err != nil {
			return nil, "", "", err
		}
		if v.TTS == "" {
			return nil, "", "", ringsounds.ErrNoTTS
		}
		engine, err := tts(ctx, v.TTS)
		if err != nil {
			return nil, "", "", err
		}
		return engine, v.Voice, v.Language, nil
	}
}

// alarmMusic makes an alarm's ring sound of the song a music_search result id or a query names, fetched by the
// server from the player's stream (the address that resolved it), never through the device
func alarmMusic(sounds *ringsounds.Service, kit *tools.MusicKit, upstream func(ctx context.Context, deviceID string, t providers.Track) (string, error)) tools.AlarmMusic {
	client := httpx.Client(nil)
	return func(ctx context.Context, deviceID, ref string) (ringsounds.Sound, error) {
		if !sounds.CanMakeMusic() {
			return ringsounds.Sound{}, ringsounds.ErrNoTranscoder
		}
		t, ok := kit.Song(ctx, deviceID, ref)
		if !ok {
			return ringsounds.Sound{}, ringsounds.ErrNotFound
		}
		title := t.Title
		if t.Artist != "" {
			title = t.Artist + " — " + t.Title
		}
		key := t.Source + ":" + t.ID + ":" + t.VideoID
		return sounds.Music(ctx, deviceID, key, title, func(ctx context.Context) (ringsounds.Body, error) {
			u, err := upstream(ctx, deviceID, t)
			if err != nil {
				return nil, err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return nil, errors.New("ringsounds: bad upstream")
			}
			resp, err := client.Do(req)
			if err != nil {
				// the transport's error quotes the signed url
				return nil, errors.New("ringsounds: upstream failed")
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				return nil, errors.New("ringsounds: upstream refused")
			}
			return resp.Body, nil
		})
	}
}

// ringSoundRefs reads which sounds each enrolled device's alarms and reminders name, from the state it last reported
func ringSoundRefs(list func(ctx context.Context) ([]enroll.Listed, error),
	state func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)) ringsounds.Refs {
	return func(ctx context.Context) (map[string][]string, error) {
		devs, err := list(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string][]string{}
		for _, d := range devs {
			raw, ok := state(ctx, d.ID, alarmsCapability)
			if !ok {
				continue
			}
			var st struct {
				Alarms    []struct{ SoundID string } `json:"alarms"`
				Reminders []struct{ SoundID string } `json:"reminders"`
			}
			if json.Unmarshal(raw, &st) != nil {
				continue
			}
			for _, a := range st.Alarms {
				if a.SoundID != "" {
					out[d.ID] = append(out[d.ID], a.SoundID)
				}
			}
			for _, r := range st.Reminders {
				if r.SoundID != "" {
					out[d.ID] = append(out[d.ID], r.SoundID)
				}
			}
		}
		return out, nil
	}
}

// reminderArgs are what a caller of the server's API (Home Assistant, a screen) sends as alarms.set_reminder: the
// whole text, spoken by the server, and the minutes from now in place of the device's own label and seconds
type reminderAPIArgs struct {
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Label     string          `json:"label"`
	Time      string          `json:"time"`
	Days      json.RawMessage `json:"days"`
	Date      string          `json:"date"`
	InMinutes int             `json:"inMinutes"`
	InSeconds int             `json:"inSeconds"`
	Level     json.RawMessage `json:"level"`
	SessionID string          `json:"sessionId"`
}

// reminderIntercept takes an alarms.set_reminder that carries text or inMinutes (the API's shape: Home Assistant's
// cadence.set_reminder) and sends the device its own: the text spoken into a ring sound (set anyway when that fails:
// the device then says its bundled prompt), its first 80 runes as the label, inMinutes as inSeconds, an id when none
// was given. one with neither passes to the device as it is (the agent's tool already speaks it).
func reminderIntercept(sounds tools.RingSounds, deliver func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)) func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
	return func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, bool, error) {
		if capability != alarmsCapability || name != "set_reminder" {
			return nil, false, nil
		}
		var a reminderAPIArgs
		if json.Unmarshal(args, &a) != nil || a.Text == "" && a.InMinutes == 0 {
			return nil, false, nil
		}
		out := map[string]any{}
		if a.SessionID != "" {
			out["sessionId"] = a.SessionID
		}
		if a.Time != "" {
			out["time"] = a.Time
		}
		if len(a.Days) > 0 {
			out["days"] = a.Days
		}
		if a.Date != "" {
			out["date"] = a.Date
		}
		if len(a.Level) > 0 {
			out["level"] = a.Level
		}
		switch {
		case a.InMinutes > 0:
			out["inSeconds"] = a.InMinutes * 60
		case a.InSeconds > 0:
			out["inSeconds"] = a.InSeconds
		}
		id := a.ID
		if id == "" {
			id = reminderID()
		}
		out["id"] = id
		text := strings.TrimSpace(a.Text)
		lbl := a.Label
		if text != "" {
			lbl = tools.ReminderLabel(text)
			if s, err := sounds.Speech(ctx, deviceID, text); err == nil {
				out["soundId"] = s.ID
			}
		}
		out["label"] = lbl
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, true, err
		}
		res, err := deliver(ctx, deviceID, capability, name, raw)
		return res, true, err
	}
}

// reminderID is a fresh [a-z0-9]{8}; the device refuses nothing for an id it holds: the set replaces it
func reminderID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 0, 8)
	var r [16]byte
	for len(b) < 8 {
		_, _ = rand.Read(r[:])
		for _, c := range r {
			if c < 252 && len(b) < 8 {
				b = append(b, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(b)
}
