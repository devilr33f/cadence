package capschema

// InputRemoteID is a screen's remote: keys from HA or the dashboard, injected as if pressed.
const InputRemoteID = "input.remote"

// InputRemoteConfig is input.remote's config.
const InputRemoteConfig = `{"type":"object","properties":{` +
	`"cec":{"type":"boolean"},` +
	`"longPressMs":{"type":"integer","minimum":300,"maximum":2000}` +
	`},"additionalProperties":false}`

// InputRemoteState is input.remote's state.
const InputRemoteState = `{"type":"object","properties":{` +
	`"lastSource":{"enum":["none","remote","cec","keyboard","gamepad","api"]}` +
	`},"required":["lastSource"],"additionalProperties":false}`

// Keys are the keys input.remote/key takes, screen/v1's Key in lower case.
var Keys = []string{
	"up", "down", "left", "right", "ok", "back", "home", "menu", "assistant", "play_pause", "play", "pause", "stop",
	"next", "previous", "fast_forward", "rewind", "volume_up", "volume_down", "volume_mute", "power",
}

// InputRemoteCommands are input.remote's commands. a key never raises the device's own level (rule 7): volume_up
// reaches the tv's volume over cec in the software volume mode, and is refused in the system mode unless the caller
// may set volume.
var InputRemoteCommands = map[string]string{
	"key": `{"type":"object","properties":{` +
		`"key":{"enum":` + quoted(Keys) + `},` +
		`"action":{"enum":["press","down","up","long"]}` +
		`},"required":["key"],"additionalProperties":false}`,
	"text": `{"type":"object","properties":{"value":{"type":"string","minLength":1,"maxLength":200}},` +
		`"required":["value"],"additionalProperties":false}`,
}

func quoted(ss []string) string {
	b := []byte{'['}
	for i, s := range ss {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, s...)
		b = append(b, '"')
	}
	return string(append(b, ']'))
}
