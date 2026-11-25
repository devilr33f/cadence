package skills

import "strings"

// DisplayName is a skill's localized title for its quiet step, from the same
// built-in catalog the turn loads. Owner skills without a title use their id.
func DisplayName(name, lang string) string {
	for _, s := range Builtins() {
		if s.Name != name {
			continue
		}
		if base, _, _ := strings.Cut(strings.ToLower(lang), "-"); base == "ru" {
			if s.TitleRU != "" {
				return s.TitleRU
			}
		} else if s.TitleEN != "" {
			return s.TitleEN
		}
		if s.TitleEN != "" {
			return s.TitleEN
		}
		if s.TitleRU != "" {
			return s.TitleRU
		}
	}
	return name
}
