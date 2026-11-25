package capschema

// MediaVideoID is a screen's player. Its sessions are media.player's like any other (pause, seek, next, ...).
const MediaVideoID = "media.video"

// MediaVideoConfig is media.video's config.
const MediaVideoConfig = `{"type":"object","properties":{` +
	`"hwdec":{"type":"string","pattern":"^[a-z0-9-]{1,32}$"},` +
	`"audioLanguage":` + lang + `,` +
	`"subtitleLanguage":` + lang + `,` +
	`"bufferS":{"type":"integer","minimum":1,"maximum":600}` +
	`},"additionalProperties":false}`

// bcp 47, short
const lang = `{"type":"string","pattern":"^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$"}`

// MediaVideoState is media.video's state: the playing item's tracks, and the group its picture follows while it is a
// follower's play (multiroom P4). memory-only on the server (D11).
const MediaVideoState = `{"type":"object","properties":{` +
	`"sessionId":{"type":"string","maxLength":64},` +
	`"tracks":{"type":"array","maxItems":64,"items":` + track + `},` +
	`"follow":` + videoFollow +
	`},"required":["tracks"],"additionalProperties":false}`

// videoFollow is a follower's play (multiroom P4): the item another screen of the group plays, whose picture follows
// the group's stream with no sound of its own (the group's is), and whose transport goes to the group
const videoFollow = `{"type":"object","properties":{` +
	`"groupId":{"type":"string","pattern":"^[0-9a-f-]{36}$"}` +
	`},"required":["groupId"],"additionalProperties":false}`

const track = `{"type":"object","properties":{` +
	`"id":{"type":"string","minLength":1,"maxLength":64},` +
	`"kind":{"enum":["video","audio","subtitle"]},` +
	`"language":{"type":"string","maxLength":35},` +
	`"label":{"type":"string","maxLength":128},` +
	`"selected":{"type":"boolean"},` +
	`"external":{"type":"boolean"}` +
	`},"required":["id","kind","selected"],"additionalProperties":false}`

// MediaVideoCommands are media.video's commands. urls and headers are never logged.
var MediaVideoCommands = map[string]string{
	"play": `{"type":"object","properties":{` +
		`"url":` + url + `,` +
		`"audioUrl":` + url + `,` +
		`"headers":{"type":"object","maxProperties":16,` +
		`"propertyNames":{"pattern":"^[A-Za-z0-9-]{1,64}$"},"additionalProperties":{"type":"string","maxLength":1024}},` +
		`"subtitles":{"type":"array","maxItems":32,"items":{"type":"object","properties":{` +
		`"url":` + url + `,"language":{"type":"string","maxLength":35},"label":{"type":"string","maxLength":128},` +
		`"format":{"enum":["srt","vtt","ass"]},"default":{"type":"boolean"}` +
		`},"required":["url"],"additionalProperties":false}},` +
		`"startMs":{"type":"integer","minimum":0},` +
		`"live":{"type":"boolean"},` +
		`"metadata":{"type":"object","properties":{` +
		`"title":{"type":"string","maxLength":256},"series":{"type":"string","maxLength":256},` +
		`"artist":{"type":"string","maxLength":256},"channel":{"type":"string","maxLength":256},` +
		`"provider":{"type":"string","pattern":"^[a-z0-9_]{1,32}$"},"providerName":{"type":"string","maxLength":64},` +
		`"providerSource":{"type":"string","maxLength":64},` +
		`"episode":{"type":"string","maxLength":32},"episodeName":{"type":"string","maxLength":128},` +
		`"artworkUrl":` + url + `,` + ref("itemRef") + `,` +
		`"album":{"type":"string","maxLength":256},"kind":{"enum":["video","track"]}` +
		`},"additionalProperties":false}` + `,` +
		`"renditions":{"type":"array","maxItems":16,"items":` + rendition + `},` +
		`"view":` + view + `,` +
		`"views":{"type":"array","maxItems":4,"uniqueItems":true,"items":` + view + `},` +
		`"audioChoices":{"type":"array","maxItems":24,"items":` + audioChoice + `},` +
		`"skips":{"type":"array","maxItems":8,"items":` + skip + `},` +
		`"actions":{"type":"array","maxItems":8,"uniqueItems":true,"items":{"enum":["next","previous"]}},` +
		`"follow":` + videoFollow +
		`},"required":["url"],"additionalProperties":false}`,
	"tracks": noArgs,
	// ends the screen's own play of sessionId (a follower's, whose item the group stopped playing); another session
	// is left alone
	"stop": `{"type":"object","properties":{` +
		`"sessionId":{"type":"string","minLength":1,"maxLength":64}` +
		`},"required":["sessionId"],"additionalProperties":false}`,
	// the server resolves an item it will play here: the screen shows its resolving step at once (the title and art
	// when known), then the play that follows, or the failure
	"prepare": `{"type":"object","properties":{` +
		`"state":{"enum":["resolving","failed"]},` +
		`"title":{"type":"string","maxLength":256},"subtitle":{"type":"string","maxLength":256},` +
		`"artworkUrl":` + url + `,` +
		`"code":{"type":"string","pattern":"^[a-z0-9_]{1,64}$"},` +
		`"stage":{"enum":["album","playlist","artist","cookies","extract","challenge","formats","pick","wait_available","relay"]},` +
		`"elapsedMs":{"type":"integer","minimum":0},"remainingMs":{"type":"integer","minimum":-1},` +
		`"height":{"type":"integer","minimum":1,"maximum":8640},` +
		`"vcodec":{"type":"string","pattern":"^[a-z0-9.-]{1,16}$"},` +
		`"acodec":{"type":"string","pattern":"^[a-zA-Z0-9.-]{1,16}$"},` +
		`"audioTracks":{"type":"integer","minimum":0,"maximum":24},` +
		`"done":{"type":"boolean"},"failed":{"type":"boolean"}` +
		`},"required":["state"],"additionalProperties":false}`,
	"select_track": `{"type":"object","properties":{` +
		`"kind":{"enum":["audio","subtitle","video"]},` +
		`"id":{"type":"string","maxLength":64}` +
		`},"required":["kind","id"],"additionalProperties":false}`,
}

// skip is a span the player offers to skip, or skips with autoSkipOpenings (screen/v1 Skip); actions are the
// transport the server serves for the session (rpc media.control), shown beside the player's own
const skip = `{"type":"object","properties":{` +
	`"kind":{"enum":["opening","ending","recap","preview"]},` +
	`"startMs":{"type":"integer","minimum":0},"endMs":{"type":"integer","minimum":1}` +
	`},"required":["kind","startMs","endMs"],"additionalProperties":false}`

// rendition is one quality the server holds for the item (server/internal/video's naming), listed so the screen can
// offer them; select_track {kind: video, id: name} picks one, which the server answers itself
const rendition = `{"type":"object","properties":{` +
	`"name":{"type":"string","pattern":"^[0-9]{2,4}p([0-9]{1,3})?$"},` +
	`"height":{"type":"integer","minimum":1,"maximum":8640},` +
	`"fps":{"type":"integer","minimum":0,"maximum":480},` +
	`"codecs":{"type":"array","maxItems":2,"items":{"type":"string","pattern":"^[a-z0-9.-]{1,16}$"}},` +
	`"selected":{"type":"boolean"},` +
	`"hdr":{"type":"string","pattern":"^[A-Za-z0-9+.-]{1,16}$"}` +
	`},"required":["name","height"],"additionalProperties":false}`

// view is how the screen shows a server item: the picture, or the audio page with the visualizer off or on
const view = `{"enum":["video","audio","visualizer"]}`

// audioChoice is one audio language the server holds for the item; rpc media.video {audio: id} picks one
const audioChoice = `{"type":"object","properties":{` +
	`"id":{"type":"string","pattern":"^a-[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$"},` +
	`"language":` + lang + `,` +
	`"label":{"type":"string","maxLength":64},` +
	`"original":{"type":"boolean"},"dubbed":{"type":"boolean"},"selected":{"type":"boolean"}` +
	`},"required":["id","language"],"additionalProperties":false}`

const url = `{"type":"string","pattern":"^https?://","maxLength":2048}`
