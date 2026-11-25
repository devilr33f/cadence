package images

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// Mount serves GET /images/{id} on the device API, without credentials: an
// id is itself the capability (unguessable, issued by this process), as a
// screen's image loader can send no token. The image comes in a type the
// request's Accept takes (a screen's: image/jpeg, image/png), converted when
// it must be. 404 for an id this process did not issue, 406 for an image no
// accepted type fits, 502 when the fetch failed now or in the last 30 s.
func (p *Proxy) Mount(r fiber.Router) {
	r.Get("/images/:id", func(c fiber.Ctx) error {
		id := c.Params("id")
		if !IDPattern.MatchString(id) {
			return c.SendStatus(fiber.StatusNotFound)
		}
		c.Set(fiber.HeaderVary, fiber.HeaderAccept)
		// the image's id is a capability: the span names the route alone
		ctx, span := tracing.StartKind(c.Context(), "images", "image.get", trace.SpanKindServer)
		typ, data, err := p.GetAccepting(ctx, id, c.Get(fiber.HeaderAccept))
		span.SetAttributes(attribute.Int(tracing.AttrHTTPResponseSize, len(data)))
		tracing.End(span, err)
		switch {
		case errors.Is(err, ErrUnknown):
			return c.SendStatus(fiber.StatusNotFound)
		case errors.Is(err, ErrNotAcceptable):
			return c.SendStatus(fiber.StatusNotAcceptable)
		case err != nil:
			return c.SendStatus(fiber.StatusBadGateway)
		}
		c.Set(fiber.HeaderContentType, typ)
		c.Set(fiber.HeaderCacheControl, "private, max-age=3600")
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
		return c.Send(data)
	})
}

type imageIn struct {
	ID string `path:"id" pattern:"^[A-Za-z0-9_-]{24}$" doc:"An image id the catalog handed out (an item's artwork hash, a card's img: ref)."`
}

type imageOut struct {
	ContentType  string `header:"Content-Type" hidden:"true"`
	CacheControl string `header:"Cache-Control"`
	NoSniff      string `header:"X-Content-Type-Options"`
	Body         []byte
}

// docs names the image route's scope: an ha token reaches it too, for the
// Home Assistant media browser's thumbnails
var docs = map[string]api.Doc{
	"get-catalog-image": {Summary: "Get a catalog image by its id", Scope: auth.ScopeHA},
}

// RegisterAdmin serves GET /catalog/images/{id} for the dashboard and an ha
// token's readers (the Home Assistant media browser's thumbnails).
func RegisterAdmin(a huma.API, p *Proxy) {
	defer api.Describe(a, docs)
	content := map[string]*huma.MediaType{}
	for _, typ := range types {
		content[typ] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	}
	huma.Register(a, huma.Operation{
		OperationID: "get-catalog-image", Method: http.MethodGet, Path: "/catalog/images/{id}", Tags: []string{"catalog"},
		Description: "The image an id the catalog issued names, fetched by the server (a public url under the cover " +
			"guard, a provider's own on its LAN host), kept in memory. 404 not_found for an id this server did not " +
			"issue (ids end with the process), 502 image_unavailable when the fetch failed now or in the last 30 s.",
		Responses: map[string]*huma.Response{"200": {Description: "The image.", Content: content}},
	}, func(ctx context.Context, in *imageIn) (*imageOut, error) {
		typ, data, err := p.Get(ctx, in.ID)
		switch {
		case errors.Is(err, ErrUnknown):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, api.Error(http.StatusBadGateway, "image_unavailable")
		}
		return &imageOut{ContentType: typ, CacheControl: "private, max-age=3600", NoSniff: "nosniff", Body: data}, nil
	})
}
