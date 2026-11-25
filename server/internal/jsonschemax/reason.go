package jsonschemax

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var english = message.NewPrinter(language.English)

// reason words a failure for the model. The library's localized strings group
// digits ("1,000") and name combinators by keyword, so those kinds get plain
// text of their own.
func reason(k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.OneOf:
		if len(k.Subschemas) > 1 {
			return "matches more than one of the allowed forms, but must match exactly one"
		}
		return "matches none of the allowed forms"
	case *kind.AnyOf:
		return "matches none of the allowed forms"
	case *kind.Minimum:
		return bound("minimum", num(k.Got), num(k.Want))
	case *kind.Maximum:
		return bound("maximum", num(k.Got), num(k.Want))
	case *kind.ExclusiveMinimum:
		return bound("exclusiveMinimum", num(k.Got), num(k.Want))
	case *kind.ExclusiveMaximum:
		return bound("exclusiveMaximum", num(k.Got), num(k.Want))
	case *kind.MultipleOf:
		return bound("multipleOf", num(k.Got), num(k.Want))
	case *kind.MinLength:
		return bound("minLength", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	case *kind.MaxLength:
		return bound("maxLength", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	case *kind.MinItems:
		return bound("minItems", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	case *kind.MaxItems:
		return bound("maxItems", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	case *kind.MinProperties:
		return bound("minProperties", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	case *kind.MaxProperties:
		return bound("maxProperties", strconv.Itoa(k.Got), strconv.Itoa(k.Want))
	}
	return k.LocalizedString(english)
}

// branches reports the kinds that fail only when no branch matched.
func branches(k jsonschema.ErrorKind) bool {
	switch k.(type) {
	case *kind.OneOf, *kind.AnyOf:
		return true
	}
	return false
}

func bound(keyword, got, want string) string {
	return fmt.Sprintf("%s: got %s, want %s", keyword, got, want)
}

func num(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	f, _ := r.Float64()
	return strconv.FormatFloat(f, 'f', -1, 64)
}
