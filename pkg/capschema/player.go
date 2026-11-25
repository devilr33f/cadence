package capschema

// media.player's commands for item 9's rich sessions, beside its transport (pause, seek, next, ...). the device's
// media.player adds them to its descriptor once it handles them; until then the server never sends them.
var (
	// MediaPlayerItemActionArgs runs one of a session's item_actions (like, save, ...) on the server's provider.
	MediaPlayerItemActionArgs = `{"type":"object","properties":{` +
		`"sessionId":{"type":"string","minLength":1,"maxLength":64},` + ref("actionId") +
		`},"required":["sessionId","actionId"],"additionalProperties":false}`
	// MediaPlayerJumpArgs plays a queue item by its ref.
	MediaPlayerJumpArgs = `{"type":"object","properties":{` +
		`"sessionId":{"type":"string","minLength":1,"maxLength":64},` + ref("ref") +
		`},"required":["ref"],"additionalProperties":false}`
)
