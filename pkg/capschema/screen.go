package capschema

// DisplayScreenID is the screen itself: power, idle and ambient, its pages and its toasts.
const DisplayScreenID = "display.screen"

// DisplayScreenConfig is display.screen's config. language is the screen's ui language (the device's language
// setting as settings.set writes it), which cadenced hands the screen in Ready. v2 reserves bootMovie, suspendMovie and wakeMovie (each {ref, sound:
// off|on, fit: fit|crop}); they are absent here, so a v1 screen refuses them until it plays them.
const DisplayScreenConfig = `{"type":"object","properties":{` +
	`"theme":{"enum":["afterglow"]},` +
	`"language":{"enum":["en","ru"]},` +
	`"overscan":{"enum":[0,2.5,5]},` +
	`"reducedMotion":{"type":"boolean"},` +
	`"ambient":{"type":"object","properties":{` +
	`"source":{"enum":["aerial","images","artwork","mixed","aurora","off"]},` +
	`"categories":{"type":"array","items":{"enum":["space","landscapes","cities","underwater"]},"uniqueItems":true},` +
	`"idleAfterS":{"type":"integer","minimum":30,"maximum":86400},` +
	`"sleepAfterS":{"type":"integer","minimum":0,"maximum":86400},` +
	// idle over a paused video after this many seconds with no key; absent is idleAfterS, 0 is never
	`"pausedAfterS":{"type":"integer","minimum":0,"maximum":86400},` +
	`"showClock":{"type":"boolean"},"showWeather":{"type":"boolean"},"showCaptions":{"type":"boolean"},` +
	`"aerialQuality":{"enum":["1080","4k"]},` +
	`"nightDim":{"type":"object","properties":{"from":` + clock + `,"to":` + clock + `},` +
	`"required":["from","to"],"additionalProperties":false}` +
	`},"additionalProperties":false},` +
	`"visualizer":{"enum":["off","classic","drive","scenes","milkdrop"]},` +
	// the drive family's pinned scene and shape; auto lets each track pick
	`"vizScene":{"enum":["auto","cover","warp","tunnel"]},` +
	`"vizQuality":{"enum":["low","medium","high","max","native"]},` +
	// how far ahead of the sound the visualizer's picture looks, ms
	`"vizOffsetMs":{"type":"integer","minimum":-300,"maximum":500},` +
	`"vizShape":{"enum":["auto","triangle","square","pentagon","hexagon","circle","star","diamond"]},` +
	`"subtitles":{"type":"object","properties":{"size":{"enum":["s","m","l"]},"style":{"enum":["box","outline"]}},` +
	`"additionalProperties":false},` +
	`"autoSkipOpenings":{"type":"boolean"},` +
	`"cec":{"type":"boolean"}` +
	`},"additionalProperties":false}`

// "HH:MM", 24 h
const clock = `{"type":"string","pattern":"^([01][0-9]|2[0-3]):[0-5][0-9]$"}`

// DisplayScreenState is display.screen's state: what the screen is and does, nothing private (no refs, no titles).
// screen, maxHeight and codecs are the device profile the server picks renditions by.
const DisplayScreenState = `{"type":"object","properties":{` +
	`"power":{"enum":["on","ambient","sleep"]},` +
	`"route":{"enum":["home","search","title","episodes","picker","resolving","player","audio","panel","queue",` +
	`"tasks","task","settings","ambient","enroll","airplay","devices","alarms"]},` +
	`"drawerOpen":{"type":"boolean"},` +
	`"width":{"type":"integer","minimum":0},"height":{"type":"integer","minimum":0},` +
	`"refreshMhz":{"type":"integer","minimum":0},` +
	`"hdr":{"type":"array","items":{"type":"string","maxLength":32},"maxItems":8},` +
	`"screen":{"type":"boolean"},` +
	`"maxHeight":{"type":"integer","minimum":0},` +
	`"codecs":{"type":"array","items":{"type":"string","maxLength":16},"maxItems":16},` +
	`"cardVersions":{"type":"array","items":{"type":"integer","minimum":1},"maxItems":8},` +
	`"cardFeatures":{"type":"array","items":{"type":"string","pattern":"^[a-z][a-z_.]{0,31}$"},"maxItems":48},` +
	`"mic":{"type":"object","properties":{"kind":{"enum":["none","speaker","local"]},` +
	`"deviceId":{"type":"string","maxLength":64},"offline":{"type":"boolean"}},` +
	`"required":["kind"],"additionalProperties":false}` +
	`},"required":["power","screen"],"additionalProperties":false}`

// DisplayScreenCommands are display.screen's commands. v2 adds screenshot (memory-only, admin scope).
var DisplayScreenCommands = map[string]string{
	"wake":  noArgs,
	"sleep": noArgs,
	"toast": `{"type":"object","properties":{` +
		`"text":{"type":"string","minLength":1,"maxLength":280},` +
		`"kind":{"enum":["info","success","warning","danger"]},` +
		`"icon":{"type":"string","pattern":"^[a-z0-9-]{1,32}$"},` +
		`"durationMs":{"type":"integer","minimum":1000,"maximum":30000},` +
		`"action":` + toastAction +
		`},"required":["text"],"additionalProperties":false}`,
	"show": `{"type":"object","properties":{` +
		`"route":{"enum":["home","search","title","episodes","panel","queue","tasks","task","settings","back"]},` +
		ref("ref") + `,` +
		`"query":{"type":"string","maxLength":200},` +
		`"episode":{"type":"string","maxLength":32}` +
		`},"required":["route"],"additionalProperties":false}`,
}

// a toast's one button (screen/v1 ToastAction): open a page itself (a task's panel), or call rpc toast.action
var toastAction = `{"type":"object","properties":{` +
	`"id":{"type":"string","pattern":"^[A-Za-z0-9_.:-]{1,64}$"},` +
	`"label":{"type":"string","minLength":1,"maxLength":40},` +
	`"kind":{"enum":["open","rpc"]},` +
	`"route":{"enum":["home","search","title","episodes","panel","queue","tasks","task","settings"]},` +
	ref("ref") +
	`},"required":["id","label","kind"],"additionalProperties":false,` +
	`"if":{"properties":{"kind":{"const":"open"}}},"then":{"required":["route"]}}`

const noArgs = `{"type":"object","additionalProperties":false}`

// an opaque, server-issued ref
func ref(name string) string {
	return `"` + name + `":{"type":"string","minLength":1,"maxLength":256}`
}
