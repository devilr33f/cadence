package servertext

import "testing"

func TestRender(t *testing.T) {
	for _, tc := range []struct {
		lang, want string
		got        func(string) string
	}{
		{"ru", "Ищу в сети: монолог фармацевта", func(l string) string { return Render(l, StepWebSearch("монолог фармацевта")) }},
		{"en", "Reading 4 sources", func(l string) string { return Render(l, StepReadSources(4)) }},
		{"ru", "Читаю 4 источника", func(l string) string { return Render(l, StepReadSources(4)) }},
		{"ru-RU", "Читаю 5 источников", func(l string) string { return Render(l, StepReadSources(5)) }},
		{"de", "Saved", func(l string) string { return Render(l, ToastSaved()) }},
	} {
		if got := tc.got(tc.lang); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.lang, got, tc.want)
		}
	}
}
