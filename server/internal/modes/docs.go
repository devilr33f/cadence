package modes

import "git.shino.boo/devilreef/cadence/server/internal/api"

var docs = map[string]api.Doc{
	"list-modes":  {Summary: "List the agent modes by name"},
	"get-mode":    {Summary: "Get an agent mode"},
	"put-mode":    {Summary: "Create or replace an agent mode; answers it as get-mode does"},
	"delete-mode": {Summary: "Delete an agent mode that no setting names"},
}
