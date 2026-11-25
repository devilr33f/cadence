package ha

import "git.shino.boo/devilreef/cadence/server/internal/api"

var docs = map[string]api.Doc{
	"get-homeassistant": {Summary: "Get the Home Assistant connection status"},
	"list-homeassistant-entities": {Summary: "List Home Assistant entities", Query: []api.Query{
		{Name: "q", Note: "matches a name, id or alias", Schema: api.Text(maxQuery)},
		{Name: "domain", Schema: api.Text(maxDomain)},
		{Name: "area", Note: "an area id", Schema: api.Text(64)},
		{Name: "limit", Note: "default 100", Schema: api.Int(1, maxEntityPage)},
	}},
	"list-homeassistant-areas": {Summary: "List Home Assistant areas"},
	"list-homeassistant-usage": {Summary: "List the entities used most", Query: []api.Query{
		{Name: "device", Note: "one device usage; absent: the household", Schema: api.UUID()},
		{Name: "limit", Note: "default 50", Schema: api.Int(1, maxUsagePage)},
	}},
}
