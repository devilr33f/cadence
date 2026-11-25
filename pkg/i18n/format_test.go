package i18n

import (
	"encoding/json"
	"os"
	"testing"
	"unicode/utf8"
)

// the cases screen/i18n's tests read too, so the two formatters agree
type cases struct {
	Messages []struct {
		Lang   Lang           `json:"lang"`
		Msg    string         `json:"msg"`
		Args   map[string]any `json:"args"`
		Pseudo bool           `json:"pseudo"`
		Want   string         `json:"want"`
		Error  bool           `json:"error"`
	} `json:"messages"`
	Invalid []string `json:"invalid"`
	Helpers []struct {
		Fn   string    `json:"fn"`
		Lang Lang      `json:"lang"`
		Args []float64 `json:"args"`
		Want string    `json:"want"`
	} `json:"helpers"`
}

func loadCases(t *testing.T) cases {
	t.Helper()
	data, err := os.ReadFile("../../i18n/testdata/format.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSharedMessageCases(t *testing.T) {
	for _, c := range loadCases(t).Messages {
		m, err := Parse(c.Msg)
		if err != nil {
			t.Errorf("%s: %v", c.Msg, err)
			continue
		}
		lang := c.Lang
		if c.Pseudo {
			m, lang = Pseudolocalize(m), Pseudo
		}
		got, err := m.Format(lang, c.Args)
		if got != c.Want || (err != nil) != c.Error {
			t.Errorf("%s %s %v: got %q (err %v), want %q (error %v)", c.Lang, c.Msg, c.Args, got, err, c.Want, c.Error)
		}
	}
}

func TestSharedInvalidCases(t *testing.T) {
	for _, s := range loadCases(t).Invalid {
		m, err := Parse(s)
		if err == nil {
			_, err = m.Args()
		}
		if err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestSharedHelperCases(t *testing.T) {
	for _, c := range loadCases(t).Helpers {
		a := c.Args
		var got string
		switch c.Fn {
		case "count":
			got = Count(c.Lang, int64(a[0]))
		case "decimal":
			got = Decimal(c.Lang, a[0])
		case "clock":
			got = Clock(c.Lang, int(a[0]), int(a[1]), HourDefault)
		case "clock24":
			got = Clock(c.Lang, int(a[0]), int(a[1]), H23)
		case "clock12":
			got = Clock(c.Lang, int(a[0]), int(a[1]), H12)
		case "countdown":
			got = Countdown(int64(a[0]))
		case "elapsed":
			got = Elapsed(int64(a[0]), int64(a[1]))
		case "duration":
			got = Duration(c.Lang, int64(a[0]), Short)
		case "duration_long":
			got = Duration(c.Lang, int64(a[0]), Long)
		default:
			t.Fatalf("unknown helper %s", c.Fn)
		}
		if got != c.Want {
			t.Errorf("%s %s %v: got %q, want %q", c.Fn, c.Lang, a, got, c.Want)
		}
	}
}

func TestPluralRules(t *testing.T) {
	ru := map[string]PluralCategory{"0": Many, "1": One, "2": Few, "3": Few, "4": Few, "5": Many, "10": Many,
		"11": Many, "12": Many, "13": Many, "14": Many, "15": Many, "20": Many, "21": One, "22": Few, "24": Few,
		"25": Many, "100": Many, "101": One, "111": Many, "112": Many, "122": Few, "1001": One, "-1": One,
		"1.5": Other, "0.1": Other, "123456789012345678901": One}
	for n, want := range ru {
		if got := PluralOf(Ru, n); got != want {
			t.Errorf("ru %s: %s, want %s", n, got, want)
		}
	}
	en := map[string]PluralCategory{"0": Other, "1": One, "2": Other, "11": Other, "21": Other, "1.5": Other,
		"-1": One}
	for n, want := range en {
		if got := PluralOf(En, n); got != want {
			t.Errorf("en %s: %s, want %s", n, got, want)
		}
		if got := PluralOf(Pseudo, n); got != want {
			t.Errorf("xx %s: %s, want en's %s", n, got, want)
		}
	}
}

func TestPseudoTables(t *testing.T) {
	if utf8.RuneCountInString(pseudoLower) != 26 || utf8.RuneCountInString(pseudoUpper) != 26 {
		t.Fatal("the pseudo tables need 26 letters each")
	}
}

func TestPseudoIsLonger(t *testing.T) {
	for _, s := range []string{"Settings", "Couldn't load", "Press ↓ to choose", "No connection to Cadence"} {
		m, _ := Parse(s)
		got, _ := Pseudolocalize(m).Format(Pseudo, nil)
		if n, want := utf8.RuneCountInString(got), utf8.RuneCountInString(s)*14/10; n < want {
			t.Errorf("%q -> %q: %d runes, want at least %d", s, got, n, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]Lang{"": En, "en": En, "ru": Ru, "RU": Ru, "ru-RU": Ru, "ru_RU": Ru, "de": En,
		"xx": Pseudo, " ru ": Ru} {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
