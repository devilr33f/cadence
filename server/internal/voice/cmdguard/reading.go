package cmdguard

import "strings"

// spelling is latin a voice reads as one of the cyrillic letters
type spelling struct{ latin, cyrillic string }

// latin letters a voice reads as these cyrillic ones by their look (lower case, so capitals too)
var lookAlikes = []spelling{
	{"a", "а"}, {"b", "в"}, {"c", "с"}, {"e", "е"}, {"h", "н"}, {"k", "к"},
	{"m", "м"}, {"o", "о"}, {"p", "р"}, {"t", "т"}, {"x", "х"}, {"y", "у"},
}

// latin a russian voice reads by its sound as one of these cyrillic letters, digraphs included; with the look-alikes
// a, m, o and t, which it says as they look, every letter of a stem has one
var sounds = []spelling{
	{"ch", "ч"}, {"tch", "ч"}, {"tsch", "ч"}, {"tsh", "ч"}, {"cz", "ч"},
	{"sh", "шщ"}, {"sch", "шщ"}, {"shch", "шщ"}, {"sz", "шщ"},
	{"kh", "х"}, {"h", "х"}, {"x", "х"}, {"g", "г"}, {"r", "р"}, {"p", "п"}, {"s", "сз"}, {"c", "с"}, {"z", "з"},
	{"u", "у"}, {"ou", "у"}, {"oo", "у"}, {"i", "и"}, {"y", "и"}, {"e", "е"}, {"ye", "е"}, {"ie", "е"},
	{"v", "в"}, {"w", "в"},
}

// reading is, for each cyrillic letter of a word, the spellings a voice reads as it, each as its letters
type reading [][][]rune

func readingOf(word string, bySound bool) reading {
	var r reading
	for _, letter := range word {
		spellings := [][]rune{{letter}}
		spellings = append(spellings, spellingsOf(lookAlikes, letter)...)
		if bySound {
			spellings = append(spellings, spellingsOf(sounds, letter)...)
		}
		r = append(r, spellings)
	}
	return r
}

func spellingsOf(table []spelling, letter rune) [][]rune {
	var out [][]rune
	for _, s := range table {
		if strings.ContainsRune(s.cyrillic, letter) {
			out = append(out, []rune(s.latin))
		}
	}
	return out
}

// longest is the most letters one reading takes
func (r reading) longest() int {
	n := 0
	for _, spellings := range r {
		most := 0
		for _, s := range spellings {
			most = max(most, len(s))
		}
		n += most
	}
	return n
}

// read returns where readings that start at start can end in heard, and whether one runs on past heard's end. All
// readings go on at once as the set of positions they reached, which a stem's few letters keep small, so an ambiguous
// letter never multiplies them: the work stays linear in the letters.
func (r reading) read(heard []rune, start int) (ends []int, open bool) {
	positions := []int{start}
	for _, spellings := range r {
		var next []int
		for _, pos := range positions {
			for _, s := range spellings {
				fit := mismatch(s, heard, pos)
				switch {
				case fit < 0:
					next = with(next, pos+len(s))
				case pos+fit == len(heard):
					open = true
				}
			}
		}
		positions = next
		if len(positions) == 0 {
			break
		}
	}
	return positions, open
}

// endsAt reports whether a reading from start ends exactly at end
func (r reading) endsAt(heard []rune, start, end int) bool {
	ends, _ := r.read(heard, start)
	for _, e := range ends {
		if e == end {
			return true
		}
	}
	return false
}

// mismatch is the index of s's first letter that heard at pos does not hold, -1 when all fit
func mismatch(s, heard []rune, pos int) int {
	for i, l := range s {
		if pos+i >= len(heard) || heard[pos+i] != l {
			return i
		}
	}
	return -1
}

func with(set []int, v int) []int {
	for _, x := range set {
		if x == v {
			return set
		}
	}
	return append(set, v)
}
