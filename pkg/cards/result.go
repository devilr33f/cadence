package cards

import (
	"errors"

	"google.golang.org/protobuf/reflect/protoreflect"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// CardResult answers a card action with the card's next revision.
func CardResult(c *cardsv1.Card) *cardsv1.CardActionResult {
	return &cardsv1.CardActionResult{Result: &cardsv1.CardActionResult_Card{Card: c}}
}

// PageResult answers an action with the page it opened or loaded.
func PageResult(p *cardsv1.Page) *cardsv1.CardActionResult {
	return &cardsv1.CardActionResult{Result: &cardsv1.CardActionResult_Page{Page: p}}
}

// RefusalResult answers a refused action: its reason token, the toast in the
// screen's language and the card to roll back to (nil for none).
func RefusalResult(reason, toast string, c *cardsv1.Card) *cardsv1.CardActionResult {
	return &cardsv1.CardActionResult{Result: &cardsv1.CardActionResult_Refusal{Refusal: &cardsv1.ActionRefusal{
		Reason: reason, Toast: toast, Card: c,
	}}}
}

// PrepareActionResult sets the features of every card r holds, as EncodeCard
// does, then checks it as ValidateActionResult does.
func PrepareActionResult(r *cardsv1.CardActionResult) error {
	switch k := r.GetResult().(type) {
	case *cardsv1.CardActionResult_Card:
		k.Card.Features = Features(k.Card)
	case *cardsv1.CardActionResult_Page:
		for _, c := range k.Page.GetCards() {
			c.Features = Features(c)
		}
	case *cardsv1.CardActionResult_Refusal:
		if c := k.Refusal.GetCard(); c != nil {
			c.Features = Features(c)
		}
	}
	return ValidateActionResult(r)
}

// ValidateActionResult checks a card action's answer: exactly one result, a
// card or a page as ValidateCard and ValidatePage do, a refusal's reason token
// and toast, and the card it rolls back to.
func ValidateActionResult(r *cardsv1.CardActionResult) error {
	if len(r.ProtoReflect().GetUnknown()) > 0 {
		return refuse(CodeUnknownField, "", "fields this vocabulary does not know")
	}
	switch k := r.GetResult().(type) {
	case *cardsv1.CardActionResult_Card:
		return validateCard(k.Card, "/card")
	case *cardsv1.CardActionResult_Page:
		if err := ValidatePage(k.Page); err != nil {
			var e *Error
			if errors.As(err, &e) {
				e.Path = "/page" + e.Path
			}
			return err
		}
		return nil
	case *cardsv1.CardActionResult_Refusal:
		f := k.Refusal
		m := f.ProtoReflect()
		if len(m.GetUnknown()) > 0 {
			return refuse(CodeUnknownField, "/refusal", "fields this vocabulary does not know")
		}
		for _, name := range []string{"reason", "toast"} {
			fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
			r := rules[fd.FullName()]
			if r.required && !m.Has(fd) {
				return refuse(CodeRequired, "/refusal/"+name, "missing")
			}
			if err := checkValue(fd, r, m.Get(fd), "/refusal/"+name); err != nil {
				return err
			}
		}
		if c := f.GetCard(); c != nil {
			return validateCard(c, "/refusal/card")
		}
		return nil
	}
	return refuse(CodeRequired, "/result", "missing")
}

// Known lists every card feature this package writes: the primitives and the
// optional fields, which a screen that draws them all advertises in its hello.
func Known() []string {
	return append(Primitives(), FieldFeatures...)
}

// Missing answers the features c uses that have (a screen's hello) lacks; a
// screen that sent none is taken to draw every feature of its versions.
func Missing(c *cardsv1.Card, have []string) []string {
	if len(have) == 0 {
		return nil
	}
	known := make(map[string]bool, len(have))
	for _, f := range have {
		known[f] = true
	}
	var out []string
	for _, f := range Features(c) {
		if !known[f] {
			out = append(out, f)
		}
	}
	return out
}
