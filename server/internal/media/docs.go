package media

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

var docs = map[string]api.Doc{
	"get-device-media":   {Summary: "Get a device media session", Scope: auth.ScopeHA, Reply: MediaView{}},
	"get-device-artwork": {Summary: "Get a device media session's cover art", Scope: auth.ScopeHA},
}
