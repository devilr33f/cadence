package cards

import (
	"regexp"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

var (
	cardIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	actionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:~-]{0,127}$`)
	optionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	imageRefPattern = regexp.MustCompile(`^img:[A-Za-z0-9_-]{1,96}$`)
	iconPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	featurePattern  = regexp.MustCompile(`^[a-z][a-z_.]{0,31}$`)
	tokenPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// rule bounds one field: a string's length in code points (each element's for
// a repeated string) and pattern, a list's size, and whether it must be set
// (a non-empty string, a non-zero enum, a present message). Patterns apply to
// non-empty strings only.
type rule struct {
	required           bool
	maxLen             int
	pattern            *regexp.Regexp
	minItems, maxItems int
}

func req(maxLen int) rule          { return rule{required: true, maxLen: maxLen} }
func opt(maxLen int) rule          { return rule{maxLen: maxLen} }
func pat(p *regexp.Regexp) rule    { return rule{maxLen: 128, pattern: p} }
func reqPat(p *regexp.Regexp) rule { return rule{required: true, maxLen: 128, pattern: p} }
func list(lo, hi int) rule         { return rule{minItems: lo, maxItems: hi} }
func set() rule                    { return rule{required: true} }

// rules covers every string, enum, message and repeated field of the package
// by its full name; TestEveryFieldHasARule keeps it whole. A field missing
// here is a scalar with no bound of its own (numbers, bools, checked in
// check.go where they have one).
var rules = map[protoreflect.FullName]rule{
	"cadence.cards.v1.Card.id":            reqPat(cardIDPattern),
	"cadence.cards.v1.Card.fallback_text": req(280),
	"cadence.cards.v1.Card.root":          set(),
	"cadence.cards.v1.Card.features":      {minItems: 1, maxItems: 40, maxLen: 32, pattern: featurePattern},
	"cadence.cards.v1.Page.cards":         list(0, MaxPageCards),
	"cadence.cards.v1.Page.next":          {},
	"cadence.cards.v1.Page.id":            pat(cardIDPattern),

	"cadence.cards.v1.Action.id":      reqPat(actionIDPattern),
	"cadence.cards.v1.Action.confirm": opt(160),

	// a card's action names its card, a page's next action its page; DecodeActionRequest refuses both at once
	"cadence.cards.v1.ActionRequest.card_id":   pat(cardIDPattern),
	"cadence.cards.v1.ActionRequest.page_id":   pat(cardIDPattern),
	"cadence.cards.v1.ActionRequest.action_id": reqPat(actionIDPattern),
	"cadence.cards.v1.ActionRequest.value":     {},
	// checked by ValidateActionResult, a card and a page as ValidateCard and ValidatePage do
	"cadence.cards.v1.CardActionResult.card":    {},
	"cadence.cards.v1.CardActionResult.page":    {},
	"cadence.cards.v1.CardActionResult.refusal": {},
	"cadence.cards.v1.ActionRefusal.reason":     reqPat(tokenPattern),
	"cadence.cards.v1.ActionRefusal.toast":      opt(160),
	"cadence.cards.v1.ActionRefusal.card":       {},
	"cadence.cards.v1.ActionValue.id":           {maxLen: 64, pattern: optionIDPattern},
	"cadence.cards.v1.ActionValue.ids":          {},
	"cadence.cards.v1.IdList.ids":               {maxItems: 8, maxLen: 64, pattern: optionIDPattern},

	"cadence.cards.v1.Node.card":       {},
	"cadence.cards.v1.Node.row":        {},
	"cadence.cards.v1.Node.column":     {},
	"cadence.cards.v1.Node.grid":       {},
	"cadence.cards.v1.Node.text":       {},
	"cadence.cards.v1.Node.long_text":  {},
	"cadence.cards.v1.Node.icon":       {},
	"cadence.cards.v1.Node.image":      {},
	"cadence.cards.v1.Node.state":      {},
	"cadence.cards.v1.Node.value":      {},
	"cadence.cards.v1.Node.meter":      {},
	"cadence.cards.v1.Node.progress":   {},
	"cadence.cards.v1.Node.countdown":  {},
	"cadence.cards.v1.Node.steps":      {},
	"cadence.cards.v1.Node.toggle":     {},
	"cadence.cards.v1.Node.button":     {},
	"cadence.cards.v1.Node.keyvalue":   {},
	"cadence.cards.v1.Node.badge":      {},
	"cadence.cards.v1.Node.media_tile": {},
	"cadence.cards.v1.Node.paged_list": {},
	"cadence.cards.v1.Node.divider":    {},
	"cadence.cards.v1.Node.sparkline":  {},
	"cadence.cards.v1.Node.link":       {},
	"cadence.cards.v1.Node.post":       {},
	"cadence.cards.v1.Node.slider":     {},
	"cadence.cards.v1.Node.qr":         {},
	"cadence.cards.v1.Node.code":       {},
	"cadence.cards.v1.Node.choices":    {},

	"cadence.cards.v1.ImageRef.ref":    reqPat(imageRefPattern),
	"cadence.cards.v1.ImageRef.alt":    opt(120),
	"cadence.cards.v1.ImageRef.aspect": {},

	"cadence.cards.v1.CardBlock.title":    req(80),
	"cadence.cards.v1.CardBlock.subtitle": opt(80),
	"cadence.cards.v1.CardBlock.icon":     pat(iconPattern),
	"cadence.cards.v1.CardBlock.tone":     {},
	"cadence.cards.v1.CardBlock.children": list(0, MaxNodes-1),
	"cadence.cards.v1.CardBlock.actions":  list(0, 4),

	"cadence.cards.v1.Row.children":    list(0, MaxNodes-1),
	"cadence.cards.v1.Row.gap":         {},
	"cadence.cards.v1.Column.children": list(0, MaxNodes-1),
	"cadence.cards.v1.Column.gap":      {},
	"cadence.cards.v1.Grid.children":   list(0, MaxNodes-1),
	"cadence.cards.v1.Grid.gap":        {},

	"cadence.cards.v1.Text.text":  req(280),
	"cadence.cards.v1.Text.style": set(),
	"cadence.cards.v1.Text.tone":  {},

	"cadence.cards.v1.LongText.text": req(8000),

	"cadence.cards.v1.Icon.name": reqPat(iconPattern),
	"cadence.cards.v1.Icon.tone": {},

	"cadence.cards.v1.Image.ref":    reqPat(imageRefPattern),
	"cadence.cards.v1.Image.alt":    req(120),
	"cadence.cards.v1.Image.aspect": set(),

	"cadence.cards.v1.State.label": req(40),
	"cadence.cards.v1.State.kind":  set(),
	"cadence.cards.v1.State.tone":  {},

	"cadence.cards.v1.Value.value": req(24),
	"cadence.cards.v1.Value.unit":  opt(12),
	"cadence.cards.v1.Value.label": opt(40),
	"cadence.cards.v1.Value.trend": {},

	"cadence.cards.v1.Meter.label":       req(40),
	"cadence.cards.v1.Meter.value_label": req(40),

	"cadence.cards.v1.Progress.label": opt(40),

	"cadence.cards.v1.Countdown.label":  req(40),
	"cadence.cards.v1.Countdown.format": {},

	"cadence.cards.v1.Steps.items": list(1, 12),
	"cadence.cards.v1.Step.label":  req(80),
	"cadence.cards.v1.Step.status": set(),

	"cadence.cards.v1.Toggle.label":  req(40),
	"cadence.cards.v1.Toggle.action": {},

	"cadence.cards.v1.Button.label":  req(40),
	"cadence.cards.v1.Button.icon":   pat(iconPattern),
	"cadence.cards.v1.Button.action": set(),

	"cadence.cards.v1.KeyValue.items":     list(1, 12),
	"cadence.cards.v1.KeyValueItem.key":   req(40),
	"cadence.cards.v1.KeyValueItem.value": req(80),

	"cadence.cards.v1.Badge.kind": set(),
	"cadence.cards.v1.Badge.text": opt(24),

	"cadence.cards.v1.MediaTile.image":     set(),
	"cadence.cards.v1.MediaTile.title":     req(80),
	"cadence.cards.v1.MediaTile.subtitle":  opt(80),
	"cadence.cards.v1.MediaTile.badges":    list(0, 4),
	"cadence.cards.v1.MediaTile.action":    set(),
	"cadence.cards.v1.MediaTile.secondary": list(0, 4),

	"cadence.cards.v1.LabeledAction.action": set(),
	"cadence.cards.v1.LabeledAction.label":  req(40),
	"cadence.cards.v1.LabeledAction.icon":   pat(iconPattern),

	"cadence.cards.v1.PagedList.items": list(0, MaxListItems),
	"cadence.cards.v1.PagedList.more":  {},

	"cadence.cards.v1.Sparkline.points": list(2, 64),
	"cadence.cards.v1.Sparkline.label":  opt(40),

	"cadence.cards.v1.Link.domain":    req(64),
	"cadence.cards.v1.Link.favicon":   {},
	"cadence.cards.v1.Link.title":     req(120),
	"cadence.cards.v1.Link.snippet":   opt(240),
	"cadence.cards.v1.Link.time":      opt(40),
	"cadence.cards.v1.Link.thumbnail": {},
	"cadence.cards.v1.Link.action":    set(),

	"cadence.cards.v1.Post.author":      req(80),
	"cadence.cards.v1.Post.handle":      opt(40),
	"cadence.cards.v1.Post.avatar":      {},
	"cadence.cards.v1.Post.time":        opt(40),
	"cadence.cards.v1.Post.text":        req(500),
	"cadence.cards.v1.Post.media":       {},
	"cadence.cards.v1.Post.stats":       list(0, 4),
	"cadence.cards.v1.Post.source":      opt(24),
	"cadence.cards.v1.Post.action":      {},
	"cadence.cards.v1.Post.note":        {},
	"cadence.cards.v1.Post.quote":       {},
	"cadence.cards.v1.Post.reply_to":    opt(40),
	"cadence.cards.v1.Post.reposted_by": opt(40),
	"cadence.cards.v1.PostStat.kind":    set(),
	"cadence.cards.v1.PostStat.label":   req(24),
	"cadence.cards.v1.Note.label":       req(40),
	"cadence.cards.v1.Note.text":        req(500),

	"cadence.cards.v1.Slider.unit":        opt(12),
	"cadence.cards.v1.Slider.label":       req(40),
	"cadence.cards.v1.Slider.value_label": opt(40),
	// required unless a timeline is set (check.go): a playback position with no action is shown, not focused
	"cadence.cards.v1.Slider.action":   {},
	"cadence.cards.v1.Slider.timeline": {},

	"cadence.cards.v1.Qr.payload": req(512),
	"cadence.cards.v1.Qr.caption": opt(80),

	"cadence.cards.v1.Code.text":  req(4000),
	"cadence.cards.v1.Code.label": opt(40),

	"cadence.cards.v1.Choices.options":      list(1, 8),
	"cadence.cards.v1.Choices.action":       set(),
	"cadence.cards.v1.Choices.submit_label": opt(40),
	"cadence.cards.v1.Choice.id":            reqPat(optionIDPattern),
	"cadence.cards.v1.Choice.label":         req(40),
}

// checkFields walks m and every message below it: no unknown fields, every
// enum value known, every rule of rules held. Paths use the protojson names.
func checkFields(m protoreflect.Message, at string) *Error {
	if len(m.GetUnknown()) > 0 {
		return refuse(CodeUnknownField, at, "fields this vocabulary does not know")
	}
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if o := fd.ContainingOneof(); o != nil && m.WhichOneof(o) != fd {
			continue
		}
		path := at + "/" + fd.JSONName()
		r := rules[fd.FullName()]
		v := m.Get(fd)
		switch {
		case fd.IsList():
			l := v.List()
			if l.Len() < r.minItems || (r.maxItems > 0 && l.Len() > r.maxItems) {
				return refuse(CodeLength, path, "%d items, want %d to %d", l.Len(), r.minItems, r.maxItems)
			}
			for j := range l.Len() {
				if err := checkValue(fd, r, l.Get(j), path+"/"+strconv.Itoa(j)); err != nil {
					return err
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			if !m.Has(fd) {
				if r.required {
					return refuse(CodeRequired, path, "missing")
				}
				continue
			}
			if err := checkValue(fd, r, v, path); err != nil {
				return err
			}
		default:
			if r.required && !m.Has(fd) {
				return refuse(CodeRequired, path, "missing")
			}
			if err := checkValue(fd, r, v, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkValue(fd protoreflect.FieldDescriptor, r rule, v protoreflect.Value, at string) *Error {
	switch fd.Kind() {
	case protoreflect.MessageKind:
		return checkFields(v.Message(), at)
	case protoreflect.StringKind:
		s := v.String()
		if !utf8.ValidString(s) {
			return refuse(CodeLength, at, "not UTF-8")
		}
		if n := utf8.RuneCountInString(s); r.maxLen > 0 && n > r.maxLen {
			return refuse(CodeLength, at, "%d characters, over %d", n, r.maxLen)
		}
		if s != "" && r.pattern != nil && !r.pattern.MatchString(s) {
			return refuse(CodePattern, at, "%q does not match %s", s, r.pattern)
		}
	case protoreflect.EnumKind:
		if fd.Enum().Values().ByNumber(v.Enum()) == nil {
			return refuse(CodeEnum, at, "unknown value %d", v.Enum())
		}
	}
	return nil
}
