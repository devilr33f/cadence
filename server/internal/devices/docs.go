package devices

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// SecretValueRequest is the body of a secret's put. The value is sealed on
// arrival and never answered.
type SecretValueRequest struct {
	Value string `json:"value" doc:"the secret; stored sealed, never returned"`
}

// ConfigureRequest is the body of a capability's configure: its config and secrets as one generation. The secrets
// are sealed on arrival and never answered.
type ConfigureRequest struct {
	Config  map[string]any    `json:"config" doc:"the capability config as a JSON object, validated against the capability schema"`
	Secrets map[string]string `json:"secrets,omitempty" doc:"secrets the capability declares, by name, each 1 to 4096 bytes; stored sealed, never returned. Secrets not named stay as they are"`
}

var docs = map[string]api.Doc{
	"get-device":      {Summary: "Get a device with its capabilities and their descriptors", Scope: auth.ScopeHA},
	"rename-device":   {Summary: "Rename a device; answers the device as get-device does"},
	"set-device-room": {Summary: "Put a device in a room; answers the device as get-device does"},
	"delete-device":   {Summary: "Delete a device with its token and data, closing its link; its telemetry ages out"},
	"put-device-config": {
		Summary: "Replace a capability config", Body: map[string]any{}, BodyRequired: true,
		BodyNote: "the capability config as a JSON object, validated against the capability schema",
	},
	"put-device-secret": {
		Summary: "Set a capability secret", Body: SecretValueRequest{}, BodyRequired: true,
	},
	"delete-device-secret": {Summary: "Remove a capability secret"},
	"configure-device-capability": {
		Summary: "Replace a capability config and set its secrets as one generation, pushed together",
		Body:    ConfigureRequest{}, BodyRequired: true,
		BodyNote: "the same config and secrets again still add a generation: the device takes it as a new attempt",
	},
}
