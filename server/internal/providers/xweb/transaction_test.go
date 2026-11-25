package xweb

import (
	"os"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// the ids the reference implementation (XClientTransaction, python) makes for the same page, script, time and xor
// byte: testdata's page and script are synthetic, made the shape x's are
func TestSignerMatchesTheReference(t *testing.T) {
	s, err := newSigner(fixture(t, "home.html"), fixture(t, "ondemand.js"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "552a680c7ae147ae147b09eb851eb851eb809eb851eb851eb80c7ae147ae147b00"; s.animationKey != want {
		t.Fatalf("animation key %s, want %s", s.animationKey, want)
	}
	for _, c := range []struct {
		method, path string
		seconds      int64
		random       byte
		want         string
	}{
		{"GET", "/i/api/graphql/abc/SearchTimeline", 123456789, 7, "B6JKzR8iN7waahQr2dEkfCneGTh1GMwedhBDk9FOO5pbM2e5NicZbvndp+nvvp54WxLKXAAG6ZjmXQ55qRdUw8kKTf0DBA"},
		{"POST", "/1.1/x.json", 99999, 200, "yG2FAtDt+HPVpdvkFh7rs+YR1ve61wPRud+MXB6B9FWU/Kh2+ejWoTYSaCYgcVG3lFdOychkBgeKUP5iRTtuh0jmU1b6yw"},
	} {
		if got := s.id(c.method, c.path, c.seconds, c.random); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestSignerRefusesAPageWithoutItsParts(t *testing.T) {
	home, ondemand := fixture(t, "home.html"), fixture(t, "ondemand.js")
	for name, c := range map[string][2]string{
		"no key":     {"<html></html>", ondemand},
		"no indices": {home, "function(){}"},
		"no frames":  {home[:500], ondemand},
	} {
		if _, err := newSigner(c[0], c[1]); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}
