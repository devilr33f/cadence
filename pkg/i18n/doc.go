// Package i18n is cadence's message format and catalogs (tv plan item 17), shared in shape with the screen's C++
// screen/i18n: an icu messageformat subset ({name}, {name, number}, plural with cldr categories, select), catalogs
// in i18n/<area>/<lang>.json with dotted keys, en and ru, the pseudo-locale xx (accented, about 40 % longer, made
// from en), and the locale formatting of numbers, clocks, countdowns and durations.
//
// i18n/testdata/format.json holds the cases both implementations must answer alike.
package i18n
