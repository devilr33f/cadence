package skills

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// bounds of a skill, as the table's checks and the admin api hold them
const (
	maxDescription = 200
	maxBody        = 4000
	maxTools       = 24
	maxTriggers    = 48
	maxTrigger     = 64
	maxModes       = 16
)

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
	toolPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	modePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// check reports the first field of s the runtime could not use, by its JSON
// pointer under the request body.
func check(s agent.Skill) (string, bool) {
	switch {
	case !namePattern.MatchString(s.Name):
		return "/path/name", false
	case blank(s.Description) || utf8.RuneCountInString(s.Description) > maxDescription || hidden(s.Description):
		return "/body/description", false
	case blank(s.Body) || utf8.RuneCountInString(s.Body) > maxBody:
		return "/body/body", false
	case len(s.Tools) > maxTools:
		return "/body/tools", false
	case len(s.Triggers) > maxTriggers:
		return "/body/triggers", false
	case len(s.Modes) > maxModes:
		return "/body/modes", false
	}
	for i, t := range s.Tools {
		if !toolPattern.MatchString(t) || slices.Contains(s.Tools[:i], t) {
			return "/body/tools/" + strconv.Itoa(i), false
		}
	}
	for i, t := range s.Triggers {
		if len(words(t)) == 0 || utf8.RuneCountInString(t) > maxTrigger || hidden(t) {
			return "/body/triggers/" + strconv.Itoa(i), false
		}
	}
	for i, m := range s.Modes {
		if !modePattern.MatchString(m) || slices.Contains(s.Modes[:i], m) {
			return "/body/modes/" + strconv.Itoa(i), false
		}
	}
	return "", true
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// hidden reports a control or format character or a line or paragraph
// separator in a one-line field
func hidden(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) })
}
