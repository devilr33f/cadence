package telemetry

// the capabilities' JSON Schemas (draft 2020-12)
const (
	levelSchema = `{"enum":["debug","info","warn","error"]}`

	// LogsConfigSchema: the level per source (default info; at most 64 sources, as many as the pipeline keeps apart),
	// kmsg on or off (default: on where the board declares it), whether the lines of third-party children ship
	// (default on) and whether the device's spans are recorded and ship (default on; a build without tracing has none)
	LogsConfigSchema = `{"type":"object","properties":{"sources":{"type":"object","maxProperties":64,` +
		`"patternProperties":{"^[a-z][a-z0-9-]{0,31}$":` + levelSchema + `},"additionalProperties":false},` +
		`"kmsg":{"type":"boolean"},"thirdParty":{"type":"boolean"},"traces":{"type":"boolean"}},"additionalProperties":false}`

	LogsStateSchema = `{"type":"object","properties":{"dropped":{"type":"integer","minimum":0},` +
		`"rejected":{"type":"integer","minimum":0},"ringBytes":{"type":"integer","minimum":0},` +
		`"sources":{"type":"array","items":{"type":"string"}}},"required":["dropped","rejected","ringBytes","sources"],` +
		`"additionalProperties":false}`

	// SystemMetricsConfigSchema: the sampling interval in seconds (default 30)
	SystemMetricsConfigSchema = `{"type":"object","properties":{"intervalS":{"type":"integer","minimum":10,"maximum":300,` +
		`"default":30}},"additionalProperties":false}`

	// a process has no command line: it is never read
	procSchema = `{"type":"object","properties":{"pid":{"type":"integer","minimum":1},` +
		`"comm":{"type":"string","maxLength":32},"cpu":{"type":"number","minimum":0},` +
		`"rss":{"type":"integer","minimum":0}},"required":["pid","comm","cpu","rss"],"additionalProperties":false}`
	procsSchema = `{"type":"array","maxItems":10,"items":` + procSchema + `}`

	SystemMetricsStateSchema = `{"type":"object","properties":{"topCpu":` + procsSchema + `,"topRss":` + procsSchema +
		`},"required":["topCpu","topRss"],"additionalProperties":false}`
)
