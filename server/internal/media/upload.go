package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// covers a device uploads because its source handed them over as bytes (airplay): memory only (D11), the least
// recently used dropped first
const (
	maxUploadBytes = 1 << 20
	uploadEntries  = 16
	uploadBytes    = 16 << 20
	uploadAuthWait = 5 * time.Second
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// uploadKey keeps a device's covers apart from another's: a session names only its own device's uploads
func uploadKey(deviceID, hash string) artKey { return sha256.Sum256([]byte(deviceID + "\x00" + hash)) }

// uploaded is the cover a device uploaded under hash.
func (a *Artwork) uploaded(deviceID, hash string) (artImage, bool) {
	return a.uploads.get(uploadKey(deviceID, hash))
}

// Uploaded is the cover a device uploaded under hash, as the image proxy serves it to a screen; ok false for none.
func (a *Artwork) Uploaded(deviceID, hash string) (contentType string, data []byte, ok bool) {
	img, ok := a.uploaded(deviceID, hash)
	return img.contentType, img.data, ok
}

// imageType sniffs the two formats a device uploads: jpeg and png
func imageType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	}
	return ""
}

// MountDevice serves PUT /artwork/{sha256} on the device API: a cover's bytes with the device's bearer token, kept
// until a session's artworkSha256 names it. 204 once kept; 400 bad_hash when the path is no SHA-256 or the bytes hash
// to another, 413 over 1 MiB, 415 neither jpeg nor png, 401 for an unknown token. logs carry the device and a code.
func (a *Artwork) MountDevice(r fiber.Router, authDevice func(ctx context.Context, token string) (string, error), log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r.Put("/artwork/:sha256", func(c fiber.Ctx) error {
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || token == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		ctx, cancel := context.WithTimeout(c.Context(), uploadAuthWait)
		dev, err := authDevice(ctx, token)
		cancel()
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			return c.SendStatus(fiber.StatusUnauthorized)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		hash := c.Params("sha256")
		body := c.Body()
		code := ""
		switch {
		case !sha256Hex.MatchString(hash):
			code = "bad_hash"
		case len(body) > maxUploadBytes:
			code = "too_large"
		case imageType(body) == "":
			code = "bad_type"
		default:
			if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != hash {
				code = "bad_hash"
			}
		}
		if code != "" {
			log.Warn("artwork_upload_refused", "device_id", dev, "code", code)
			return c.SendStatus(map[string]int{
				"bad_hash": fiber.StatusBadRequest, "too_large": fiber.StatusRequestEntityTooLarge,
				"bad_type": fiber.StatusUnsupportedMediaType,
			}[code])
		}
		a.uploads.put(uploadKey(dev, hash), artImage{contentType: imageType(body), data: bytes.Clone(body)})
		log.Info("artwork_uploaded", "device_id", dev, "bytes", len(body))
		return c.SendStatus(fiber.StatusNoContent)
	})
}
