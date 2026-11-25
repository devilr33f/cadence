package groups

import (
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

var docs = map[string]api.Doc{
	"list-groups": {Summary: "List the multiroom groups with their elected leaders", Scope: auth.ScopeHA},
	"get-group": {Summary: "Get a multiroom group with each member's live media.group status",
		Scope: auth.ScopeHA},
	"create-group": {Summary: "Create a multiroom group or stereo pair; its key is made here and pushed to its members"},
	"update-group": {Summary: "Replace a multiroom group; a changed membership takes a new key"},
	"delete-group": {Summary: "Delete a multiroom group; its members play alone again"},
	"calibrate-group": {Summary: "Calibrate a multiroom group acoustically: each member's trim from its measured output " +
		"latency (the owner's: every member plays a short chirp), measured by the speakers or scheduled for the owner's " +
		"browser"},
	"apply-group-calibration": {Summary: "Apply what the owner's browser heard of a listener calibration: each member's " +
		"trim from its chirp's arrival"},
	"join-device-group": {Summary: "Make the device lead a group with the listed devices (Home Assistant's join): its own " +
		"group extended, or a new one named after it; each listed device leaves the group it was in, and a group left " +
		"with one member, or a stereo pair that lost one, is deleted", Scope: auth.ScopeHA},
	"leave-device-group": {Summary: "Take the device out of its group; a group left with one member, or a stereo pair, " +
		"is deleted. A device in no group answers 204 too", Scope: auth.ScopeHA},
}
