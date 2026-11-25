package subscriptions

import (
	"strconv"
	"strings"
	"unicode"
)

// what a spoken update quotes of the outside text, in characters
const (
	maxSaidText  = 200
	maxSaidTitle = 80
)

// noun is how the templates name a kind of thing, in Russian with its
// gender (0 masculine, 1 feminine, 2 neuter) and plural forms, and in English.
type noun struct {
	ru            [3]string // one, few (2-4), many
	gender        int
	enOne, enMany string
}

var nouns = map[string]noun{
	"thread": {ru: [3]string{"тред", "треда", "тредов"}, gender: 0, enOne: "thread", enMany: "threads"},
	"task":   {ru: [3]string{"задача", "задачи", "задач"}, gender: 1, enOne: "task", enMany: "tasks"},
	"":       {ru: [3]string{"обновление", "обновления", "обновлений"}, gender: 2, enOne: "update", enMany: "updates"},
}

// verbs per event: Russian singular by gender, then plural; English
// singular, plural
var verbs = map[string]struct {
	ru     [4]string
	en     [2]string
	quotes bool // a single update says its text after it
}{
	EventStopped:  {ru: [4]string{"остановился", "остановилась", "остановилось", "остановились"}, en: [2]string{"stopped", "stopped"}},
	EventQuestion: {ru: [4]string{"спрашивает", "спрашивает", "спрашивает", "спрашивают"}, en: [2]string{"asks", "ask"}, quotes: true},
	EventApproval: {ru: [4]string{"просит разрешения", "просит разрешения", "просит разрешения", "просят разрешения"}, en: [2]string{"asks for approval", "ask for approval"}, quotes: true},
	EventError:    {ru: [4]string{"сообщает об ошибке", "сообщает об ошибке", "сообщает об ошибке", "сообщают об ошибке"}, en: [2]string{"hit an error", "hit errors"}},
	EventDone:     {ru: [4]string{"завершён", "завершена", "завершено", "завершены"}, en: [2]string{"is done", "are done"}},
	EventChanged:  {ru: [4]string{"обновился", "обновилась", "обновилось", "обновились"}, en: [2]string{"changed", "changed"}},
}

var (
	ruCounts = [][3]string{
		{"один", "одна", "одно"}, {"два", "две", "два"}, {"три", "три", "три"}, {"четыре", "четыре", "четыре"},
		{"пять", "пять", "пять"}, {"шесть", "шесть", "шесть"}, {"семь", "семь", "семь"}, {"восемь", "восемь", "восемь"},
		{"девять", "девять", "девять"}, {"десять", "десять", "десять"},
	}
	enCounts = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
)

// compose is the one sentence a batch of updates is spoken as, from fixed
// templates in the device's language (ru, else en): the outside words (a
// title, a question's text) go to speech alone, never to a model. One update
// says what it is and, for a question, its text; several are counted by
// event, an event of one naming its title.
func compose(items []InboxItem, lang string) string {
	if len(items) == 0 {
		return ""
	}
	ru := lang == "ru"
	kind := items[0].Kind
	for _, it := range items[1:] {
		if it.Kind != kind {
			kind = ""
			break
		}
	}
	n, ok := nouns[kind]
	if !ok {
		n = nouns[""]
	}
	if len(items) == 1 {
		return single(items[0], n, ru)
	}
	var parts []string
	for _, ev := range Events {
		var group []InboxItem
		for _, it := range items {
			if it.Event == ev {
				group = append(group, it)
			}
		}
		switch {
		case len(group) == 1:
			parts = append(parts, named(group[0], n, ru))
		case len(group) > 1:
			parts = append(parts, counted(len(group), ev, n, ru))
		}
	}
	return sentence(strings.Join(parts, ", "))
}

// single is one update: its noun, title and verb, a question's text after.
func single(it InboxItem, n noun, ru bool) string {
	s := named(it, n, ru)
	if v := verbs[it.Event]; v.quotes && it.Question != nil {
		if t := clip(it.Question.Text, maxSaidText); t != "" {
			s += ": " + t
		}
	} else if v.quotes {
		if t := clip(it.Text, maxSaidText); t != "" {
			s += ": " + t
		}
	}
	return sentence(s)
}

func named(it InboxItem, n noun, ru bool) string {
	v := verbs[it.Event]
	title := clip(it.Title, maxSaidTitle)
	if ru {
		s := n.ru[0]
		if title != "" {
			s += " „" + title + "“"
		}
		return s + " " + v.ru[n.gender]
	}
	s := n.enOne
	if title != "" {
		s += " “" + title + "”"
	}
	return s + " " + v.en[0]
}

func counted(count int, ev string, n noun, ru bool) string {
	v := verbs[ev]
	if ru {
		c := strconv.Itoa(count)
		if count <= len(ruCounts) {
			c = ruCounts[count-1][n.gender]
		}
		return c + " " + n.ru[ruPlural(count)] + " " + v.ru[3]
	}
	c := strconv.Itoa(count)
	if count <= len(enCounts) {
		c = enCounts[count-1]
	}
	return c + " " + n.enMany + " " + v.en[1]
}

// ruPlural picks the Russian form for a count: 0 one, 1 few, 2 many.
func ruPlural(n int) int {
	switch {
	case n%10 == 1 && n%100 != 11:
		return 0
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return 1
	}
	return 2
}

// sentence capitalizes the first letter and ends with a full stop.
func sentence(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	r[0] = unicode.ToUpper(r[0])
	out := string(r)
	if !strings.HasSuffix(out, ".") && !strings.HasSuffix(out, "?") && !strings.HasSuffix(out, "!") {
		out += "."
	}
	return out
}

// clip makes outside text one line of plain characters, at most limit.
func clip(s string, limit int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if len(r) > limit {
		return strings.TrimSpace(string(r[:limit])) + "…"
	}
	return s
}
