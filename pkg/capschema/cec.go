package capschema

// DisplayCecID is the hdmi cec bus a screen sits on: the devices the wire carries (the tv, an av receiver),
// their power and audio, and the commands that move them (one touch play, standby, volume keys, system
// audio). A board without cec (no /dev/cec, an app the platform keeps from it) reports present:false and
// refuses every command but refresh.
const DisplayCecID = "display.cec"

// DisplayCecConfig is display.cec's config. enabled false keeps the board from ever transmitting; keys a
// remote sends still arrive (the screen's, the board's volume buttons).
const DisplayCecConfig = `{"type":"object","properties":{` +
	`"enabled":{"type":"boolean"}` +
	`},"additionalProperties":false}`

// an address on the cec wire, "n.n.n.n" (each n 0-15, the leading ones may be dropped: "2", "2.0.0.0")
const cecPhysicalAddress = `{"type":"string","pattern":"^(1[0-5]|[0-9])(\\.(1[0-5]|[0-9])){0,3}$","maxLength":15}`

// DisplayCecState is display.cec's state: the adapter as the kernel (or the app's bridge) named it and the
// devices the last scan heard. names are the wire's osd names (they live in the db like device names do,
// dashboard-only, never logged, rule 8); unknown power is a device that answered its poll but no status.
const DisplayCecState = `{"type":"object","properties":{` +
	`"adapter":{"type":"object","properties":{` +
	`"present":{"type":"boolean"},` +
	`"physicalAddress":` + cecPhysicalAddress + `,` +
	`"logicalAddress":{"type":"integer","minimum":0,"maximum":15},` +
	`"deviceType":{"enum":["tv","recording","tuner","playback","audio_system"]}` +
	`},"required":["present"],"additionalProperties":false},` +
	`"systemAudio":{"type":"object","properties":{` +
	`"available":{"type":"boolean"},` +
	`"enabled":{"enum":["on","off","unknown"]}` +
	`},"required":["available","enabled"],"additionalProperties":false},` +
	`"volumeTarget":{"enum":["tv","audio_system"]},` +
	`"devices":{"type":"array","maxItems":16,"items":{"type":"object","properties":{` +
	`"logicalAddress":{"type":"integer","minimum":0,"maximum":15},` +
	`"type":{"enum":["tv","recording","reserved","tuner","playback","audio_system","switch","processor","free"]},` +
	`"physicalAddress":` + cecPhysicalAddress + `,` +
	`"vendorId":{"type":"integer","minimum":0,"maximum":16777215},` +
	`"name":{"type":"string","maxLength":14},` +
	`"power":{"enum":["on","standby","to_on","to_off","unknown"]},` +
	`"audioStatus":{"type":"object","properties":{` +
	`"volume":{"type":"integer","minimum":0,"maximum":100},` +
	`"muted":{"type":"boolean"}` +
	`},"required":["volume","muted"],"additionalProperties":false},` +
	`"supports":{"type":"array","items":{"enum":["power","volume","audio_status","system_audio"]},"uniqueItems":true}` +
	`},"required":["logicalAddress","type","power"],"additionalProperties":false}}` +
	`},"required":["adapter","systemAudio","devices"],"additionalProperties":false}`

// DisplayCecCommands are display.cec's commands: an authenticated caller's press (the dashboard, home
// assistant), as the quick panel's slider is (rule 7); the agent has no tool for them.
var DisplayCecCommands = map[string]string{
	"refresh": noArgs,
	"power": `{"type":"object","properties":{` +
		`"action":{"enum":["on","off","toggle"]}` +
		`},"required":["action"],"additionalProperties":false}`,
	"volume": `{"type":"object","properties":{` +
		`"action":{"enum":["up","down","mute","unmute","toggle_mute"]},` +
		`"steps":{"type":"integer","minimum":1,"maximum":8}` +
		`},"required":["action"],"additionalProperties":false}`,
	"system_audio": `{"type":"object","properties":{` +
		`"enabled":{"type":"boolean"}` +
		`},"required":["enabled"],"additionalProperties":false}`,
	"active_source": noArgs,
}

// bounds the board and the server share
const (
	// CEC OSD names are 14 bytes at most on the wire
	CecNameMaxBytes = 14
	// one command sends at most this many volume presses, as a held key
	CecVolumeMaxSteps = 8
)
