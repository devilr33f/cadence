package deepgram

// decodeHook runs on every upstream frame; tests set it to inject a panic
var decodeHook func([]byte)
