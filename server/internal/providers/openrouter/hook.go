package openrouter

// decodeHook runs on every upstream data line; tests set it to inject a panic
var decodeHook func([]byte)
