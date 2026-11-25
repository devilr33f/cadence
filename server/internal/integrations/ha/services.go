package ha

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxDomains           = 256
	maxServicesPerDomain = 64
	maxFieldsPerService  = 32
	maxServiceName       = 64
	maxKind              = 120
)

var fieldNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Service is what Home Assistant says one service takes. Description and
// field descriptions are its text: sanitize them before the model sees them.
type Service struct {
	Description string
	// Fields keep Home Assistant's order, a collapsible section's fields
	// in its place
	Fields []Field
}

type Field struct {
	Name     string
	Required bool
	// Kind is a one-line shape from the field's selector ("number 0..100 %",
	// "one of a, b", "boolean"), sanitized; empty when unknown
	Kind string
	// Selector is the selector's own name as Home Assistant wrote it
	// ("entity", "number"), empty without one; compare it, never show it
	Selector    string
	Description string
}

type member struct {
	key string
	val json.RawMessage
}

// members reads a JSON object's members in document order, the first of a
// repeated key kept; ok is false when raw is no object.
func members(raw json.RawMessage) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []member
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		var val json.RawMessage
		if err != nil || !isKey || dec.Decode(&val) != nil {
			return nil, false
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, member{key, val})
		}
	}
	return out, true
}

// parseServices reads get_services (domain -> service -> description): the
// first 256 domains, 64 services each and 32 fields per service in Home
// Assistant's order. A malformed domain or service is dropped, and so is a
// service with a malformed field, so no check ever runs on half a service.
func parseServices(raw json.RawMessage) (map[string]map[string]Service, error) {
	domains, ok := members(raw)
	if !ok {
		return nil, fail(CodeBadResponse)
	}
	out := make(map[string]map[string]Service, min(len(domains), maxDomains))
	for _, d := range domains {
		if len(out) == maxDomains {
			break
		}
		services, ok := members(d.val)
		if !ok || len(d.key) > maxDomain || !domainRe.MatchString(d.key) {
			continue
		}
		byName := map[string]Service{}
		for _, s := range services {
			if len(byName) == maxServicesPerDomain {
				break
			}
			if len(s.key) > maxServiceName || !domainRe.MatchString(s.key) {
				continue
			}
			if svc, ok := parseService(s.val); ok {
				byName[s.key] = svc
			}
		}
		out[d.key] = byName
	}
	return out, nil
}

func parseService(raw json.RawMessage) (Service, bool) {
	var s struct {
		Description json.RawMessage `json:"description"`
		Fields      json.RawMessage `json:"fields"`
	}
	var svc Service
	if json.Unmarshal(raw, &s) != nil || !text(s.Description, maxDescription, &svc.Description) {
		return Service{}, false
	}
	if isNull(s.Fields) {
		return svc, true
	}
	fields, ok := flatFields(s.Fields)
	if !ok {
		return Service{}, false
	}
	for _, f := range fields {
		if len(svc.Fields) == maxFieldsPerService {
			break
		}
		field, ok := parseField(f)
		if !ok {
			return Service{}, false
		}
		svc.Fields = append(svc.Fields, field)
	}
	return svc, true
}

// flatFields lists a service's fields with each section's fields in its
// place: a section only groups fields on screen, the call takes them flat.
func flatFields(raw json.RawMessage) ([]member, bool) {
	fields, ok := members(raw)
	if !ok {
		return nil, false
	}
	var out []member
	for _, f := range fields {
		var sec struct {
			Fields json.RawMessage `json:"fields"`
		}
		if json.Unmarshal(f.val, &sec) != nil || isNull(sec.Fields) {
			out = append(out, f)
			continue
		}
		inner, ok := members(sec.Fields)
		if !ok {
			return nil, false
		}
		out = append(out, inner...)
	}
	return out, true
}

func parseField(m member) (Field, bool) {
	var f struct {
		Required    json.RawMessage `json:"required"`
		Description json.RawMessage `json:"description"`
		Selector    json.RawMessage `json:"selector"`
	}
	field := Field{Name: m.key}
	if !fieldNameRe.MatchString(m.key) || json.Unmarshal(m.val, &f) != nil ||
		!text(f.Description, maxDescription, &field.Description) {
		return Field{}, false
	}
	if !isNull(f.Required) && json.Unmarshal(f.Required, &field.Required) != nil {
		return Field{}, false
	}
	if sel, ok := members(f.Selector); ok && len(sel) > 0 {
		field.Selector = sel[0].key
		field.Kind = Sanitize(selectorKind(sel[0].key, sel[0].val), maxKind)
	}
	return field, true
}

// selectorKind is v1's one-line shape of a selector, from its first key;
// empty for a kind it does not know.
func selectorKind(kind string, config json.RawMessage) string {
	switch kind {
	case "number":
		var c map[string]any
		if json.Unmarshal(config, &c) != nil || c == nil {
			return ""
		}
		out := "number"
		lo, okLo := c["min"].(float64)
		hi, okHi := c["max"].(float64)
		if okLo && okHi {
			out += " " + num(lo) + ".." + num(hi)
		}
		if unit, ok := c["unit_of_measurement"].(string); ok {
			out += " " + unit
		}
		return out
	case "select":
		var c map[string]json.RawMessage
		if json.Unmarshal(config, &c) != nil || c == nil {
			return ""
		}
		var options []any
		if json.Unmarshal(c["options"], &options) != nil || options == nil {
			return "one of …"
		}
		values := make([]string, len(options))
		for i, o := range options {
			values[i] = optionValue(o)
		}
		return "one of " + strings.Join(values, ", ")
	}
	return plainKinds[kind]
}

var plainKinds = map[string]string{
	"boolean": "boolean", "text": "text", "color_temp": "color temperature",
	"rgb_color": "color", "rgbw_color": "color", "xy_color": "color",
	"time": "time", "date": "date", "duration": "duration",
	"entity": "entity id", "area": "area", "object": "object",
}

// optionValue is a select option as v1 printed it: a string, else an
// object's value, else ?.
func optionValue(o any) string {
	switch v := o.(type) {
	case string:
		return v
	case map[string]any:
		value, ok := v["value"]
		if !ok {
			return "?"
		}
		switch x := value.(type) {
		case string:
			return x
		case float64:
			return num(x)
		case bool:
			return strconv.FormatBool(x)
		case nil:
			return "null"
		}
	}
	return "?"
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
