package cards

import (
	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// genericCard is a tool no builder knows (an MCP server's): its name and how
// it ended. Its arguments and result are a third party's and may carry what
// the owner would not show, so neither is drawn.
func genericCard(r ToolResult) Subject {
	name := clip(r.Tool, 80)
	if name == "" {
		name = "tool"
	}
	ok := r.OK
	return &toolCard{id: OpaqueID("tool", r.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
		state, kind, tone, icon := rr.T("tool.done"), cardsv1.StateKind_STATE_KIND_ON, cardsv1.Tone_TONE_NEUTRAL, "sparkle"
		if !ok {
			state, kind, tone, icon = rr.T("tool.failed"), cardsv1.StateKind_STATE_KIND_UNAVAILABLE, cardsv1.Tone_TONE_DANGER, "warning"
		}
		b := &cardsv1.CardBlock{Title: name, Subtitle: rr.T("tool.subtitle"), Icon: icon, Tone: tone,
			Children: pcards.Nodes(&cardsv1.State{Label: clip(state, 40), Kind: kind})}
		return b, name + ": " + state
	}}
}
