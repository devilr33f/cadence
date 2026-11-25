package app

import (
	"context"
	"encoding/json"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
)

type speechFake struct {
	texts []string
	fail  bool
}

func (s *speechFake) Speech(_ context.Context, _, text string) (ringsounds.Sound, error) {
	s.texts = append(s.texts, text)
	if s.fail {
		return ringsounds.Sound{}, ringsounds.ErrTTS
	}
	return ringsounds.Sound{ID: "snd0000000000001", Title: text}, nil
}

func (s *speechFake) Info(context.Context, string, []string) map[string]ringsounds.Info { return nil }

// Home Assistant's cadence.set_reminder sends the text and the minutes; the device gets its own shape: the speech's
// sound, the label and seconds. what the agent's tool sends passes as it is
func TestReminderInterceptSpeaksTheAPIsText(t *testing.T) {
	speech := &speechFake{}
	var sent []string
	deliver := func(_ context.Context, _, capability, name string, args []byte) (json.RawMessage, error) {
		sent = append(sent, capability+"."+name+" "+string(args))
		return json.RawMessage(`{"reminder":{}}`), nil
	}
	f := reminderIntercept(speech, deliver)
	res, handled, err := f(context.Background(), "d1", "alarms", "set_reminder",
		[]byte(`{"id":"ha1","text":"Покормить кота","inMinutes":15}`))
	if !handled || err != nil || string(res) != `{"reminder":{}}` {
		t.Fatalf("handled %t %v %s", handled, err, res)
	}
	if len(sent) != 1 || sent[0] != `alarms.set_reminder {"id":"ha1","inSeconds":900,"label":"Покормить кота","soundId":"snd0000000000001"}` {
		t.Fatalf("sent %q", sent)
	}
	// a failed speech still sets it, the device then says its own prompt; a time and days go as they came
	speech.fail = true
	sent = nil
	if _, handled, _ := f(context.Background(), "d1", "alarms", "set_reminder",
		[]byte(`{"text":"Вынести мусор","time":"20:00","days":["mon","thu"]}`)); !handled {
		t.Fatal("not handled")
	}
	var got map[string]any
	if len(sent) != 1 || json.Unmarshal([]byte(sent[0][len("alarms.set_reminder "):]), &got) != nil ||
		got["label"] != "Вынести мусор" || got["time"] != "20:00" || got["soundId"] != nil || len(got["id"].(string)) != 8 {
		t.Fatalf("sent %q", sent)
	}
	// the device's own shape, and every other command, pass through
	for _, c := range []struct{ capability, name, args string }{
		{"alarms", "set_reminder", `{"id":"x","label":"a","inSeconds":60}`},
		{"alarms", "set_timer", `{"id":"x","seconds":60}`},
		{"media.url", "set_reminder", `{"text":"a","inMinutes":1}`},
	} {
		if _, handled, _ := f(context.Background(), "d1", c.capability, c.name, []byte(c.args)); handled {
			t.Errorf("%s.%s handled", c.capability, c.name)
		}
	}
}
