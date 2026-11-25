package capschema

// MediaMpvID is the screen player's engine (mpv) as the owner tunes it: quality, decoding, network buffering and
// track preferences, distinct from media.video (the sessions it plays).
const MediaMpvID = "media.mpv"

// MediaMpvConfig is media.mpv's config: a fixed set of mpv options the screen maps itself, never an option name or a
// filter string from the server. every key is optional: absent is mpv's own default, so nothing changes until the
// owner sets one. the screen applies a key live where mpv takes a runtime set and at the next file otherwise, and
// reports each key applied, pending (waiting for the next file) or refused in its state.
//
// vo, ao, volume, mute, pause and the gate are ours (rule 7) and deliberately not among the keys.
const MediaMpvConfig = `{"type":"object","properties":{` +
	// a preset the screen expands into its own fixed options (fast drops scaling quality and loop filtering,
	// high-quality raises the scalers; the surface-embed path has no scalers, where it stays with decode alone)
	`"profile":{"enum":["fast","default","high-quality"]},` +
	// empty default is the board's own (auto-safe through the render api, mediacodec on the surface)
	`"hwdec":{"enum":["auto","auto-safe","no","mediacodec","vaapi","vdpau","nvdec","v4l2m2m","v4l2m2m-copy",` +
	`"drm","drm-copy"]},` +
	`"cacheEnabled":{"type":"boolean"},` +
	`"demuxerMaxBytes":{"type":"integer","minimum":1048576,"maximum":2147483647},` +
	`"demuxerMaxBackBytes":{"type":"integer","minimum":1048576,"maximum":2147483647},` +
	`"cacheSecs":{"type":"number","minimum":1,"maximum":600},` +
	`"readaheadS":{"type":"number","minimum":1,"maximum":600},` +
	`"networkTimeoutS":{"type":"number","minimum":1,"maximum":300},` +
	`"streamBufferBytes":{"type":"integer","minimum":4096,"maximum":10485760},` +
	`"userAgent":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9 ._;/():=+-]{0,255}$"},` +
	`"videoSync":{"enum":["audio","display-resample","display-resample-desync","display-vdrop","display-adrop",` +
	`"desync"]},` +
	// interpolation and the tscale/scaler keys need the render api path (linux screens); the surface-embed path
	// (android) accepts them and they change nothing there
	`"interpolation":{"type":"boolean"},` +
	`"tscale":{"enum":["oversample","catmull_rom","mitchell","linear","sphinx"]},` +
	`"scale":` + scaler + `,` +
	`"cscale":` + scaler + `,` +
	`"dscale":` + scaler + `,` +
	// mpv's own deband filter (its parameters stay mpv's)
	`"deband":{"type":"boolean"},` +
	// mpv's sharpen strength
	`"sharpen":{"type":"number","minimum":0,"maximum":20},` +
	// comma-separated language list, mpv's alang/slang; the suggested value is rus,ru,jpn,eng, but absent stays
	// mpv's own track selection
	`"audioLanguages":{"type":"string","pattern":` + langList + `,"minLength":2,"maxLength":128},` +
	`"subtitleLanguages":{"type":"string","pattern":` + langList + `,"minLength":2,"maxLength":128},` +
	`"audioDelayMs":{"type":"integer","minimum":-10000,"maximum":10000}` +
	`},"additionalProperties":false}`

// the scalers mpv is asked for by name (allowlist)
const scaler = `{"enum":["bilinear","bicubic","catmull_rom","mitchell","lanczos","spline16","spline36",` +
	`"ewa_lanczos","ewa_lanczossharp","ewa_robidoux","ewa_robidouxsharp","oversample"]}`

// one to eight comma-separated bcp 47 short tags ("rus,ru,jpn,eng")
const langList = `"^([a-zA-Z]{2,3}(-[A-Za-z0-9]{2,8})?)(,[a-zA-Z]{2,3}(-[A-Za-z0-9]{2,8})?){0,7}$"`

// MediaMpvState is media.mpv's state: which of the config's keys the screen applied, which wait for the next file,
// and which it refused (mpv rejected the value, or the device's own check did). the names are the config's keys.
const MediaMpvState = `{"type":"object","properties":{` +
	`"screen":{"type":"boolean"},` +
	`"applied":` + keyList + `,` +
	`"pending":` + keyList + `,` +
	`"refused":` + keyList +
	`},"required":["screen"],"additionalProperties":false}`

const keyList = `{"type":"array","maxItems":64,"items":{"type":"string","pattern":"^[a-z][a-zA-Z0-9]{0,31}$"}}`
