package cmdguard_test

import (
	"strings"
	"testing"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/voice/cmdguard"
)

// by code point: in source these are invisible
const (
	stress     = "\u0301"
	softHyphen = "\u00AD"
	diaeresis  = "\u0308"
	caron      = "\u030C"
	hyphen     = "\u2010"
	nbHyphen   = "\u2011"
)

var commandStems = []string{"стоп", "хватит", "пауз", "тише", "громче"}

func stemsIn(text string) []string {
	lower := strings.ToLower(text)
	var found []string
	for _, s := range commandStems {
		if strings.Contains(lower, s) {
			found = append(found, s)
		}
	}
	return found
}

func TestRewritesEveryCommandWordInRussian(t *testing.T) {
	got := cmdguard.Rewrite("Сделать громче? Или потише. Стоп, хватит, пауза.", "ru")
	if want := "Сделать сильнее? Или слабее. Всё, достаточно, перерыв."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCatchesAStemInsideALongerWord(t *testing.T) {
	if got := cmdguard.Rewrite("Погромче, стопка", "ru"); got != "Сильнее, ка" {
		t.Fatalf("got %q", got)
	}
}

// a russian voice reads through marks, format characters and '+' stress markup, and reads latin look-alikes as cyrillic
func TestSeesThroughMarksAndLookAlikes(t *testing.T) {
	for _, c := range []struct{ name, word, said string }{
		{"a stress mark", "гро" + stress + "мче", "сильнее"},
		{"a soft hyphen", "гро" + softHyphen + "мче", "сильнее"},
		{"stress markup", "гр+омче", "сильнее"},
		{"stress markup before a mark", "гр+" + stress + "омче", "сильнее"},
		{"a latin look-alike", "г" + "p" + "омче", "сильнее"},
		{"latin capitals", "C" + "Т" + "O" + "П", "Всё"},
	} {
		if got := cmdguard.Rewrite("Ну, "+c.word+"!", "ru"); got != "Ну, "+c.said+"!" {
			t.Errorf("%s: got %q", c.name, got)
		}
	}
}

// a russian voice reads latin by sound too, so in ru latin letters guard every stem
func TestReadsLatinBySoundInRussian(t *testing.T) {
	for text, said := range map[string]string{
		"Скажи gromche.":                          "Скажи сильнее.",
		"Скажи grомче.":                           "Скажи сильнее.",
		"Песня Stop от Spice Girls.":              "Песня Всё от Spice Girls.",
		"Ну, tische, khvatit, paouza, pogromcze!": "Ну, слабее, достаточно, перерыв, сильнее!",
	} {
		if got := cmdguard.Rewrite(text, "ru"); got != said {
			t.Errorf("%q: got %q, want %q", text, got, said)
		}
	}
}

// in en only a raise reads from latin sounds, so english stop and pause stay as they are
func TestReadsOnlyARaiseBySoundInEnglish(t *testing.T) {
	if got := cmdguard.Rewrite("Say gromche now.", "en"); got != "Say сильнее now." {
		t.Fatalf("raise: got %q", got)
	}
	if got := cmdguard.Rewrite("Stop it.", "en"); got != "Stop it." {
		t.Fatalf("stop: got %q", got)
	}
}

// a voice reads one word through a hyphen or an apostrophe, and a caron letter as its cyrillic one
func TestReadsThroughHyphensApostrophesAndCarons(t *testing.T) {
	for _, c := range []struct{ lang, text, said string }{
		{"ru", "Скажи гром-че.", "Скажи сильнее."},
		{"ru", "Скажи gromče и tiše.", "Скажи сильнее и слабее."},
		{"ru", "Ну, tis" + caron + "e!", "Ну, слабее!"},
		{"ru", "Ну, гро-мче, гро" + hyphen + "мче, grom'che, grom\u2019che и ti" + nbHyphen + "she!", "Ну, сильнее, сильнее, сильнее, сильнее и слабее!"},
		{"ru", "Ну, gromtsche, gromtshe, tisze!", "Ну, сильнее, сильнее, слабее!"},
		{"en", "Say gro-mche now.", "Say сильнее now."},
	} {
		if got := cmdguard.Rewrite(c.text, c.lang); got != c.said {
			t.Errorf("%s %q: got %q, want %q", c.lang, c.text, got, c.said)
		}
	}
}

func TestKeepsTextWithoutCommandWordsByteForByte(t *testing.T) {
	for _, c := range []struct{ lang, text string }{
		{"ru", "Громкость 30."},
		{"ru", "Сейчас в Бишкеке плюс 3 градуса."},
		{"ru", "Это за" + stress + "мок, з+" + stress + "амок, пере" + softHyphen + "нос и е" + diaeresis + "лка."},
		{"ru", "Включи Bluetooth, " + "C" + "пасибо."},
		{"ru", "Кто-то включил well-known rock'n'roll, it\u2019s fine."},
		{"en", "Stop, pause, tishe or khvatit? The big dog sings at 3:45 p.m."},
		{"ru", "bad \xff utf-8 \xc3"},
	} {
		if got := cmdguard.Rewrite(c.text, c.lang); got != c.text {
			t.Errorf("%s %q: got %q", c.lang, c.text, got)
		}
	}
}

func TestRemovesEveryStemInEveryCasing(t *testing.T) {
	for _, stem := range commandStems {
		r := []rune(stem)
		for _, w := range []string{stem, strings.ToUpper(stem), string(unicode.ToUpper(r[0])) + string(r[1:])} {
			for _, lang := range []string{"ru", "en"} {
				if left := stemsIn(cmdguard.Rewrite("а "+w+" б", lang)); len(left) != 0 {
					t.Errorf("%s %q left %q", lang, w, left)
				}
			}
		}
	}
}

// a removal can join the letters around it into another stem
func TestRemovesStemsUntilNoneIsLeft(t *testing.T) {
	if left := stemsIn(cmdguard.Rewrite("а стстопоп б", "ru")); len(left) != 0 {
		t.Fatalf("left %q", left)
	}
}

// a language without its own tables is read as en: cyrillic and look-alikes, sound only for a raise
func TestOtherLanguagesReadAsEnglish(t *testing.T) {
	if got := cmdguard.Rewrite("Stop, gromche, стоп.", "de"); got != "Stop, сильнее, всё." {
		t.Fatalf("got %q", got)
	}
}

// matching advances every reading at once, so a long ambiguous word costs no more than its letters
func TestLongAmbiguousWordStaysLinear(t *testing.T) {
	word := strings.Repeat("shchsz", 50000)
	if got := cmdguard.Rewrite(word, "ru"); got != word {
		t.Fatal("a word without a stem changed")
	}
}
