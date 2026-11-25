package memory

import (
	"strconv"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// CreateMemoryRequest adds a memory by hand.
type CreateMemoryRequest struct {
	Scope    string `json:"scope" enum:"global,device"`
	DeviceID string `json:"deviceId,omitempty" format:"uuid" doc:"required for the device scope"`
	Kind     string `json:"kind" enum:"preference,fact,alias,lesson"`
	Text     string `json:"text" minLength:"1" maxLength:"500"`
}

// EditMemoryRequest changes the fields it names.
type EditMemoryRequest struct {
	Text     *string `json:"text,omitempty" minLength:"1" maxLength:"500"`
	Kind     *string `json:"kind,omitempty" enum:"preference,fact,alias,lesson"`
	Scope    *string `json:"scope,omitempty" enum:"global,device"`
	DeviceID *string `json:"deviceId,omitempty" format:"uuid"`
	Pinned   *bool   `json:"pinned,omitempty"`
}

var logLimit = api.Query{Name: "limit", Note: "default " + strconv.Itoa(defaultLog) + ", at most " + strconv.Itoa(maxPage), Schema: api.MinInt(1)}

var docs = map[string]api.Doc{
	"list-memories": {Summary: "List memories", Query: []api.Query{
		{Name: "scope", Schema: api.Enum(ScopeGlobal, ScopeDevice)},
		{Name: "deviceId", Schema: api.UUID()},
		{Name: "kind", Schema: api.Enum(KindPreference, KindFact, KindAlias, KindLesson)},
		{Name: "status", Schema: api.Enum(StatusActive, StatusPending)},
		{Name: "pinned", Schema: api.Enum("true", "false")},
		{Name: "limit", Note: "default " + strconv.Itoa(defaultPage) + ", at most " + strconv.Itoa(maxPage), Schema: api.MinInt(1)},
		{Name: "offset", Schema: api.MinInt(0)},
	}},
	"create-memory":           {Summary: "Add a memory", Body: CreateMemoryRequest{}, BodyRequired: true},
	"edit-memory":             {Summary: "Edit a memory", Body: EditMemoryRequest{}, BodyRequired: true},
	"delete-memory":           {Summary: "Delete a memory"},
	"approve-memory":          {Summary: "Approve a pending memory; 204 when there was nothing to approve"},
	"reject-memory":           {Summary: "Reject a pending memory"},
	"list-device-compactions": {Summary: "List conversation compactions of a device", Query: []api.Query{logLimit}},
	"list-device-learning":    {Summary: "List learning runs of a device", Query: []api.Query{logLimit}},
}
