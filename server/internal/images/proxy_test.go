package images_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/images"
)

func TestIDsAreIssuedOnlyAndImagesCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		default:
			http.Redirect(w, r, "/a.png", http.StatusFound)
		}
	}))
	defer srv.Close()
	var public atomic.Int32
	p := images.New(images.Config{Public: func(string) (string, []byte, error) {
		public.Add(1)
		return "image/jpeg", []byte("jpg"), nil
	}})
	ctx := context.Background()
	local := p.Ref(srv.URL+"/a.png", true)
	if !images.IDPattern.MatchString(local) || local != p.Ref(srv.URL+"/a.png", true) {
		t.Fatalf("id %q", local)
	}
	if p.Ref(srv.URL+"/a.png", false) != "" {
		t.Fatal("a public http url got an id")
	}
	for range 2 {
		typ, data, err := p.Get(ctx, local)
		if err != nil || typ != "image/png" || string(data) != "png" {
			t.Fatalf("%q %q %v", typ, data, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times", hits.Load())
	}
	if _, _, err := p.Get(ctx, "AAAAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, images.ErrUnknown) {
		t.Fatalf("unknown id %v", err)
	}
	for _, path := range []string{"/page", "/redirect"} {
		if _, _, err := p.Get(ctx, p.Ref(srv.URL+path, true)); !errors.Is(err, images.ErrUnavailable) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	pub := p.Ref("https://i.ytimg.com/vi/x/hqdefault.jpg", false)
	if _, data, err := p.Get(ctx, pub); err != nil || string(data) != "jpg" || public.Load() != 1 {
		t.Fatalf("public %v", err)
	}
	if images.New(images.Config{}).Ref("https://i.ytimg.com/vi/x/hqdefault.jpg", false) == pub {
		t.Fatal("ids outlive the process")
	}
}

// a screen decodes png and jpeg alone: a webp (torrserver's posters) or a gif
// reaches it converted, by its Accept, once per id and form; a browser's
// Accept gets the original
func TestDeviceRouteConvertsWhatTheAcceptDoesNotTake(t *testing.T) {
	poster, err := os.ReadFile("testdata/poster.webp")
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := os.ReadFile("testdata/alpha.webp")
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 4, 2), color.Palette{color.Transparent, color.NRGBA{200, 10, 10, 255}})
	pal.SetColorIndex(1, 1, 1)
	if err := gif.Encode(&frame, pal, nil); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/poster.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write(poster)
		case "/alpha.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write(alpha)
		case "/frame.gif":
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write(frame.Bytes())
		case "/broken.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("RIFF....WEBPVP8 "))
		}
	}))
	defer srv.Close()
	p := images.New(images.Config{})
	// immutable as the server's app (api.New): else a param is the request buffer's, which the next request reuses, and
	// the ids the cache keeps change under it
	app := fiber.New(fiber.Config{Immutable: true})
	p.Mount(app)
	get := func(path, accept string) (*http.Response, []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/images/"+p.Ref(srv.URL+path, true), nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	const screen = "image/jpeg, image/png"
	cases := []struct {
		path, accept, typ string
		w, h              int
	}{
		{"/poster.webp", screen, "image/jpeg", 32, 16},
		{"/poster.webp", screen, "image/jpeg", 32, 16},
		{"/alpha.webp", screen, "image/png", 32, 16},
		{"/alpha.webp", "image/jpeg", "image/jpeg", 32, 16},
		{"/frame.gif", screen, "image/png", 4, 2},
		{"/poster.webp", "image/webp,image/*;q=0.8", "image/webp", 32, 16},
		{"/poster.webp", "", "image/webp", 32, 16},
	}
	for _, c := range cases {
		resp, body := get(c.path, c.accept)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != c.typ || resp.Header.Get("Vary") != "Accept" {
			t.Fatalf("%s %q: %d %q", c.path, c.accept, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if c.typ == "image/webp" {
			continue
		}
		img, format, err := image.Decode(bytes.NewReader(body))
		if err != nil || "image/"+format != c.typ || img.Bounds().Dx() != c.w || img.Bounds().Dy() != c.h {
			t.Fatalf("%s %q: %s %v %v", c.path, c.accept, format, img, err)
		}
		if c.path == "/alpha.webp" && c.typ == "image/png" {
			if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
				t.Fatal("transparency lost")
			}
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("fetched %d times", hits.Load())
	}
	if resp, _ := get("/poster.webp", "image/avif"); resp.StatusCode != http.StatusNotAcceptable {
		t.Fatalf("avif only %d", resp.StatusCode)
	}
	if resp, _ := get("/broken.webp", screen); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("broken %d", resp.StatusCode)
	}
}
