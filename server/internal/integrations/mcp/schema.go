package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

const (
	maxSchemaBytes = 8 << 10
	maxSchemaDepth = 8
	maxSchemaProps = 64
	maxDescription = 600
	maxSchemaText  = 200
)

// why the catalog leaves a tool out
const (
	// SkipName: an empty remote name, one over 128 code points or with control characters
	SkipName = "name"
	// SkipDuplicate: a remote name listed twice, a local name another server
	// took first, or no local name left for it
	SkipDuplicate        = "duplicate"
	SkipSchemaSize       = "schema_size"
	SkipSchemaDepth      = "schema_depth"
	SkipSchemaProperties = "schema_properties"
	SkipSchemaType       = "schema_type"
	SkipSchemaRef        = "schema_ref"
	// SkipSchema: a schema that does not compile, or has a pattern over 512 bytes
	SkipSchema = "schema"
	// SkipChanged: the hash moved past the acknowledged one (Ruling M5d-R2)
	SkipChanged = "changed"
	// SkipCap: past 64 tools of its server or 128 in all, the owner's
	// enabledDefault tools ranked first, then by local name
	SkipCap = "cap"
	// SkipUnavailable: the server has not listed for 10 minutes
	SkipUnavailable = "unavailable"
)

// checkSchema is "" for an inputSchema the model may get: at most 8 KiB as
// listed, an object of type object, nested at most 8 deep, no object over 64
// members, and no $ref that leaves the document. The root is 1 deep and each
// object or array in it one more, except that a map of named subschemas
// (properties and its like) and a subschema in it are one level together:
// properties nested 8 deep fit. Compiling it is the caller's.
func checkSchema(raw json.RawMessage) string {
	if len(raw) > maxSchemaBytes {
		return SkipSchemaSize
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return SkipSchema
	}
	if obj, ok := v.(map[string]any); !ok || obj["type"] != "object" {
		return SkipSchemaType
	}
	var w walk
	w.visit(v, 1)
	switch {
	case w.deep:
		return SkipSchemaDepth
	case w.wide:
		return SkipSchemaProperties
	case w.remote:
		return SkipSchemaRef
	}
	return ""
}

// schemaMaps are the keywords whose value maps names to subschemas.
var schemaMaps = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true,
	// draft-07: a name to a subschema or to the names it requires
	"dependencies": true,
}

type walk struct{ deep, wide, remote bool }

func (w *walk) visit(v any, depth int) {
	switch v := v.(type) {
	case map[string]any:
		if w.over(len(v), depth) {
			return
		}
		for k, child := range v {
			if ref, ok := child.(string); ok && k == "$ref" && !strings.HasPrefix(ref, "#") {
				w.remote = true
			}
			if named, ok := child.(map[string]any); ok && schemaMaps[k] {
				if !w.over(len(named), depth+1) {
					for _, sub := range named {
						w.visit(sub, depth+1)
					}
				}
				continue
			}
			w.visit(child, depth+1)
		}
	case []any:
		if w.over(0, depth) {
			return
		}
		for _, child := range v {
			w.visit(child, depth+1)
		}
	}
}

// over notes a container of members past the cap or depth past the limit;
// true stops the walk below it.
func (w *walk) over(members, depth int) bool {
	if members > maxSchemaProps {
		w.wide = true
	}
	if depth > maxSchemaDepth {
		w.deep = true
		return true
	}
	return false
}

// describe is the description the model gets: control and format characters
// dropped, whitespace runs one space, "[MCP <prefix>] " before it, at most
// 600 code points.
func describe(prefix, desc string) string {
	return head(strings.TrimSpace("[MCP "+prefix+"] "+oneLine(desc)), maxDescription)
}

// oneLine is s with control and format characters dropped and whitespace
// runs one space, trimmed.
func oneLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// head is the first n code points of s.
func head(s string, n int) string {
	out := []rune(s)
	return string(out[:min(len(out), n)])
}

// schemaVocabulary is every keyword the model reads: JSON Schema 2020-12's,
// examples aside, and draft-07's dependencies and definitions. Comments,
// extensions and unknown keywords are only more of the server's text.
var schemaVocabulary = map[string]bool{
	"$schema": true, "$id": true, "$ref": true, "$anchor": true, "$dynamicRef": true, "$dynamicAnchor": true,
	"$vocabulary": true, "$defs": true,
	"prefixItems": true, "items": true, "contains": true, "additionalProperties": true, "properties": true,
	"patternProperties": true, "dependentSchemas": true, "propertyNames": true, "if": true, "then": true,
	"else": true, "allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"unevaluatedItems": true, "unevaluatedProperties": true,
	"type": true, "const": true, "enum": true, "multipleOf": true, "maximum": true, "exclusiveMaximum": true,
	"minimum": true, "exclusiveMinimum": true, "maxLength": true, "minLength": true, "pattern": true,
	"maxItems": true, "minItems": true, "uniqueItems": true, "maxContains": true, "minContains": true,
	"maxProperties": true, "minProperties": true, "required": true, "dependentRequired": true,
	"title": true, "description": true, "default": true, "deprecated": true, "readOnly": true, "writeOnly": true,
	"format": true, "contentEncoding": true, "contentMediaType": true, "contentSchema": true,
	"dependencies": true, "definitions": true,
}

// schemaData hold data, not schemas: what they say changes what validates,
// so their text is left as the server wrote it.
var schemaData = map[string]bool{"const": true, "enum": true, "default": true, "dependentRequired": true, "$vocabulary": true}

// maxPattern bounds a regular expression of the schema, in bytes
const maxPattern = 512

var errLongPattern = errors.New("mcp: a schema pattern over 512 bytes")

// schemaText is the inputSchema the model reads: the vocabulary's keywords
// alone, every description and title string at any depth one line of at
// most 200 code points. A pattern over 512 bytes fails it. The caps and the
// hash are taken over the schema as listed.
func schemaText(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	var c cleaner
	v = c.schema(v)
	if c.long {
		return "", errLongPattern
	}
	out, err := json.Marshal(v)
	return string(out), err
}

// cleaner notes a pattern too long while it cleans a schema.
type cleaner struct{ long bool }

func (c *cleaner) schema(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = c.schema(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			named, isMap := x.(map[string]any)
			text, isText := x.(string)
			switch {
			case !schemaVocabulary[k]:
			case schemaData[k]:
				out[k] = x
			case isText && (k == "description" || k == "title"):
				out[k] = head(oneLine(text), maxSchemaText)
			case isMap && schemaMaps[k]:
				// names to subschemas: a property may be called examples
				subs := make(map[string]any, len(named))
				for name, sub := range named {
					c.pattern(k == "patternProperties", name)
					subs[name] = c.schema(sub)
				}
				out[k] = subs
			default:
				c.pattern(k == "pattern" && isText, text)
				out[k] = c.schema(x)
			}
		}
		return out
	}
	return v
}

func (c *cleaner) pattern(is bool, s string) {
	if is && len(s) > maxPattern {
		c.long = true
	}
}

// hashedHints are the annotations as a hash takes them: every field always
// written, a hint the server left unset null. The SDK's own encoding of
// them changes with MCPGODEBUG, so it never reaches a hash.
type hashedHints struct {
	ReadOnly    bool   `json:"readOnly"`
	Destructive *bool  `json:"destructive"`
	Idempotent  bool   `json:"idempotent"`
	OpenWorld   *bool  `json:"openWorld"`
	Title       string `json:"title"`
}

// toolHash is the sha256 of what the owner's overrides were given to: the
// server's endpoint, and the remote description, inputSchema and annotations,
// as canonical JSON. An instance pointed at another server under its name
// thus changes every tool.
func toolHash(endpoint string, t RemoteTool) string {
	canon := func(raw []byte) any {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			return string(raw)
		}
		return v
	}
	var hints *hashedHints
	if a := t.Annotations; a != nil {
		hints = &hashedHints{a.ReadOnlyHint, a.DestructiveHint, a.IdempotentHint, a.OpenWorldHint, a.Title}
	}
	raw, _ := json.Marshal(struct {
		Endpoint    string       `json:"endpoint"`
		Description string       `json:"description"`
		InputSchema any          `json:"inputSchema"`
		Annotations *hashedHints `json:"annotations"`
	}{endpoint, t.Description, canon(t.InputSchema), hints})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
