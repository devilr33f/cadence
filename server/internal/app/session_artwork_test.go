package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// a device gets its own session's cover alone (a group member's is its source's), as the server sees it now
func TestSessionArtworkServesTheCallersOwnCover(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	p := images.New(images.Config{})
	sessions := map[string]media.MediaSession{
		"member": {Source: "group", Metadata: media.MediaMetadata{Title: "Town", Artist: "Bokka", ArtworkHash: "h"}, ArtworkDevice: "leader"},
		"bare":   {Source: "url", Metadata: media.MediaMetadata{Title: "x"}},
	}
	var asked string
	app := fiber.New()
	mountSessionArtwork(sessionArtworkDeps{
		auth: func(_ context.Context, token string) (string, error) {
			if token == "nobody" {
				return "", auth.ErrUnauthorized
			}
			return token, nil
		},
		session: func(id string) (media.MediaSession, bool) { s, ok := sessions[id]; return s, ok },
		art: func(dev string, s media.MediaSession) string {
			asked = dev + " " + s.ArtworkDevice
			return "img:" + p.RefLoaded("k", func() (string, []byte, error) { return "image/png", png, nil })
		},
		images: p, log: slog.New(slog.DiscardHandler),
	})(app)
	call := func(token, item string) (int, string, string) {
		req := httptest.NewRequest(http.MethodGet, "/session-artwork", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if item != "" {
			req.Header.Set(itemHeader, item)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	if code, _, _ := call("", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _, _ := call("nobody", ""); code != 401 {
		t.Fatalf("unknown token: %d", code)
	}
	for _, dev := range []string{"bare", "idle"} {
		if code, _, _ := call(dev, ""); code != 404 {
			t.Fatalf("%s: %d", dev, code)
		}
	}
	if code, _, _ := call("member", itemKey(media.MediaMetadata{Title: "Next", Artist: "Bokka"})); code != 409 {
		t.Fatalf("another song: %d", code)
	}
	code, typ, body := call("member", itemKey(media.MediaMetadata{Title: "Town", Artist: "Bokka"}))
	if code != 200 || typ != "image/png" || body != string(png) || asked != "member leader" {
		t.Fatalf("cover: %d %s %q asked %q", code, typ, body, asked)
	}
}
