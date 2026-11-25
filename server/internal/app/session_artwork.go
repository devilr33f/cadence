package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// itemHeader names the session a device asks the cover of: the hex SHA-256 of its title, a NUL and its artist, as the
// device knows them (a header, so no proxy's access log keeps even a hash of what plays)
const itemHeader = "X-Cadence-Item"

const sessionArtworkAuthWait = 5 * time.Second

// sessionArtworkDeps is what GET /v2/session-artwork reads: the device's session as the server sees it (a group
// member's carries its stream source's cover), its cover as an img: ref, and the image proxy that serves the ref
type sessionArtworkDeps struct {
	auth    func(ctx context.Context, token string) (string, error)
	session func(deviceID string) (media.MediaSession, bool)
	art     func(dev string, s media.MediaSession) string
	images  *images.Proxy
	log     *slog.Logger
}

// itemKey is what itemHeader carries for a session
func itemKey(md media.MediaMetadata) string {
	sum := sha256.Sum256([]byte(md.Title + "\x00" + md.Artist))
	return hex.EncodeToString(sum[:])
}

// mountSessionArtwork serves GET /session-artwork on the device API: the cover of the device's own media session as
// jpeg or png (a webp or gif converted), for the device's own screen: the cover a source handed another device (a
// group's stream source's airplay upload) or one an artwork url names. Only the caller's own session's cover is ever
// served. 404 without a session or a cover, 409 when itemHeader names another title and artist than the server's
// view (the device asks again), 502 when the image failed, 401 for an unknown token. Logs carry the device and a code.
func mountSessionArtwork(d sessionArtworkDeps) func(fiber.Router) {
	return func(r fiber.Router) {
		r.Get("/session-artwork", func(c fiber.Ctx) error {
			token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
			if !ok || token == "" {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			ctx, cancel := context.WithTimeout(c.Context(), sessionArtworkAuthWait)
			dev, err := d.auth(ctx, token)
			cancel()
			switch {
			case errors.Is(err, auth.ErrUnauthorized):
				return c.SendStatus(fiber.StatusUnauthorized)
			case err != nil:
				return c.SendStatus(fiber.StatusServiceUnavailable)
			}
			s, ok := d.session(dev)
			if !ok || s.Metadata.ArtworkHash == "" {
				return c.SendStatus(fiber.StatusNotFound)
			}
			if want := c.Get(itemHeader); want != "" && want != itemKey(s.Metadata) {
				return c.SendStatus(fiber.StatusConflict)
			}
			id, ok := strings.CutPrefix(d.art(dev, s), "img:")
			if !ok {
				return c.SendStatus(fiber.StatusNotFound)
			}
			typ, data, err := d.images.GetAccepting(c.Context(), id, "image/jpeg, image/png")
			switch {
			case errors.Is(err, images.ErrUnknown):
				return c.SendStatus(fiber.StatusNotFound)
			case err != nil:
				d.log.Warn("session_artwork_failed", "device_id", dev)
				return c.SendStatus(fiber.StatusBadGateway)
			}
			c.Set(fiber.HeaderContentType, typ)
			c.Set(fiber.HeaderCacheControl, "no-store")
			c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
			return c.Send(data)
		})
	}
}
