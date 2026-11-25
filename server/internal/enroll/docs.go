package enroll

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

var docs = map[string]api.Doc{
	"create-device": {Summary: "Create a device and its one-time enrol code"},
	"list-devices":  {Summary: "List devices", Scope: auth.ScopeHA},
	"list-boards":   {Summary: "List the boards a device can be"},
}
