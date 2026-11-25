package memory

import (
	"regexp"
	"strconv"
	"strings"
)

// the deterministic half of keeping secrets out of learned memories; the
// prompt asks the model for the other half
var (
	secretPrefix = regexp.MustCompile(`\b(?:(?:sk|pk|rk)-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|` +
		`glpat-[A-Za-z0-9_-]{8,}|(?:AKIA|ASIA)[A-Z0-9]{16}\b|xox[abprs]-[A-Za-z0-9-]{8,}|AIza[A-Za-z0-9_-]{30,}|` +
		`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
	emailLike = regexp.MustCompile(`[\p{L}\p{N}._%+-]+@[\p{L}\p{N}-]+(?:\.[\p{L}\p{N}-]+)*\.\p{L}{2,}`)
	// at least 9 digits, at most two separators between two of them
	phoneLike = regexp.MustCompile(`\+?\d(?:[ \-.()]{0,2}\d){8,}`)
	// dot-separated digit groups, whole: only an IPv4 address among them is no phone number
	dottedDigits = regexp.MustCompile(`\d+(?:\.\d+)+`)
	// the words a value follows; whole words, so "ключевой" or "tokenize" are none of them
	secretWord = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(password|passwords|passwd|passcode|api[ _-]?keys?|tokens?|` +
		`пароль|пароля|паролем|паролю|пароли|токен|токена|токеном|токену|токены|ключ|ключа|ключом|ключу|ключи)(?:[^\p{L}\p{N}]|$)`)
	// a value after such a word: "is x", ": x", "= x", "— x" (up to three words
	// between), or a word with a digit right after it
	valueAfter = regexp.MustCompile(`(?i)^(?:(?:\s+[\p{L}\p{N}'’-]+){0,3}?\s*[:=]\s*\S|(?:\s+[\p{L}\p{N}'’-]+){0,3}?\s+(?:is|was|are|это|—|–|-)\s+\S|\s+\S*\p{N})`)
	tokenRun   = regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`)
)

// looksSecret reports text that holds, or seems to hold, a credential or
// contact detail: a known key prefix, an email address, a phone number, a
// password, token or key word followed by a value, or a long run that reads
// as random.
func looksSecret(text string) bool {
	if secretPrefix.MatchString(text) || emailLike.MatchString(text) || phoneLike.MatchString(withoutIPv4(text)) {
		return true
	}
	for _, m := range secretWord.FindAllStringSubmatchIndex(text, -1) {
		if valueAfter.MatchString(text[m[3]:]) {
			return true
		}
	}
	for _, run := range tokenRun.FindAllString(text, -1) {
		if randomRun(run) {
			return true
		}
	}
	return false
}

// withoutIPv4 blanks the IPv4 addresses in text: four groups of 0-255 with
// no further group on either side, so "8.999.123.45.67" stays a number.
func withoutIPv4(text string) string {
	return dottedDigits.ReplaceAllStringFunc(text, func(run string) string {
		groups := strings.Split(run, ".")
		if len(groups) != 4 {
			return run
		}
		for _, g := range groups {
			if n, err := strconv.Atoi(g); err != nil || len(g) > 3 || n > 255 {
				return run
			}
		}
		return " "
	})
}

// randomRun reports a run whose letters and digits change class (lower,
// upper, digit) the way random keys do. Per character, random hex changes
// between letter and digit about 0.47 times and base62 0.27, plus 0.35 case
// changes; words and snake_case names stay near 0, CamelCase at about 0.33
// case changes, which count half.
func randomRun(run string) bool {
	var prev byte
	n, digits, flips, cases := 0, 0, 0, 0
	for i := 0; i < len(run); i++ {
		var c byte
		switch ch := run[i]; {
		case ch >= 'a' && ch <= 'z':
			c = 'l'
		case ch >= 'A' && ch <= 'Z':
			c = 'u'
		case ch >= '0' && ch <= '9':
			c = 'd'
			digits++
		default:
			continue
		}
		n++
		switch {
		case prev == 0 || c == prev:
		case c == 'd' || prev == 'd':
			flips++
		default:
			cases++
		}
		prev = c
	}
	score := (float64(flips) + float64(cases)/2) / float64(max(n, 1))
	// with three digits and three letters a lower score already reads as a key
	if digits >= 3 && n-digits >= 3 {
		return score >= 0.15
	}
	return score >= 0.25
}
