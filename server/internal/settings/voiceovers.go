package settings

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
)

// CatalogVoiceovers is the household's taste in the catalog's dubs: Prefer names voiceovers (AniLibria, AniDUB)
// and sources (anime365, aniliberty) in the order they are wanted, Avoid the ones never taken unasked. A title's
// remembered choice comes first; catalog_play asks the user when neither settles a title.
type CatalogVoiceovers struct {
	Prefer []string `json:"prefer,omitempty" nullable:"false" maxItems:"32" doc:"Dubs and sources wanted, best first."`
	Avoid  []string `json:"avoid,omitempty" nullable:"false" maxItems:"32" doc:"Dubs and sources never taken unasked."`
}

const (
	maxVoiceoverNames = 32
	maxVoiceoverName  = 64
)

// VoiceoverKey is a dub's or a source's name as preferences compare it: lower case, letters and digits alone
// ("Studio Band" and "studioband" are one)
func VoiceoverKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validVoiceovers checks a catalogVoiceovers value: an object of prefer and avoid (either may be left out), each
// at most 32 names of 1 to 64 characters, no name twice across both
func validVoiceovers(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var v CatalogVoiceovers
	if dec.Decode(&v) != nil || len(v.Prefer) > maxVoiceoverNames || len(v.Avoid) > maxVoiceoverNames {
		return false
	}
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, v.Prefer...), v.Avoid...) {
		k := VoiceoverKey(n)
		if k == "" || len([]rune(n)) > maxVoiceoverName || seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}
