package ringsounds

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const authWait = 5 * time.Second

// Mount serves GET /ring-sound/{id} on the device API: the sound's Ogg Opus to the device it was made for, with the
// device's bearer token (never in the url), whole or one byte range, so audiod's url input streams and loops it. 401
// for an unknown token, 404 for an id the device does not hold (another device's included), 416 for a range past its
// end. Lines carry the device, a code and byte counts.
func (s *Service) Mount(r fiber.Router, authDevice func(ctx context.Context, token string) (string, error)) {
	r.Get("/ring-sound/:id", func(c fiber.Ctx) error {
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || token == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		ctx, cancel := context.WithTimeout(c.Context(), authWait)
		dev, err := authDevice(ctx, token)
		cancel()
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			return c.SendStatus(fiber.StatusUnauthorized)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		row, err := s.get(c.Context(), dev, c.Params("id"))
		switch {
		case errors.Is(err, ErrNotFound):
			s.d.Log.Info("ring_sound_refused", "device_id", dev, "code", "not_found")
			return c.SendStatus(fiber.StatusNotFound)
		case err != nil:
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		size := len(row.Audio)
		c.Set(fiber.HeaderContentType, "audio/ogg")
		c.Set(fiber.HeaderAcceptRanges, "bytes")
		c.Set(fiber.HeaderETag, `"`+row.Sha256+`"`)
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		first, last, ranged, ok := byteRange(c.Get(fiber.HeaderRange), size)
		if !ok {
			c.Set(fiber.HeaderContentRange, "bytes */"+strconv.Itoa(size))
			return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
		}
		logServed(s.d.Log, dev, ranged, last-first+1)
		if !ranged {
			return c.Send(row.Audio)
		}
		c.Set(fiber.HeaderContentRange, "bytes "+strconv.Itoa(first)+"-"+strconv.Itoa(last)+"/"+strconv.Itoa(size))
		return c.Status(fiber.StatusPartialContent).Send(row.Audio[first : last+1])
	})
}

func logServed(log *slog.Logger, dev string, ranged bool, n int) {
	log.Info("ring_sound_served", "device_id", dev, "ranged", ranged, "bytes", n)
}

// byteRange reads one "bytes=a-b", "bytes=a-" or "bytes=-n" range of size bytes; none (or one this does not parse,
// several among them) is the whole. ok false for one past the end
func byteRange(header string, size int) (first, last int, ranged, ok bool) {
	spec, isBytes := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if header == "" || !isBytes || strings.Contains(spec, ",") {
		return 0, size - 1, false, true
	}
	from, to, found := strings.Cut(spec, "-")
	if !found {
		return 0, size - 1, false, true
	}
	if from == "" {
		n, err := strconv.Atoi(to)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		return max(0, size-n), size - 1, true, size > 0
	}
	a, err := strconv.Atoi(from)
	if err != nil || a < 0 || a >= size {
		return 0, 0, false, false
	}
	b := size - 1
	if to != "" {
		if b, err = strconv.Atoi(to); err != nil || b < a {
			return 0, 0, false, false
		}
		b = min(b, size-1)
	}
	return a, b, true, true
}
