package devices_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

const seekSchema = `{"type":"object","properties":{"positionMs":{"type":"integer","minimum":0}},"required":["positionMs"]}`

func TestCommandLookup(t *testing.T) {
	e := newEnv(t)
	player := desc("media.player")
	player.Commands = map[string]string{"seek": seekSchema, "pause": `{"type":"object"}`}
	e.sendReport(t, report(desc("audio"), player))
	ctx := context.Background()

	dev, sch, err := e.svc.Command(ctx, strings.ToUpper(e.id), "media.player", "seek")
	if err != nil || dev != e.id {
		t.Fatalf("%q %v", dev, err)
	}
	if path, ok := jsonschemax.Validate(sch, []byte(`{"positionMs":-1}`)); ok || path != "/positionMs" {
		t.Fatalf("schema not the stored one: %q %v", path, ok)
	}
	cases := []struct {
		dev, capability, name string
		want                  error
	}{
		{"00000000-0000-0000-0000-000000000000", "media.player", "seek", devices.ErrNotFound},
		{"nope", "media.player", "seek", devices.ErrNotFound},
		{e.id, "media.url", "seek", devices.ErrUnknownCapability},
		{e.id, "audio", "seek", devices.ErrUnknownCommand},
		{e.id, "media.player", "play", devices.ErrUnknownCommand},
	}
	for _, c := range cases {
		if _, _, err := e.svc.Command(ctx, c.dev, c.capability, c.name); !errors.Is(err, c.want) {
			t.Fatalf("%s %s %s: %v", c.dev, c.capability, c.name, err)
		}
	}

	// the admin detail lists the commands by name with their args schemas, as stored
	code, body := call(t, e.app(), http.MethodGet, "/api/v1/devices/"+e.id, "")
	if code != 200 || !strings.Contains(body, `"id":"audio"`) || !strings.Contains(body, `"commands":[]`) ||
		!strings.Contains(body, `"commands":[{"name":"pause","argsSchema":{"type":"object"}},{"name":"seek","argsSchema":`+seekSchema+`}]`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCanonicalID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if dev, err := e.svc.Canonical(ctx, strings.ToUpper(e.id)); err != nil || dev != e.id {
		t.Fatalf("%q %v", dev, err)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		if _, err := e.svc.Canonical(ctx, id); !errors.Is(err, devices.ErrNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
