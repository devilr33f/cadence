package ytweb

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	countRe = regexp.MustCompile(`(?i)(\d[\d,]*(?:\.\d+)?)\s*(k|m|b|thousand|million|billion)?\b`)
	agoRe   = regexp.MustCompile(`(?i)(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h|days?|d|weeks?|wks?|w|months?|mo|years?|yrs?|y)\s+ago\b`)
	clockRe = regexp.MustCompile(`^(?:(\d{1,3}):)?(\d{1,2}):(\d{2})$`)
)

// ParseCount reads a count as YouTube shows it with hl=en: exact
// ("131,220,260 views"), short ("843K views", "3.8B plays", "39M
// subscribers"), in words ("1.4 million views") or none ("No views", which is
// 0 and true). ok is false where the text holds no count.
func ParseCount(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasPrefix(strings.ToLower(s), "no ") {
		return 0, true
	}
	m := countRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(m[2]) {
	case "k", "thousand":
		f *= 1e3
	case "m", "million":
		f *= 1e6
	case "b", "billion":
		f *= 1e9
	}
	if f > math.MaxInt64/2 {
		return 0, false
	}
	return int64(math.Round(f)), true
}

// parseAgo turns a relative time ("17 hours ago", "Streamed 2 months ago",
// "1y ago") into a time before now; months are 30 days and years 365
func parseAgo(s string, now time.Time) (time.Time, bool) {
	m := agoRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1000 {
		return time.Time{}, false
	}
	var unit time.Duration
	switch u := strings.ToLower(m[2]); {
	case strings.HasPrefix(u, "mo"):
		unit = 30 * 24 * time.Hour
	case strings.HasPrefix(u, "s"):
		unit = time.Second
	case strings.HasPrefix(u, "m"):
		unit = time.Minute
	case strings.HasPrefix(u, "h"):
		unit = time.Hour
	case strings.HasPrefix(u, "d"):
		unit = 24 * time.Hour
	case strings.HasPrefix(u, "w"):
		unit = 7 * 24 * time.Hour
	case strings.HasPrefix(u, "y"):
		unit = 365 * 24 * time.Hour
	}
	return now.Add(-time.Duration(n) * unit), true
}

// parseClock reads "17:44" or "2:34:42" as seconds
func parseClock(s string) (int, bool) {
	m := clockRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	se, _ := strconv.Atoi(m[3])
	return h*3600 + mi*60 + se, true
}

// countBefore reads the count just before word ("…, 1.4 million views, …"),
// so a number in a title beside it is not taken
func countBefore(s, word string) (int64, bool) {
	i := strings.Index(s, word)
	if i < 0 {
		return 0, false
	}
	ms := countRe.FindAllStringIndex(s[:i], -1)
	if len(ms) == 0 {
		return 0, false
	}
	last := ms[len(ms)-1]
	if strings.TrimSpace(s[last[1]:i]) != "" {
		return 0, false
	}
	return ParseCount(s[last[0]:i])
}
