package devices_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestSetDeviceRoom(t *testing.T) {
	e := newEnv(t)
	app := e.app()
	path := "/api/v1/devices/" + e.id + "/room"
	if code, body := call(t, app, http.MethodPut, path, `{"room":"  Гостиная "}`); code != 200 || !strings.Contains(body, `"room":"Гостиная"`) {
		t.Fatalf("%d %s", code, body)
	}
	if r := e.detail(t).Room; r != "Гостиная" {
		t.Fatalf("stored %q", r)
	}
	if code, body := call(t, app, http.MethodPut, path, `{"room":""}`); code != 200 || !strings.Contains(body, `"room":""`) {
		t.Fatalf("none: %d %s", code, body)
	}
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{path, `{"room":"a\nb"}`, 422},
		{path, `{"room":"` + strings.Repeat("ё", 65) + `"}`, 422},
		{"/api/v1/devices/00000000-0000-0000-0000-000000000000/room", `{"room":"x"}`, 404},
		{"/api/v1/devices/nope/room", `{"room":"x"}`, 404},
	} {
		if code, body := call(t, app, http.MethodPut, c.path, c.body); code != c.code {
			t.Errorf("%s %s: %d %s", c.path, c.body, code, body)
		}
	}
}
