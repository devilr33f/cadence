package i18n

import "strings"

// Lang is a supported language: a catalog's file name and the device's language setting.
type Lang string

const (
	En Lang = "en"
	Ru Lang = "ru"
	// Pseudo is made from en at load time, for overflow checks: never a setting's value
	Pseudo Lang = "xx"
)

// Catalogued are the languages a catalog file exists for. Pseudo is derived.
var Catalogued = []Lang{En, Ru}

// Normalize maps a language setting or tag (ru, ru-RU, ru_RU, RU) to a supported language; anything else is en.
func Normalize(tag string) Lang {
	t := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(t, "-_"); i >= 0 {
		t = t[:i]
	}
	switch Lang(t) {
	case Ru:
		return Ru
	case Pseudo:
		return Pseudo
	default:
		return En
	}
}

// rules gives the language whose plural rules and number symbols a language uses: xx takes en's
func (l Lang) rules() Lang {
	if l == Ru {
		return Ru
	}
	return En
}
