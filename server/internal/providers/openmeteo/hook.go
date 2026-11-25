package openmeteo

// decodeHook runs on every upstream body; tests set it to inject a panic
var decodeHook func([]byte)
