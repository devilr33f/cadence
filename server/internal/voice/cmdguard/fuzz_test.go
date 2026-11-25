package cmdguard_test

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

// the oracle: its own spelling tables (from the v1 reference, not the package's), every spelling of every stem
// enumerated, and a plain substring search in the text as a voice hears it

var oracleLooks = map[string]string{
	"a": "а", "b": "в", "c": "с", "e": "е", "h": "н", "k": "к", "m": "м", "o": "о", "p": "р", "t": "т", "x": "х", "y": "у",
}

var oracleSounds = map[string]string{
	"ch": "ч", "tch": "ч", "tsch": "ч", "tsh": "ч", "cz": "ч",
	"sh": "шщ", "sch": "шщ", "shch": "шщ", "sz": "шщ",
	"kh": "х", "h": "х", "x": "х", "g": "г", "r": "р", "p": "п", "s": "сз", "c": "с", "z": "з",
	"u": "у", "ou": "у", "oo": "у", "i": "и", "y": "и", "e": "е", "ye": "е", "ie": "е", "v": "в", "w": "в",
}

var oracleCarons = map[rune]rune{'č': 'ч', 'š': 'ш', 'ž': 'ж'}

// spellingsOf is every way to write stem a voice reads as it
func spellingsOf(stem string, bySound bool) []string {
	out := []string{""}
	for _, letter := range stem {
		alts := map[string]bool{string(letter): true}
		for latin, cyr := range oracleLooks {
			if strings.ContainsRune(cyr, letter) {
				alts[latin] = true
			}
		}
		if bySound {
			for latin, cyr := range oracleSounds {
				if strings.ContainsRune(cyr, letter) {
					alts[latin] = true
				}
			}
		}
		var next []string
		for _, prefix := range out {
			for alt := range alts {
				next = append(next, prefix+alt)
			}
		}
		out = next
	}
	return out
}

type oracle struct{ stems map[string][]string }

func newOracle(lang string) oracle {
	o := oracle{stems: map[string][]string{}}
	for _, s := range commandStems {
		o.stems[s] = spellingsOf(s, lang == "ru" || s == "громче")
	}
	return o
}

var (
	ruOracle = newOracle("ru")
	enOracle = newOracle("en")
	raises   = spellingsOf("громче", true)
)

func inWordRune(r rune) bool { return unicode.In(r, unicode.L, unicode.M, unicode.Cf) }

func joinerRune(r rune) bool { return strings.ContainsRune("+-\u2010\u2011'\u2019", r) }

// heard is text as a voice reads it: composed, through marks and format characters and through a joiner before a
// letter, in lower case, a caron letter as its cyrillic one and any other letter without its marks
func heard(text string) []rune {
	rs := []rune(norm.NFC.String(text))
	var out []rune
	for i, r := range rs {
		if unicode.In(r, unicode.M, unicode.Cf) || (joinerRune(r) && i+1 < len(rs) && inWordRune(rs[i+1])) {
			continue
		}
		lower := unicode.ToLower(r)
		if c, ok := oracleCarons[lower]; ok {
			out = append(out, c)
			continue
		}
		for _, d := range norm.NFD.String(string(lower)) {
			if !unicode.Is(unicode.M, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// found names the stems a voice reads in text
func (o oracle) found(text string) []string {
	h := string(heard(text))
	var out []string
	for stem, spellings := range o.stems {
		for _, sp := range spellings {
			if strings.Contains(h, sp) {
				out = append(out, stem)
				break
			}
		}
	}
	return out
}

// mayBeginRaise reports whether the end of piece's trailing word may begin a raise that a later piece completes
func mayBeginRaise(piece string) bool {
	end := len(piece)
	for end > 0 {
		r, n := utf8.DecodeLastRuneInString(piece[:end])
		if !inWordRune(r) && !joinerRune(r) {
			break
		}
		end -= n
	}
	h := heard(piece[end:])
	// a joiner the piece ends in is read through once a letter follows it in the next piece
	for len(h) > 0 && joinerRune(h[len(h)-1]) {
		h = h[:len(h)-1]
	}
	for i := range h {
		tail := string(h[i:])
		for _, sp := range raises {
			if len(tail) < len(sp) && strings.HasPrefix(sp, tail) {
				return true
			}
		}
	}
	return false
}

// the generator's pieces: stems and their look-alike and sound-alike spellings, fragments of them, other words,
// joiners, marks, format characters, emoji, digits and punctuation
var fragments = []string{
	"стоп", "Стоп", "СТОП", "хватит", "пауза", "тише", "потише", "громче", "погромче", "стстопоп",
	"CТОП", "cтоп", "CТOП", "xватит", "XBATИT", "Tише", "гpомче", "grомче",
	"gromche", "pogromcze", "gromtsche", "tische", "tisze", "khvatit", "paouza", "stop", "Stop", "pause", "gromče", "tiše",
	"гро", "мче", "гром", "че", "сто", "п", "ти", "ше", "хва", "тит", "па", "уз", "g", "gr", "ro", "m", "ch", "c", "s", "z", "e",
	"привет", "музыку", "пирог", "ром", "замок", "the", "dog", "Bluetooth", "rock'n'roll", "ёлка",
	"-", "+", "'", "\u2019", "\u2010", "\u2011", stress, diaeresis, caron, softHyphen, "\u200D", "\u200B",
	" ", " ", " ", "  ", "\n", ".", ",", "!", "?", "…", "😀", "👍🏽", "3", "45",
}

// stem-free: no letter of any spelling of any stem, so nothing here can read as one
var freeFragments = []string{
	"бд", "жк", "клн", "фц", "щъ", "ыьэ", "юя", "dfj", "lnq", "Бджл", "QLD",
	"-", "+", "'", "\u2019", stress, diaeresis, softHyphen, "\u200D", " ", " ", "\n", ".", ",", "!", "😀", "3",
}

func generate(r *rand.Rand, from []string, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(from[r.IntN(len(from))])
	}
	return b.String()
}

// chunksOf cuts text at up to four random points as the chunker would: a cut at whitespace drops it and is no run,
// any other cut is inside a run
func chunksOf(r *rand.Rand, text string) []cmdguard.Chunk {
	var cuts []int
	for range r.IntN(5) {
		cuts = append(cuts, r.IntN(len(text)+1))
	}
	var out []cmdguard.Chunk
	last := 0
	for _, c := range sortedCuts(cuts, text) {
		if c <= last {
			continue
		}
		piece := text[last:c]
		next, _ := utf8.DecodeRuneInString(text[c:])
		prev, _ := utf8.DecodeLastRuneInString(piece)
		atSpace := unicode.IsSpace(next) || unicode.IsSpace(prev)
		out = append(out, cmdguard.Chunk{Text: strings.TrimSpace(piece), InRun: !atSpace})
		last = c
	}
	return append(out, cmdguard.Chunk{Text: strings.TrimSpace(text[last:])})
}

// sortedCuts puts the cuts in order, each on a rune boundary
func sortedCuts(cuts []int, text string) []int {
	for i := range cuts {
		for cuts[i] > 0 && cuts[i] < len(text) && !utf8.RuneStart(text[cuts[i]]) {
			cuts[i]--
		}
	}
	for i := 1; i < len(cuts); i++ {
		for j := i; j > 0 && cuts[j] < cuts[j-1]; j-- {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
		}
	}
	return cuts
}

func streamed(lang string, chunks []cmdguard.Chunk) string {
	s := cmdguard.NewStream(lang)
	var b strings.Builder
	for _, c := range chunks {
		for _, out := range s.Push(c) {
			b.WriteString(out)
		}
	}
	for _, out := range s.Flush() {
		b.WriteString(out)
	}
	return b.String()
}

func FuzzGuard(f *testing.F) {
	for seed := range uint64(400) {
		f.Add("", seed)
	}
	for _, text := range []string{
		"Сделай громче музыку.", "Ну, гро-мче, grom'che и ti\u2011she!", "Это пирог ром и чай.", "Say gro-mche now.",
		"Стоп, хватит, пауза.", "a стстопоп б", "Скажи gromče и tiše.", "Ну, tis" + caron + "e!", "Песня Stop от Spice Girls.",
	} {
		f.Add(text, uint64(len(text)))
	}
	f.Fuzz(func(t *testing.T, text string, seed uint64) {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		if text == "" {
			text = generate(r, fragments, 1+r.IntN(24))
		}
		for _, lang := range []string{"ru", "en"} {
			o := ruOracle
			if lang == "en" {
				o = enOracle
			}
			// (b) a rewrite leaves nothing to rewrite
			once := cmdguard.Rewrite(text, lang)
			if twice := cmdguard.Rewrite(once, lang); twice != once {
				t.Fatalf("%s: not idempotent: %q -> %q -> %q", lang, text, once, twice)
			}
			// (a) no stem reaches tts, whatever the cuts; in en a cut of the accepted gap's shape may leave a stem,
			// never a raise
			chunks := chunksOf(r, text)
			out := streamed(lang, chunks)
			left := o.found(out)
			if lang == "en" {
				left = outsideGaps(o, lang, chunks, left)
			}
			if len(left) != 0 {
				t.Fatalf("%s: %q cut %+v spoke %q with %q", lang, text, chunks, out, left)
			}
			// (d) cut at whitespace only, the stream says what a rewrite of the whole says, unless a raise may read
			// across a cut: the two are then one word (fail-closed)
			pieces := strings.Fields(text)
			whole := strings.Join(pieces, " ")
			var ws []cmdguard.Chunk
			across := false
			for i, p := range pieces {
				ws = append(ws, cmdguard.Chunk{Text: p})
				across = across || (i < len(pieces)-1 && mayBeginRaise(p))
			}
			if got, want := streamed(lang, ws), cmdguard.Rewrite(whole, lang); !across && got != want {
				t.Fatalf("%s: %q cut at whitespace spoke %q, want %q", lang, whole, got, want)
			}
		}
		// (c) text from a stem-free alphabet passes byte for byte
		free := generate(r, freeFragments, 1+r.IntN(16))
		for _, lang := range []string{"ru", "en"} {
			if got := cmdguard.Rewrite(free, lang); got != free {
				t.Fatalf("%s: stem-free %q came back %q", lang, free, got)
			}
			if pieces := strings.Fields(free); len(pieces) > 0 {
				var ws []cmdguard.Chunk
				for _, p := range pieces {
					ws = append(ws, cmdguard.Chunk{Text: p})
				}
				if got, want := streamed(lang, ws), strings.Join(pieces, " "); got != want {
					t.Fatalf("%s: stem-free stream %q came back %q", lang, want, got)
				}
			}
		}
	})
}

// gapCut reports a cut of the accepted gap's own shape after chunk i: cut inside its run, its trailing word latin and
// ending in a look-alike, and the next chunk's first word holding the cyrillic rest (C | ТОП, XB | ATИT: п, и and ш
// have no look-alike, so a stem other than громче keeps a cyrillic letter there); marks, format characters and joiners
// at the cut are read through, as a voice reads them
func gapCut(chunks []cmdguard.Chunk, i int) bool {
	if !chunks[i].InRun {
		return false
	}
	tail, head := trailingWord(chunks[i].Text), leadingWord(chunks[i+1].Text)
	if len(tail) == 0 {
		return false
	}
	_, look := oracleLooks[string(tail[len(tail)-1])]
	return look && !slices.ContainsFunc(tail, cyrillic) && slices.ContainsFunc(head, cyrillic)
}

// outsideGaps keeps the stems left that no gap-shaped cut forms: streamed again with each such cut at a space, a stem
// that spans one is gone, and only those are forgiven; a raise never is
func outsideGaps(o oracle, lang string, chunks []cmdguard.Chunk, left []string) []string {
	split := slices.Clone(chunks)
	gaps := false
	for i := 0; i+1 < len(chunks); i++ {
		if gapCut(chunks, i) {
			split[i].InRun, gaps = false, true
		}
	}
	if !gaps {
		return left
	}
	still := o.found(streamed(lang, split))
	var kept []string
	for _, stem := range left {
		if stem == "громче" || slices.Contains(still, stem) {
			kept = append(kept, stem)
		}
	}
	return kept
}

func cyrillic(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }

// trailingWord is the letters a voice hears at the end of piece, through a joiner there
func trailingWord(piece string) []rune {
	h := heard(piece)
	end := len(h)
	for end > 0 && joinerRune(h[end-1]) {
		end--
	}
	start := end
	for start > 0 && unicode.IsLetter(h[start-1]) {
		start--
	}
	return h[start:end]
}

// leadingWord is the letters a voice hears at the start of piece
func leadingWord(piece string) []rune {
	h := heard(piece)
	end := 0
	for end < len(h) && unicode.IsLetter(h[end]) {
		end++
	}
	return h[:end]
}
