package capschema

// AudioOutputID is a device's speaker output.
const AudioOutputID = "audio.output"

// AudioDelayMinMs and AudioDelayMaxMs bound audio.output's delayMs, the board's audio delay trim: what the output path
// adds after audiod (a tv's hdmi chain, a soundbar), or takes back where the device reports too much.
const (
	AudioDelayMinMs = -1000
	AudioDelayMaxMs = 1000
)

const delayMs = `"delayMs":{"type":"integer","minimum":-1000,"maximum":1000}`

// AudioOutputConfig is audio.output's config on a speaker board: delayMs alone, 0 when absent. The level is the
// device's own, set by command.
const AudioOutputConfig = `{"type":"object","properties":{` + delayMs + `},"additionalProperties":false}`

// AudioOutputScreenConfig is audio.output's config on a screen board: the delay trim, and whose volume the cadence
// level is. software (the default) is cadence's own gain, the tv's or hdmi volume staying the remote's; system maps
// it to android's STREAM_MUSIC or alsa's Master. the android-tv board's level is always STREAM_MUSIC (its audiod
// Configure), whatever the mode says.
const AudioOutputScreenConfig = `{"type":"object","properties":{` + delayMs + `,` +
	`"volume":{"type":"object","properties":{"mode":{"enum":["software","system"]}},"additionalProperties":false}` +
	`},"additionalProperties":false}`
