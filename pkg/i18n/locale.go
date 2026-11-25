package i18n

import (
	"strconv"
	"strings"
	"time"
)

// nbsp separates thousands in ru, and a number from its unit or an AM/PM marker in every language
const nbsp = "\u00a0"

type symbols struct {
	group, decimal string
}

func symbolsOf(l Lang) symbols {
	if l.rules() == Ru {
		return symbols{group: nbsp, decimal: ","}
	}
	return symbols{group: ",", decimal: "."}
}

// localizeNumber groups a plain number's integer digits by three and swaps in the language's decimal sign
func localizeNumber(l Lang, plain string) string {
	sym := symbolsOf(l)
	sign := ""
	if strings.HasPrefix(plain, "-") {
		sign, plain = "-", plain[1:]
	}
	intPart, frac, hasFrac := strings.Cut(plain, ".")
	var b strings.Builder
	b.WriteString(sign)
	for i := range len(intPart) {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(sym.group)
		}
		b.WriteByte(intPart[i])
	}
	if hasFrac {
		b.WriteString(sym.decimal)
		b.WriteString(frac)
	}
	return b.String()
}

// Count is an integer as the language groups it: 12,345 or 12 345.
func Count(l Lang, n int64) string {
	return localizeNumber(l, strconv.FormatInt(n, 10))
}

// Decimal is a number with at most 3 fraction digits, trailing zeros dropped: 4.2 or 4,2.
func Decimal(l Lang, f float64) string {
	return localizeNumber(l, plainFloat(f))
}

// HourCycle picks a 12- or 24-hour clock.
type HourCycle int

const (
	// HourDefault is the language's own: en 12 hours, ru 24
	HourDefault HourCycle = iota
	H12
	H23
)

// Clock is a time of day: en 9:05 PM, ru 21:05.
func Clock(l Lang, hour, minute int, cycle HourCycle) string {
	hour = ((hour % 24) + 24) % 24
	minute = ((minute % 60) + 60) % 60
	if cycle == HourDefault {
		cycle = H23
		if l.rules() == En {
			cycle = H12
		}
	}
	if cycle == H23 {
		return pad2(hour) + ":" + pad2(minute)
	}
	marker := "AM"
	if hour >= 12 {
		marker = "PM"
	}
	h := hour % 12
	if h == 0 {
		h = 12
	}
	return strconv.Itoa(h) + ":" + pad2(minute) + nbsp + marker
}

// Countdown is a time left: 0:09, 4:05, 1:02:03. Negative is 0:00.
func Countdown(seconds int64) string {
	return Elapsed(seconds, seconds)
}

// Elapsed is a position as a player shows it: m:ss, or h:mm:ss when it or the length it is shown beside reaches an
// hour, so the two line up (0:05:10 / 1:20:00).
func Elapsed(seconds, length int64) string {
	if seconds < 0 {
		seconds = 0
	}
	h, m, s := seconds/3600, seconds/60%60, seconds%60
	if h > 0 || length >= 3600 {
		return strconv.FormatInt(h, 10) + ":" + pad2(int(m)) + ":" + pad2(int(s))
	}
	return strconv.FormatInt(seconds/60, 10) + ":" + pad2(int(s))
}

// DurationStyle picks units: short (2 h 5 min) or long (2 hours 5 minutes).
type DurationStyle int

const (
	Short DurationStyle = iota
	Long
)

type unitForms struct {
	short string
	// long by plural category
	long map[PluralCategory]string
}

var units = map[Lang][3]unitForms{
	En: {
		{short: "h", long: map[PluralCategory]string{One: "hour", Other: "hours"}},
		{short: "min", long: map[PluralCategory]string{One: "minute", Other: "minutes"}},
		{short: "s", long: map[PluralCategory]string{One: "second", Other: "seconds"}},
	},
	Ru: {
		{short: "ч", long: map[PluralCategory]string{One: "час", Few: "часа", Many: "часов", Other: "часа"}},
		{short: "мин", long: map[PluralCategory]string{One: "минута", Few: "минуты", Many: "минут", Other: "минуты"}},
		{short: "с", long: map[PluralCategory]string{One: "секунда", Few: "секунды", Many: "секунд", Other: "секунды"}},
	},
}

// Duration is a length in words: an hour or more as hours and minutes (minutes left out at 0), a minute or more as
// minutes alone, less as seconds. Seconds round down. en 2 h 5 min, ru 2 ч 5 мин.
func Duration(l Lang, seconds int64, style DurationStyle) string {
	if seconds < 0 {
		seconds = 0
	}
	u := units[l.rules()]
	part := func(n int64, i int) string {
		s := Count(l, n) + nbsp
		if style == Short {
			return s + u[i].short
		}
		return s + u[i].long[PluralOf(l, strconv.FormatInt(n, 10))]
	}
	h, m := seconds/3600, seconds/60%60
	switch {
	case h > 0 && m > 0:
		return part(h, 0) + " " + part(m, 1)
	case h > 0:
		return part(h, 0)
	case seconds >= 60:
		return part(seconds/60, 1)
	default:
		return part(seconds, 2)
	}
}

var months = map[Lang][12]string{
	En: {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October",
		"November", "December"},
	// genitive, as a day of the month takes it
	Ru: {"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября",
		"декабря"},
}

var weekdays = map[Lang][7]string{
	En: {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	Ru: {"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"},
}

// DayMonth is a date without its year: en November 14, ru 14 ноября.
func DayMonth(l Lang, t time.Time) string {
	m := months[l.rules()][t.Month()-1]
	if l.rules() == Ru {
		return strconv.Itoa(t.Day()) + nbsp + m
	}
	return m + nbsp + strconv.Itoa(t.Day())
}

// Weekday is a day's name: en Saturday, ru суббота (lower case, as ru writes it mid-sentence).
func Weekday(l Lang, t time.Time) string {
	return weekdays[l.rules()][t.Weekday()]
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
