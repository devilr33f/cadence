package providers

import "git.shino.boo/devilreef/cadence/server/internal/api"

// CreateProviderRequest creates a provider instance of a module.
type CreateProviderRequest struct {
	Name    string            `json:"name" pattern:"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$" doc:"not modules, in any case: the module catalog's path"`
	Module  string            `json:"module" doc:"the module the instance runs"`
	Config  map[string]any    `json:"config,omitempty" doc:"validated against the module config"`
	Secrets map[string]string `json:"secrets,omitempty" doc:"sealed on arrival; an instance answers {set: true} for each, never the value"`
}

// UpdateProviderRequest edits an instance without changing its name, module or identity.
type UpdateProviderRequest struct {
	Config       map[string]any    `json:"config,omitempty" doc:"when given, replaces the whole config and is validated against the module config"`
	Secrets      map[string]string `json:"secrets,omitempty" doc:"each given value replaces that secret; keys left out keep their stored value"`
	ClearSecrets []string          `json:"clearSecrets,omitempty" doc:"optional secrets to remove; clearing a required secret is refused"`
}

var docs = map[string]api.Doc{
	"create-provider":       {Summary: "Create a provider instance", Body: CreateProviderRequest{}, BodyRequired: true},
	"update-provider":       {Summary: "Update a provider instance's config and secrets", Body: UpdateProviderRequest{}, BodyRequired: true},
	"list-providers":        {Summary: "List provider instances"},
	"delete-provider":       {Summary: "Delete a provider instance that no setting names"},
	"list-provider-modules": {Summary: "List the modules a provider instance can run"},
	"test-provider":         {Summary: "Test a provider instance with its module's cheapest free call"},
}
