package capschema

// SettingsSetArgs is rpc settings.set's arguments: what a screen's settings page writes for its own device. At least
// one key; each is validated again by the server against the setting it writes. language is the device's language
// setting and display.screen's config language too (the screen's ui), followUpWindowMs the device's followUp.windowMs;
// screen holds display.screen config keys, merged into the device's desired display.screen config, so their bounds
// are DisplayScreenConfig's.
const SettingsSetArgs = `{"type":"object","properties":{` +
	`"language":{"enum":["en","ru"]},` +
	`"followUpWindowMs":{"type":"integer","minimum":2000,"maximum":15000},` +
	`"screen":{"type":"object","properties":{` +
	`"subtitles":{"type":"object","properties":{"size":{"enum":["s","m","l"]},"style":{"enum":["box","outline"]}},` +
	`"additionalProperties":false},` +
	`"overscan":{"enum":[0,2.5,5]},` +
	`"autoSkipOpenings":{"type":"boolean"},` +
	`"reducedMotion":{"type":"boolean"},` +
	`"visualizer":{"enum":["off","classic","drive","scenes","milkdrop"]},` +
	`"vizScene":{"enum":["auto","cover","warp","tunnel"]},` +
	`"vizQuality":{"enum":["low","medium","high","max","native"]},` +
	`"vizShape":{"enum":["auto","triangle","square","pentagon","hexagon","circle","star","diamond"]}` +
	`},"minProperties":1,"additionalProperties":false}` +
	`},"minProperties":1,"additionalProperties":false}`
