package i18n

import "strings"

// PluralCategory is a cldr plural category.
type PluralCategory string

const (
	Zero  PluralCategory = "zero"
	One   PluralCategory = "one"
	Two   PluralCategory = "two"
	Few   PluralCategory = "few"
	Many  PluralCategory = "many"
	Other PluralCategory = "other"
)

// Categories lists the cardinal categories a language has (cldr): en one/other, ru one/few/many/other.
func Categories(l Lang) []PluralCategory {
	if l.rules() == Ru {
		return []PluralCategory{One, Few, Many, Other}
	}
	return []PluralCategory{One, Other}
}

// PluralOf is the cardinal category of a number as it is shown: formatted is its digits with "." before any
// visible fraction ("21", "1.5"), so a shown fraction (v != 0) takes the fraction rules.
func PluralOf(l Lang, formatted string) PluralCategory {
	digits := strings.TrimPrefix(formatted, "-")
	if strings.Contains(digits, ".") {
		// en: one needs v = 0; ru: one, few and many need v = 0
		return Other
	}
	// i mod 100 from the last two digits, so a long number never overflows
	i100 := 0
	for _, c := range digits {
		i100 = (i100*10 + int(c-'0')) % 100
	}
	i10 := i100 % 10
	if l.rules() == Ru {
		switch {
		case i10 == 1 && i100 != 11:
			return One
		case i10 >= 2 && i10 <= 4 && (i100 < 12 || i100 > 14):
			return Few
		default:
			return Many
		}
	}
	if digits == "1" {
		return One
	}
	return Other
}
