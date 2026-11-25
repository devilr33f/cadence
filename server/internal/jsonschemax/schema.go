package jsonschemax

import (
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var errRemoteRef = errors.New("remote schema references are refused")

// schemas never fetch anything
type refusingLoader struct{}

func (refusingLoader) Load(string) (any, error) { return nil, errRemoteRef }

// Compile returns nil for an empty schema, which accepts any value.
func Compile(src string) (*jsonschema.Schema, error) {
	if src == "" {
		return nil, nil
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refusingLoader{})
	const url = "cadence:///schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

// Validate returns the JSON pointer of the first failing location, and never
// the instance, so its result may answer any client.
func Validate(sch *jsonschema.Schema, raw []byte) (path string, ok bool) {
	f, ok := Check(sch, raw)
	return f.Path, ok
}

// Failure is where an instance failed and why, in English. Reason may quote
// the instance's own values, so it goes only to whoever sent the instance
// (the model), never to a log.
type Failure struct{ Path, Reason string }

// Check is Validate with the reason of the deepest first cause. A oneOf or
// anyOf that matched nothing is reported itself, with its first branch's
// failure: that branch alone would read as the only way to pass.
func Check(sch *jsonschema.Schema, raw []byte) (Failure, bool) {
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return Failure{Reason: "not valid JSON"}, false
	}
	if sch == nil {
		return Failure{}, true
	}
	err = sch.Validate(inst)
	if err == nil {
		return Failure{}, true
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return Failure{Reason: "invalid value"}, false
	}
	verr = firstCause(verr)
	f := Failure{Path: pointer(verr.InstanceLocation), Reason: reason(verr.ErrorKind)}
	if len(verr.Causes) > 0 {
		c := firstCause(verr.Causes[0])
		f.Reason += "; the first fails"
		if at := pointer(c.InstanceLocation); at != f.Path {
			f.Reason += " at " + at
		}
		f.Reason += ": " + reason(c.ErrorKind)
	}
	return f, false
}

func firstCause(e *jsonschema.ValidationError) *jsonschema.ValidationError {
	for len(e.Causes) > 0 && !branches(e.ErrorKind) {
		e = e.Causes[0]
	}
	return e
}

func pointer(tokens []string) string {
	var b strings.Builder
	esc := strings.NewReplacer("~", "~0", "/", "~1")
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(esc.Replace(t))
	}
	return b.String()
}

// ConfigInvalidError carries only where the config failed, never its value.
type ConfigInvalidError struct{ Path string }

func (e *ConfigInvalidError) Error() string {
	return fmt.Sprintf("config invalid at %q", e.Path)
}
