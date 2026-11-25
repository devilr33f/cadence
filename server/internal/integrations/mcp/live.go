package mcp

import (
	"fmt"

	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// LiveConnected is events.MCPConnected's data.
type LiveConnected struct {
	Instance string `json:"instance"`
	Era      string `json:"era"`
	Tools    int    `json:"tools"`
}

// LiveUnavailable is events.MCPUnavailable's data.
type LiveUnavailable struct {
	Instance string `json:"instance"`
	Code     string `json:"code"`
}

// LiveTools is events.MCPToolsChanged's data: counts only.
type LiveTools struct {
	Instance string `json:"instance"`
	Total    int    `json:"total"`
	Offered  int    `json:"offered"`
	Changed  int    `json:"changed"`
	Skipped  int    `json:"skipped"`
}

// codeRemoved is the unavailable code of a server Apply left out
const codeRemoved = "removed"

// noCounts is the counts a server starts from, never published
var noCounts = countsKey(LiveTools{})

func countsKey(n LiveTools) string { return fmt.Sprint(n.Total, n.Offered, n.Changed, n.Skipped) }

func (c *Catalog) publish(typ string, data any) {
	if c.bus != nil {
		c.bus.Publish(events.Event{Type: typ, At: c.clock.Now(), Data: data})
	}
}

// up publishes that s listed tools on an era's session, unless that is what
// it said last; held under c.mu.
func (c *Catalog) up(s *server, era string, tools int) {
	if health := StateConnected + "/" + era; s.health != health {
		s.health = health
		c.publish(events.MCPConnected, LiveConnected{Instance: s.cfg.Instance, Era: era, Tools: tools})
	}
}

// down publishes that s failed with code, unless that is what it said last;
// held under c.mu.
func (c *Catalog) down(s *server, code string) {
	if health := StateUnavailable + "/" + code; s.health != health {
		s.health = health
		c.publish(events.MCPUnavailable, LiveUnavailable{Instance: s.cfg.Instance, Code: code})
	}
}

// counted publishes the counts of s once they differ from the last
// published; held under c.mu.
func (c *Catalog) counted(s *server, n LiveTools) {
	if key := countsKey(n); s.fed != key {
		s.fed = key
		c.publish(events.MCPToolsChanged, n)
	}
}
