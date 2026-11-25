package telemetry

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// tag is the document's tag of the telemetry operations
const tag = "telemetry"

// docs are admin's but the vitals: log bodies are third-party text and
// metrics name the device's processes, while vitals name none (M8-D3)
var docs = map[string]api.Doc{
	"list-logs":            {Summary: "List stored log lines"},
	"list-device-metrics":  {Summary: "List a device metric series"},
	"get-device-processes": {Summary: "Get a device top processes"},
	"get-device-vitals":    {Summary: "Get a device latest vitals: cpu, memory, disks and temperature", Scope: auth.ScopeHA},
}
