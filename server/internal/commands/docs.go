package commands

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// CommandReply is a command answer: the device result as it came.
type CommandReply struct {
	Result any `json:"result" doc:"the device result for the command"`
}

var docs = map[string]api.Doc{
	"send-device-command": {
		Summary: "Send a capability command to a device", Body: map[string]any{},
		BodyNote: "the command args as a JSON object; an empty body sends none",
		Reply:    CommandReply{},
		Scope:    auth.ScopeHA,
	},
}
