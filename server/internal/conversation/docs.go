package conversation

import "git.shino.boo/devilreef/cadence/server/internal/api"

var docs = map[string]api.Doc{
	"list-device-turns":         {Summary: "List a device's conversation turns, newest first, a page at a time"},
	"get-device-turn":           {Summary: "Get a device's conversation turn with its messages as stored"},
	"reset-device-conversation": {Summary: "Start the device's next turn with no earlier turns or summary; keep the stored history and memories"},
	"compact-device-conversation": {
		Summary: "Compact the device's current conversation segment now, including its newest committed turns, and run memory learning",
	},
}
