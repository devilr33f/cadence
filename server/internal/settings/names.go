package settings

import (
	"bytes"
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
)

// Names is a list of provider instance names that also reads one name alone,
// as the key held before it took several: "" is none.
type Names []string

func (n *Names) UnmarshalJSON(b []byte) error {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '"' {
		var one string
		if err := json.Unmarshal(t, &one); err != nil {
			return err
		}
		*n = nil
		if one != "" {
			*n = Names{one}
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	*n = list
	return nil
}

// Schema is the list's: one name alone is read too, but not advertised
func (Names) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeArray, Items: &huma.Schema{Type: huma.TypeString}}
}

// nameLists are the keys of Names that one name alone may have been stored
// or sent as
var nameLists = []string{"music"}

// normalizeNames rewrites one name alone as a list of it, so every reader
// sees a list: "" (unset in a put) is left for the put to drop
func normalizeNames(o Overrides) {
	for _, k := range nameLists {
		raw, ok := o[k]
		if !ok || isNull(raw) || isEmptyString(raw) {
			continue
		}
		var n Names
		if json.Unmarshal(raw, &n) != nil {
			continue
		}
		if b, err := json.Marshal([]string(n)); err == nil {
			o[k] = b
		}
	}
}
