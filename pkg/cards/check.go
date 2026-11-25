package cards

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// ErrorCode says why a card was refused. Codes and paths are safe for logs;
// an Error's Reason is not.
type ErrorCode string

const (
	CodeDecode       ErrorCode = "decode"
	CodeSize         ErrorCode = "size"
	CodeVersion      ErrorCode = "version"
	CodeUnknownField ErrorCode = "unknown_field"
	CodeEnum         ErrorCode = "enum"
	CodeRequired     ErrorCode = "required"
	CodeLength       ErrorCode = "length"
	CodePattern      ErrorCode = "pattern"
	// CodeEmptyNode is a node with no primitive set: one from a newer
	// vocabulary, decoded by this one
	CodeEmptyNode ErrorCode = "empty_node"
	// CodeRoot is a root that is not a card, or a card below the root.
	CodeRoot ErrorCode = "root"
	// CodeListItem is a paged list item that is not a post, tile or link.
	CodeListItem   ErrorCode = "list_item"
	CodeQuote      ErrorCode = "quote"
	CodeDepth      ErrorCode = "depth"
	CodeNodes      ErrorCode = "nodes"
	CodeFocusables ErrorCode = "focusables"
	CodeFeatures   ErrorCode = "features"
	// CodePrimary is more than one primary button in a card.
	CodePrimary ErrorCode = "primary"
	// CodeTone is the accent tone on a node: it is the primary button's alone.
	CodeTone ErrorCode = "tone"
	// CodeRange is a number outside its bounds, or bounds out of order.
	CodeRange ErrorCode = "range"
	// CodeChoices is duplicate option ids or min and max that cannot hold.
	CodeChoices ErrorCode = "choices"
	// CodeDuplicate is an action id, a citation number or a page's card id
	// used twice.
	CodeDuplicate ErrorCode = "duplicate"
	// CodeStale, CodeUnknownAction and CodeValue refuse an action request:
	// another card or revision, an action the card does not hold, or a value
	// its primitive does not take.
	CodeStale         ErrorCode = "stale"
	CodeUnknownAction ErrorCode = "unknown_action"
	CodeValue         ErrorCode = "value"
)

// Error is a refusal. Code and Path (a JSON pointer by protojson names) name
// it without quoting the card; Reason words it in English and may quote the
// card's own text, so it goes only to whoever built the card, never to a log.
// Error() leaves Reason out.
type Error struct {
	Code   ErrorCode
	Path   string
	Reason string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "cards: " + string(e.Code)
	}
	return "cards: " + string(e.Code) + " at " + e.Path
}

func refuse(code ErrorCode, path, format string, args ...any) *Error {
	return &Error{Code: code, Path: path, Reason: fmt.Sprintf(format, args...)}
}

var deterministic = proto.MarshalOptions{Deterministic: true}

// EncodeCard is the server's way out: it sets c's features, checks c as the
// device will, and answers its deterministic encoding.
func EncodeCard(c *cardsv1.Card) ([]byte, error) {
	c.Features = Features(c)
	if err := ValidateCard(c); err != nil {
		return nil, err
	}
	return deterministic.Marshal(c)
}

// EncodePage is EncodeCard for a panel page and each of its cards.
func EncodePage(p *cardsv1.Page) ([]byte, error) {
	for _, c := range p.GetCards() {
		c.Features = Features(c)
	}
	if err := ValidatePage(p); err != nil {
		return nil, err
	}
	return deterministic.Marshal(p)
}

// DecodeCard decodes and checks one card.
func DecodeCard(raw []byte) (*cardsv1.Card, error) {
	if len(raw) > MaxCardBytes {
		return nil, refuse(CodeSize, "", "card is %d bytes, over %d", len(raw), MaxCardBytes)
	}
	c := &cardsv1.Card{}
	if err := proto.Unmarshal(raw, c); err != nil {
		return nil, refuse(CodeDecode, "", "does not decode")
	}
	return c, ValidateCard(c)
}

// DecodePage decodes and checks a panel page.
func DecodePage(raw []byte) (*cardsv1.Page, error) {
	if len(raw) > MaxPageBytes {
		return nil, refuse(CodeSize, "", "page is %d bytes, over %d", len(raw), MaxPageBytes)
	}
	p := &cardsv1.Page{}
	if err := proto.Unmarshal(raw, p); err != nil {
		return nil, refuse(CodeDecode, "", "does not decode")
	}
	return p, ValidatePage(p)
}

// ValidateCard checks a card against the vocabulary and the device limits.
func ValidateCard(c *cardsv1.Card) error { return validateCard(c, "") }

// validateCard answers error, never a nil *Error in one
func validateCard(c *cardsv1.Card, at string) error {
	if n := proto.Size(c); n > MaxCardBytes {
		return refuse(CodeSize, at, "card is %d bytes, over %d", n, MaxCardBytes)
	}
	if c.GetVersion() != Version {
		return refuse(CodeVersion, at+"/version", "version %d, want %d", c.GetVersion(), Version)
	}
	if c.GetTtlS() > MaxTTLSeconds {
		return refuse(CodeRange, at+"/ttlS", "ttl %d s, over %d", c.GetTtlS(), MaxTTLSeconds)
	}
	if err := checkFields(c.ProtoReflect(), at); err != nil {
		return err
	}
	root := c.GetRoot()
	if _, ok := root.GetKind().(*cardsv1.Node_Card); !ok {
		return refuse(CodeRoot, at+"/root", "the root is %q, not a card", kindOf(root))
	}
	t := &tally{actions: map[string]bool{}, cites: map[uint32]bool{}}
	if err := t.walk(root, 0, at+"/root"); err != nil {
		return err
	}
	switch {
	case t.nodes > MaxNodes:
		return refuse(CodeNodes, at+"/root", "%d nodes, over %d", t.nodes, MaxNodes)
	case t.focusables > MaxFocusables:
		return refuse(CodeFocusables, at+"/root", "%d focusable elements, over %d", t.focusables, MaxFocusables)
	case t.primaries > 1:
		return refuse(CodePrimary, at+"/root", "%d primary buttons, at most one", t.primaries)
	}
	if want := Features(c); !slices.Equal(c.GetFeatures(), want) {
		return refuse(CodeFeatures, at+"/features", "features %v, the card uses %v", c.GetFeatures(), want)
	}
	return nil
}

// ValidatePage checks a panel page: its size, each card as ValidateCard does,
// its next action, and that no card id repeats.
func ValidatePage(p *cardsv1.Page) error {
	if n := proto.Size(p); n > MaxPageBytes {
		return refuse(CodeSize, "", "page is %d bytes, over %d", n, MaxPageBytes)
	}
	if p.GetVersion() != Version {
		return refuse(CodeVersion, "/version", "version %d, want %d", p.GetVersion(), Version)
	}
	if len(p.GetCards()) > MaxPageCards {
		return refuse(CodeLength, "/cards", "%d cards, over %d", len(p.GetCards()), MaxPageCards)
	}
	if len(p.ProtoReflect().GetUnknown()) > 0 {
		return refuse(CodeUnknownField, "", "fields this vocabulary does not know")
	}
	if next := p.GetNext(); next != nil {
		if err := checkFields(next.ProtoReflect(), "/next"); err != nil {
			return err
		}
	}
	idField := p.ProtoReflect().Descriptor().Fields().ByName("id")
	if err := checkValue(idField, rules[idField.FullName()], p.ProtoReflect().Get(idField), "/id"); err != nil {
		return err
	}
	ids := map[string]bool{}
	for i, c := range p.GetCards() {
		at := "/cards/" + strconv.Itoa(i)
		if err := validateCard(c, at); err != nil {
			return err
		}
		if ids[c.GetId()] {
			return refuse(CodeDuplicate, at+"/id", "card id %q twice in one page", c.GetId())
		}
		ids[c.GetId()] = true
	}
	return nil
}

type tally struct {
	nodes, focusables, primaries int
	actions                      map[string]bool
	cites                        map[uint32]bool
}

func (t *tally) action(a *cardsv1.Action, at string) *Error {
	if a == nil {
		return nil
	}
	if t.actions[a.GetId()] {
		return refuse(CodeDuplicate, at+"/id", "action %q twice in one card", a.GetId())
	}
	t.actions[a.GetId()] = true
	return nil
}

func accent(tone cardsv1.Tone, at string) *Error {
	if tone == cardsv1.Tone_TONE_ACCENT {
		return refuse(CodeTone, at+"/tone", "accent is the primary button's alone")
	}
	return nil
}

func finite(v float64, at string) *Error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return refuse(CodeRange, at, "not a finite number")
	}
	return nil
}

// walk counts n and what is below it, at depth below the card (the root is
// 0), and checks what a field rule cannot say.
func (t *tally) walk(n *cardsv1.Node, depth int, at string) *Error {
	t.nodes++
	kind := kindOf(n)
	switch {
	case kind == "":
		return refuse(CodeEmptyNode, at, "a node with no primitive this vocabulary knows")
	case depth > MaxDepth:
		return refuse(CodeDepth, at, "depth %d below the card, over %d", depth, MaxDepth)
	case t.nodes > MaxNodes:
		return refuse(CodeNodes, at, "over %d nodes", MaxNodes)
	case kind == "card" && depth > 0:
		return refuse(CodeRoot, at, "a card below the root")
	}
	in := at + "/" + nodeKinds.Fields().ByName(protoreflect.Name(kind)).JSONName()
	if err := t.check(n, depth, in); err != nil {
		return err
	}
	// a card's actions are walked as button nodes after its children
	key := "children"
	if _, ok := n.GetKind().(*cardsv1.Node_PagedList); ok {
		key = "items"
	}

	kids := children(n)
	nChildren := len(kids)
	if c, ok := n.GetKind().(*cardsv1.Node_Card); ok {
		nChildren = len(c.Card.GetChildren())
	}
	_, list := n.GetKind().(*cardsv1.Node_PagedList)
	before := t.focusables
	for i, c := range kids {
		path := in + "/" + key + "/" + strconv.Itoa(i)
		if i >= nChildren {
			path = in + "/actions/" + strconv.Itoa(i-nChildren)
		}
		if err := t.walk(c, depth+1, path); err != nil {
			return err
		}
	}
	if list {
		t.focusables = before
	}
	return nil
}

func (t *tally) check(n *cardsv1.Node, depth int, at string) *Error {
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Card:
		return accent(k.Card.GetTone(), at)
	case *cardsv1.Node_Grid:
		if c := k.Grid.GetColumns(); c < 2 || c > 4 {
			return refuse(CodeRange, at+"/columns", "%d columns, want 2 to 4", c)
		}
	case *cardsv1.Node_Text:
		return accent(k.Text.GetTone(), at)
	case *cardsv1.Node_State:
		return accent(k.State.GetTone(), at)
	case *cardsv1.Node_Icon:
		if s := k.Icon.GetSize(); s != 0 && s != 32 && s != 48 {
			return refuse(CodeRange, at+"/size", "size %d, want 32 or 48", s)
		}
		return accent(k.Icon.GetTone(), at)
	case *cardsv1.Node_LongText:
		t.focusables++
		if l := k.LongText.GetCollapsedLines(); l != 0 && (l < 3 || l > 10) {
			return refuse(CodeRange, at+"/collapsedLines", "%d lines, want 3 to 10", l)
		}
	case *cardsv1.Node_Code:
		t.focusables++
	case *cardsv1.Node_Meter:
		return ordered(k.Meter.GetMin(), k.Meter.GetValue(), k.Meter.GetMax(), at)
	case *cardsv1.Node_Progress:
		switch a := k.Progress.GetAmount().(type) {
		case nil:
			return refuse(CodeRequired, at+"/fraction", "neither a fraction nor indeterminate")
		case *cardsv1.Progress_Fraction:
			if err := finite(a.Fraction, at+"/fraction"); err != nil {
				return err
			}
			if a.Fraction < 0 || a.Fraction > 1 {
				return refuse(CodeRange, at+"/fraction", "fraction %v, want 0 to 1", a.Fraction)
			}
		case *cardsv1.Progress_Indeterminate:
			if !a.Indeterminate {
				return refuse(CodeRange, at+"/indeterminate", "indeterminate set false")
			}
		}
	case *cardsv1.Node_Countdown:
		if k.Countdown.GetEndsAtMs() <= 0 {
			return refuse(CodeRange, at+"/endsAtMs", "no end time")
		}
	case *cardsv1.Node_Sparkline:
		for i, p := range k.Sparkline.GetPoints() {
			if err := finite(p, at+"/points/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
	case *cardsv1.Node_Toggle:
		if k.Toggle.GetAction() != nil {
			t.focusables++
		}
		return t.action(k.Toggle.GetAction(), at+"/action")
	case *cardsv1.Node_Button:
		t.focusables++
		if k.Button.GetPrimary() {
			t.primaries++
		}
		return t.action(k.Button.GetAction(), at+"/action")
	case *cardsv1.Node_MediaTile:
		t.focusables++
		if k.MediaTile.GetImage().GetAspect() == cardsv1.Aspect_ASPECT_UNSPECIFIED {
			return refuse(CodeRequired, at+"/image/aspect", "a tile's image needs its aspect")
		}
		if err := t.action(k.MediaTile.GetAction(), at+"/action"); err != nil {
			return err
		}
		for i, s := range k.MediaTile.GetSecondary() {
			if err := t.action(s.GetAction(), at+"/secondary/"+strconv.Itoa(i)+"/action"); err != nil {
				return err
			}
		}
	case *cardsv1.Node_PagedList:
		// one focus region that scrolls its items, plus its more row; the
		// items' own focusables are the region's
		t.focusables++
		if m := k.PagedList.GetMore(); m != nil {
			t.focusables++
			if err := t.action(m.GetAction(), at+"/more/action"); err != nil {
				return err
			}
		}
		for i, it := range k.PagedList.GetItems() {
			switch it.GetKind().(type) {
			case *cardsv1.Node_Post, *cardsv1.Node_MediaTile, *cardsv1.Node_Link:
			default:
				return refuse(CodeListItem, at+"/items/"+strconv.Itoa(i), "a %q in a paged list", kindOf(it))
			}
		}
	case *cardsv1.Node_Link:
		t.focusables++
		if c := k.Link.GetCite(); c != 0 {
			if c > 99 {
				return refuse(CodeRange, at+"/cite", "citation %d, want 1 to 99", c)
			}
			if t.cites[c] {
				return refuse(CodeDuplicate, at+"/cite", "citation %d twice in one card", c)
			}
			t.cites[c] = true
		}
		return t.action(k.Link.GetAction(), at+"/action")
	case *cardsv1.Node_Post:
		return t.post(k.Post, depth, at)
	case *cardsv1.Node_Slider:
		s := k.Slider
		if err := checkTimeline(s, at); err != nil {
			return err
		}
		if s.GetAction() != nil {
			t.focusables++
		}
		for _, v := range []float64{s.GetMin(), s.GetValue(), s.GetMax(), s.GetStep()} {
			if err := finite(v, at); err != nil {
				return err
			}
		}
		if err := ordered(s.GetMin(), s.GetValue(), s.GetMax(), at); err != nil {
			return err
		}
		if s.GetStep() <= 0 || s.GetStep() > s.GetMax()-s.GetMin() {
			return refuse(CodeRange, at+"/step", "step %v does not fit the range", s.GetStep())
		}
		return t.action(s.GetAction(), at+"/action")
	case *cardsv1.Node_Choices:
		c := k.Choices
		t.focusables += len(c.GetOptions())
		if c.GetMultiple() {
			t.focusables++
		}
		if err := checkChoices(c, at); err != nil {
			return err
		}
		return t.action(c.GetAction(), at+"/action")
	}
	return nil
}

// post counts a post's focus and actions, and its quote one level below it,
// which holds no quote of its own.
func (t *tally) post(p *cardsv1.Post, depth int, at string) *Error {
	if p.GetAction() != nil {
		t.focusables++
	}
	if err := t.action(p.GetAction(), at+"/action"); err != nil {
		return err
	}
	q := p.GetQuote()
	if q == nil {
		return nil
	}
	if q.GetQuote() != nil {
		return refuse(CodeQuote, at+"/quote/quote", "a quoted post holds no quote")
	}
	t.nodes++
	if depth+1 > MaxDepth {
		return refuse(CodeDepth, at+"/quote", "depth %d below the card, over %d", depth+1, MaxDepth)
	}
	if q.GetAction() != nil {
		t.focusables++
	}
	return t.action(q.GetAction(), at+"/quote/action")
}

// the most seconds of value per second a timeline runs at (a player's fastest forward)
const maxTimelineRate = 4

// checkTimeline holds a slider without a timeline to its action, and a timeline to a running position: an anchor,
// a rate of 0 to maxTimelineRate and a max above 0
func checkTimeline(s *cardsv1.Slider, at string) *Error {
	tl := s.GetTimeline()
	if tl == nil {
		if s.GetAction() == nil {
			return refuse(CodeRequired, at+"/action", "missing")
		}
		return nil
	}
	if tl.GetAnchorAtMs() <= 0 {
		return refuse(CodeRange, at+"/timeline/anchorAtMs", "no anchor time")
	}
	if err := finite(tl.GetRate(), at+"/timeline/rate"); err != nil {
		return err
	}
	if r := tl.GetRate(); r < 0 || r > maxTimelineRate {
		return refuse(CodeRange, at+"/timeline/rate", "rate %v, want 0 to %d", r, maxTimelineRate)
	}
	if s.GetMax() <= 0 {
		return refuse(CodeRange, at+"/max", "a timeline's max %v is not above 0", s.GetMax())
	}
	return nil
}

func ordered(lo, v, hi float64, at string) *Error {
	for _, x := range []float64{lo, v, hi} {
		if err := finite(x, at); err != nil {
			return err
		}
	}
	if lo >= hi {
		return refuse(CodeRange, at+"/min", "min %v is not below max %v", lo, hi)
	}
	if v < lo || v > hi {
		return refuse(CodeRange, at+"/value", "value %v is outside %v..%v", v, lo, hi)
	}
	return nil
}

func checkChoices(c *cardsv1.Choices, at string) *Error {
	seen := map[string]bool{}
	for i, o := range c.GetOptions() {
		if seen[o.GetId()] {
			return refuse(CodeChoices, at+"/options/"+strconv.Itoa(i)+"/id", "option %q twice", o.GetId())
		}
		seen[o.GetId()] = true
	}
	if !c.GetMultiple() {
		if c.GetMin() != 0 || c.GetMax() != 0 || c.GetSubmitLabel() != "" {
			return refuse(CodeChoices, at, "min, max and submitLabel need multiple")
		}
		return nil
	}
	lo, hi := choiceBounds(c)
	if hi > len(c.GetOptions()) || lo > hi {
		return refuse(CodeChoices, at, "min %d and max %d do not fit %d options", lo, hi, len(c.GetOptions()))
	}
	return nil
}

// choiceBounds is a multiple answer's size: min 0 means 1, max 0 every option.
func choiceBounds(c *cardsv1.Choices) (lo, hi int) {
	lo, hi = int(c.GetMin()), int(c.GetMax())
	if lo == 0 {
		lo = 1
	}
	if hi == 0 {
		hi = len(c.GetOptions())
	}
	return lo, hi
}
