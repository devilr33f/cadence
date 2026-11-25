package favorites_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/favorites"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type favoriteNoPing struct{}

func (favoriteNoPing) Ping(context.Context) error { return nil }

func newFavoriteAPI(t *testing.T) (*fiber.App, *favorites.Service, *rater, *db.Queries) {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	r := &rater{}
	s := favorites.New(q, func(context.Context, string) (providers.Music, error) { return r, nil }, nil)
	app := api.NewApp(api.Options{
		DB: favoriteNoPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { favorites.RegisterAdmin(a, s) }},
	})
	return app, s, r, q
}

func favoriteRequest(t *testing.T, app *fiber.App, method, target, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer x")
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

func TestFavoritesAdminAPI(t *testing.T) {
	app, _, _, q := newFavoriteAPI(t)
	if code, body := favoriteRequest(t, app, http.MethodGet, "/api/v1/music/favorites", ""); code != 200 || body != `{"items":[]}` {
		t.Fatalf("empty favorites %d %s", code, body)
	}
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "Kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.StartPlayHistory(context.Background(), db.StartPlayHistoryParams{
		DeviceID: dev.ID, DedupeKey: "key", Source: "music", Provider: "yandex", Kind: "track", Title: "Song",
		ProviderSongID: "42", WebUrl: "https://music.yandex.ru/track/42", ProviderInstance: "ym",
		StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	code, body := favoriteRequest(t, app, http.MethodPut, "/api/v1/music/songs/yandex/42/feedback", `{"feedback":"like"}`)
	if code != 200 || !strings.Contains(body, `"favorite":true`) || !strings.Contains(body, `"providerSync":"updated"`) ||
		!strings.Contains(body, `"providerSongId":"42"`) {
		t.Fatalf("like %d %s", code, body)
	}
	if code, body = favoriteRequest(t, app, http.MethodGet, "/api/v1/music/favorites?query=song", ""); code != 200 ||
		!strings.Contains(body, `"title":"Song"`) || strings.Contains(body, `"instance"`) {
		t.Fatalf("favorites %d %s", code, body)
	}
	if code, _ = favoriteRequest(t, app, http.MethodPut, "/api/v1/music/songs/unknown/42/feedback", `{"feedback":"like"}`); code != 422 {
		t.Fatalf("unknown provider %d", code)
	}
	if code, _ = favoriteRequest(t, app, http.MethodPut, "/api/v1/music/songs/yandex/42/feedback", `{"feedback":"other"}`); code != 422 {
		t.Fatalf("unknown feedback %d", code)
	}
	if code, body = favoriteRequest(t, app, http.MethodPut, "/api/v1/music/songs/yandex/42/feedback", `{"feedback":"none"}`); code != 200 ||
		!strings.Contains(body, `"favorite":false`) || !strings.Contains(body, `"feedback":"none"`) {
		t.Fatalf("clear %d %s", code, body)
	}
	if code, body = favoriteRequest(t, app, http.MethodGet, "/api/v1/music/favorites", ""); code != 200 || body != `{"items":[]}` {
		t.Fatalf("cleared favorites %d %s", code, body)
	}
}

func TestFavoritesAdminProviderRefusal(t *testing.T) {
	app, _, r, q := newFavoriteAPI(t)
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "Kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.StartPlayHistory(context.Background(), db.StartPlayHistoryParams{
		DeviceID: dev.ID, DedupeKey: "key", Source: "music", Provider: "yandex", Kind: "track", Title: "Song",
		ProviderSongID: "42", ProviderInstance: "ym", StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	r.err = providers.ErrSignInRequired
	if code, body := favoriteRequest(t, app, http.MethodPut, "/api/v1/music/songs/yandex/42/feedback", `{"feedback":"like"}`); code != 409 || body != `{"error":"sign_in_required"}` {
		t.Fatalf("provider refusal %d %s", code, body)
	}
}
