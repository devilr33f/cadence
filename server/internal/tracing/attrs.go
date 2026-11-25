package tracing

// Attribute keys. The HTTP ones are OpenTelemetry's stable semantic
// conventions, spelled out rather than pulled from a semconv module; the
// cadence ones are this package's contract with the instrumentation and the
// dashboard.
const (
	AttrHTTPMethod       = "http.request.method"
	AttrHTTPRoute        = "http.route"
	AttrHTTPStatus       = "http.response.status_code"
	AttrHTTPRequestSize  = "http.request.body.size"
	AttrHTTPResponseSize = "http.response.body.size"
	AttrServerAddress    = "server.address"
	AttrServerPort       = "server.port"
	AttrErrorType        = "error.type"

	// AttrDeviceID names the device a server span works for; the list's
	// device filter reads it as it reads a device span's resource.
	AttrDeviceID = "device.id"
	// AttrSessionID and AttrTurnID are a voice session's and turn's ids.
	AttrSessionID = "cadence.session_id"
	AttrTurnID    = "cadence.turn_id"
	// AttrProvider is a provider instance's name, AttrModule its module
	// (deepgram-flux, openrouter…).
	AttrProvider = "cadence.provider"
	AttrModule   = "cadence.module"
	// AttrMethod is an rpc or capability method's name (pkg/rpc).
	AttrMethod = "cadence.method"
)
