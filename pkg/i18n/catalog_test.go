package i18n

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// the completeness check over the repo's catalogs: every key in en and ru, the same arguments, valid plural forms
func TestCatalogsComplete(t *testing.T) {
	for _, area := range []string{"screen", "server"} {
		c, err := Load(os.DirFS("../../i18n"), area)
		if err != nil {
			t.Fatal(err)
		}
		for _, err := range c.Check() {
			t.Error(err)
		}
		if len(c.Keys()) == 0 {
			t.Errorf("%s: no keys", area)
		}
	}
}

func TestCheckFindsGaps(t *testing.T) {
	fsys := fstest.MapFS{
		"a/en.json": {Data: []byte(`{"x.one":"One","x.two":"{n, plural, one {# a} other {# b}}","x.arg":"{a}",
			"Bad":"x","x.extra_en":"{n, plural, one {#} few {#} other {#}}"}`)},
		"a/ru.json": {Data: []byte(`{"x.two":"{n, plural, one {# а} other {# б}}","x.arg":"{b}","Bad":"x",
			"x.extra_en":"{n, plural, one {#} few {#} many {#} other {#}}","x.only_ru":"y"}`)},
	}
	c, err := Load(fsys, "a")
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, err := range c.Check() {
		all = append(all, err.Error())
	}
	got := strings.Join(all, "\n")
	for _, want := range []string{"missing x.one", "x.two: plural cases", "x.arg: arguments", `bad key "Bad"`,
		"a/en: x.extra_en: plural cases", "x.only_ru is not in en"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
}

func TestLoadRefuses(t *testing.T) {
	for name, data := range map[string]string{
		"nested":   `{"a.b":{"c":"d"}}`,
		"repeated": `{"a.b":"x","a.b":"y"}`,
		"broken":   `{"a.b":"{oops"}`,
		"array":    `["a"]`,
		"trailing": `{"a.b":"x"} {}`,
	} {
		fsys := fstest.MapFS{"a/en.json": {Data: []byte(data)}, "a/ru.json": {Data: []byte(`{}`)}}
		if _, err := Load(fsys, "a"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestRenderFallsBack(t *testing.T) {
	fsys := fstest.MapFS{
		"a/en.json": {Data: []byte(`{"k.both":"Both {n}","k.en_only":"English"}`)},
		"a/ru.json": {Data: []byte(`{"k.both":"Оба {n}"}`)},
	}
	c, err := Load(fsys, "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lang      Lang
		key, want string
	}{
		{Ru, "k.both", "Оба 3"}, {Ru, "k.en_only", "English"}, {Ru, "k.none", "k.none"},
		{Pseudo, "k.both", "[Ɓóţĥ 3]"}, {"de", "k.both", "Both 3"},
	} {
		if got := c.Render(tc.lang, tc.key, Args{"n": 3}); got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.lang, tc.key, got, tc.want)
		}
	}
}
