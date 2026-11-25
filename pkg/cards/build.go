package cards

import (
	"fmt"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// NewCard is an envelope of vocabulary Version around a card root.
func NewCard(id string, revision uint32, fallback string, root *cardsv1.CardBlock) *cardsv1.Card {
	return &cardsv1.Card{Version: Version, Id: id, Revision: revision, FallbackText: fallback, Root: N(root)}
}

// Act is an action by its server-issued id.
func Act(id string) *cardsv1.Action { return &cardsv1.Action{Id: id} }

// Confirm is an action the device asks about first.
func Confirm(id, question string) *cardsv1.Action {
	return &cardsv1.Action{Id: id, Confirm: question}
}

// Nodes wraps primitives, as N does each.
func Nodes(ps ...any) []*cardsv1.Node {
	out := make([]*cardsv1.Node, len(ps))
	for i, p := range ps {
		out[i] = N(p)
	}
	return out
}

// N wraps one primitive message in a Node; a *Node passes through. Anything
// else is a programming error and panics.
func N(p any) *cardsv1.Node {
	switch p := p.(type) {
	case *cardsv1.Node:
		return p
	case *cardsv1.CardBlock:
		return &cardsv1.Node{Kind: &cardsv1.Node_Card{Card: p}}
	case *cardsv1.Row:
		return &cardsv1.Node{Kind: &cardsv1.Node_Row{Row: p}}
	case *cardsv1.Column:
		return &cardsv1.Node{Kind: &cardsv1.Node_Column{Column: p}}
	case *cardsv1.Grid:
		return &cardsv1.Node{Kind: &cardsv1.Node_Grid{Grid: p}}
	case *cardsv1.Text:
		return &cardsv1.Node{Kind: &cardsv1.Node_Text{Text: p}}
	case *cardsv1.LongText:
		return &cardsv1.Node{Kind: &cardsv1.Node_LongText{LongText: p}}
	case *cardsv1.Icon:
		return &cardsv1.Node{Kind: &cardsv1.Node_Icon{Icon: p}}
	case *cardsv1.Image:
		return &cardsv1.Node{Kind: &cardsv1.Node_Image{Image: p}}
	case *cardsv1.State:
		return &cardsv1.Node{Kind: &cardsv1.Node_State{State: p}}
	case *cardsv1.Value:
		return &cardsv1.Node{Kind: &cardsv1.Node_Value{Value: p}}
	case *cardsv1.Meter:
		return &cardsv1.Node{Kind: &cardsv1.Node_Meter{Meter: p}}
	case *cardsv1.Progress:
		return &cardsv1.Node{Kind: &cardsv1.Node_Progress{Progress: p}}
	case *cardsv1.Countdown:
		return &cardsv1.Node{Kind: &cardsv1.Node_Countdown{Countdown: p}}
	case *cardsv1.Steps:
		return &cardsv1.Node{Kind: &cardsv1.Node_Steps{Steps: p}}
	case *cardsv1.Toggle:
		return &cardsv1.Node{Kind: &cardsv1.Node_Toggle{Toggle: p}}
	case *cardsv1.Button:
		return &cardsv1.Node{Kind: &cardsv1.Node_Button{Button: p}}
	case *cardsv1.KeyValue:
		return &cardsv1.Node{Kind: &cardsv1.Node_Keyvalue{Keyvalue: p}}
	case *cardsv1.Badge:
		return &cardsv1.Node{Kind: &cardsv1.Node_Badge{Badge: p}}
	case *cardsv1.MediaTile:
		return &cardsv1.Node{Kind: &cardsv1.Node_MediaTile{MediaTile: p}}
	case *cardsv1.PagedList:
		return &cardsv1.Node{Kind: &cardsv1.Node_PagedList{PagedList: p}}
	case *cardsv1.Divider:
		return &cardsv1.Node{Kind: &cardsv1.Node_Divider{Divider: p}}
	case *cardsv1.Sparkline:
		return &cardsv1.Node{Kind: &cardsv1.Node_Sparkline{Sparkline: p}}
	case *cardsv1.Link:
		return &cardsv1.Node{Kind: &cardsv1.Node_Link{Link: p}}
	case *cardsv1.Post:
		return &cardsv1.Node{Kind: &cardsv1.Node_Post{Post: p}}
	case *cardsv1.Slider:
		return &cardsv1.Node{Kind: &cardsv1.Node_Slider{Slider: p}}
	case *cardsv1.Qr:
		return &cardsv1.Node{Kind: &cardsv1.Node_Qr{Qr: p}}
	case *cardsv1.Code:
		return &cardsv1.Node{Kind: &cardsv1.Node_Code{Code: p}}
	case *cardsv1.Choices:
		return &cardsv1.Node{Kind: &cardsv1.Node_Choices{Choices: p}}
	}
	panic(fmt.Sprintf("cards: %T is not a primitive", p))
}

// NumberValue, IDValue and IDsValue are action values: a slider's, single
// choices' and multiple choices'.
func NumberValue(v float64) *cardsv1.ActionValue {
	return &cardsv1.ActionValue{Kind: &cardsv1.ActionValue_Number{Number: v}}
}

func IDValue(id string) *cardsv1.ActionValue {
	return &cardsv1.ActionValue{Kind: &cardsv1.ActionValue_Id{Id: id}}
}

func IDsValue(ids ...string) *cardsv1.ActionValue {
	return &cardsv1.ActionValue{Kind: &cardsv1.ActionValue_Ids{Ids: &cardsv1.IdList{Ids: ids}}}
}
