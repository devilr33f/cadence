package voice

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// AnnounceRequest is what to speak on a device.
type AnnounceRequest struct {
	Text     string `json:"text" minLength:"1" maxLength:"500" doc:"1 to 500 characters once trimmed"`
	Language string `json:"language,omitempty" enum:"en,ru"`
}

var docs = map[string]api.Doc{
	"announce-to-device": {Summary: "Speak an announcement on a device", Body: AnnounceRequest{}, BodyRequired: true, Scope: auth.ScopeHA},
}
