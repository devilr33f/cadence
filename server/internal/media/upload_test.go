package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

const deviceToken = "device-secret-token"

func bytesHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// put sends PUT /v2/artwork/{hash} with body as the device bearer
func put(t *testing.T, a *media.Artwork, logs *bytes.Buffer, bearer, hash string, body []byte) int {
	t.Helper()
	app := api.NewApp(api.Options{DB: noPing{}, Version: "t", Device: []func(fiber.Router){
		func(r fiber.Router) {
			a.MountDevice(r, func(_ context.Context, token string) (string, error) {
				if token != deviceToken {
					return "", auth.ErrUnauthorized
				}
				return devID, nil
			}, slog.New(slog.NewTextHandler(logs, nil)))
		},
	}})
	req := httptest.NewRequest(http.MethodPut, "/v2/artwork/"+hash, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// airplay names its cover by the hash of the bytes the device uploaded: the session's artworkHash is that hash, and
// get-device-artwork serves the bytes only to the device that uploaded them
func TestUploadedArtworkIsTheSessionsCover(t *testing.T) {
	e := newEnv(t)
	e.art = media.NewArtwork()
	var logs bytes.Buffer
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 500)...)
	hash := bytesHash(png)

	m := playing()
	m.Source = cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY
	m.Metadata.ArtworkUrl, m.Metadata.ArtworkSha256 = "", hash
	handle(t, e.store, devID, m)
	if code, body := e.get(t, devID); code != 200 || !strings.Contains(body, `"artworkHash":"`+hash+`"`) || !strings.Contains(body, `"artworkUrl":""`) {
		t.Fatalf("%d %s", code, body)
	}
	// named before it arrived: nothing to serve yet
	if code, _, _ := e.do(t, "x", artPath); code != 404 {
		t.Fatalf("before the upload: %d", code)
	}
	if code := put(t, e.art, &logs, deviceToken, hash, png); code != 204 {
		t.Fatalf("upload: %d", code)
	}
	code, h, body := e.do(t, "x", artPath+"?h="+hash)
	if code != 200 || !bytes.Equal(body, png) || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %d bytes", code, h, len(body))
	}
	// another device naming the same hash gets nothing of this one's
	m2 := playing()
	m2.Metadata.ArtworkUrl, m2.Metadata.ArtworkSha256 = "", hash
	handle(t, e.store, "dev-2", m2)
	if code, _, _ := e.do(t, "x", "/api/v1/devices/dev-2/artwork"); code != 404 {
		t.Fatalf("dev-2: %d", code)
	}
	// a url wins over an upload, and a malformed hash is none
	m.Metadata.ArtworkSha256 = "NOTHEX"
	m.Metadata.ArtworkUrl = ""
	handle(t, e.store, devID, m)
	if _, body := e.get(t, devID); !strings.Contains(body, `"artworkHash":""`) {
		t.Fatalf("malformed hash shown: %s", body)
	}
	if strings.Contains(logs.String(), deviceToken) {
		t.Fatal("the token reached the log")
	}
}

func TestUploadRefusals(t *testing.T) {
	a := media.NewArtwork()
	var logs bytes.Buffer
	jpeg := []byte("\xff\xd8\xff\xe0 a cover")
	for _, tc := range []struct {
		name   string
		bearer string
		hash   string
		body   []byte
		want   int
	}{
		{"unknown token", "nope", bytesHash(jpeg), jpeg, 401},
		{"another hash", deviceToken, bytesHash([]byte("x")), jpeg, 400},
		{"no hash", deviceToken, "abc", jpeg, 400},
		{"not an image", deviceToken, bytesHash([]byte("<svg/>")), []byte("<svg/>"), 415},
		{"too large", deviceToken, "", append([]byte("\xff\xd8\xff"), make([]byte, 1<<20)...), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := tc.hash
			if hash == "" {
				hash = bytesHash(tc.body)
			}
			if code := put(t, a, &logs, tc.bearer, hash, tc.body); code != tc.want {
				t.Fatalf("%d, want %d", code, tc.want)
			}
		})
	}
	for _, code := range []string{"code=bad_hash", "code=bad_type", "code=too_large"} {
		if !strings.Contains(logs.String(), code) {
			t.Errorf("no %s in %s", code, logs.String())
		}
	}
}
