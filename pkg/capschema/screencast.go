package capschema

// MediaScreencastID is airplay on a screen (libairplay): audio, mirroring and the pin.
const MediaScreencastID = "media.screencast"

// MediaScreencastConfig is media.screencast's config.
const MediaScreencastConfig = `{"type":"object","properties":{` +
	`"enabled":{"type":"boolean"},` +
	`"name":{"type":"string","minLength":1,"maxLength":32},` +
	`"pin":{"type":"boolean"},` +
	`"maxHeight":{"enum":[720,1080,2160]},` +
	`"h265":{"type":"boolean"}` +
	`},"additionalProperties":false}`

// MediaScreencastState is media.screencast's state; the sender's name is memory-only on the server (D11).
const MediaScreencastState = `{"type":"object","properties":{` +
	`"enabled":{"type":"boolean"},` +
	`"state":{"enum":["off","starting","ready","failed"]},` +
	`"code":{"type":"string","maxLength":64},` +
	`"mirroring":{"type":"boolean"},` +
	`"clientName":{"type":"string","maxLength":64}` +
	`},"required":["enabled","state","mirroring"],"additionalProperties":false}`
