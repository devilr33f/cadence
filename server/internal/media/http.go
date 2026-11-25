package media

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

type Devices interface {
	Canonical(ctx context.Context, deviceID string) (string, error)
}

type mediaIn struct {
	ID string `path:"id"`
}

// mediaOut's body is a MediaSession, or MediaIdle for none (MediaView in the
// document)
type mediaOut struct{ Body any }

// MediaView documents get-device-media's answer: the device's session, or
// {"state":"idle"} when it has none.
type MediaView struct{}

// Schema is one of the two.
func (MediaView) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		r.Schema(reflect.TypeFor[MediaSession](), true, ""),
		r.Schema(reflect.TypeFor[MediaIdle](), true, ""),
	}}
}

type artworkIn struct {
	ID string `path:"id"`
	H  string `query:"h" pattern:"^[0-9a-f]{64}$" doc:"The artworkHash the caller expects: when the session's art is another, 409 artwork_changed and nothing is fetched."`
}

// artworkOut is the image; its type is the document's content, not a header
type artworkOut struct {
	ContentType  string `header:"Content-Type" hidden:"true"`
	CacheControl string `header:"Cache-Control"`
	NoSniff      string `header:"X-Content-Type-Options"`
	Body         []byte
}

func imageContent() map[string]*huma.MediaType {
	content := map[string]*huma.MediaType{}
	for _, typ := range artTypes {
		content[typ] = &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	}
	return content
}

// RegisterAdmin serves GET /devices/{id}/media, the device's session or
// {"state":"idle"} for none, and GET /devices/{id}/artwork, that session's
// cover art.
func RegisterAdmin(a huma.API, s *Store, devs Devices, art *Artwork) {
	defer api.Describe(a, docs)
	huma.Register(a, huma.Operation{
		OperationID: "get-device-media", Method: http.MethodGet, Path: "/devices/{id}/media", Tags: []string{"media"},
	}, func(ctx context.Context, in *mediaIn) (*mediaOut, error) {
		dev, err := canonical(ctx, s, devs, in.ID)
		if err != nil {
			return nil, err
		}
		if v, ok := s.Get(dev); ok {
			return &mediaOut{Body: v}, nil
		}
		return &mediaOut{Body: none}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-device-artwork", Method: http.MethodGet, Path: "/devices/{id}/artwork", Tags: []string{"media"},
		Description: "The image the session's artworkUrl names, fetched by the server (https on port 443 only, no " +
			"internal address, no redirect, 5 s, at most 5 MiB, jpeg, png, webp or gif) and kept in memory by the " +
			"url's SHA-256, the session's artworkHash; or, for a source with no url (airplay), the jpeg or png the " +
			"device uploaded, kept in memory by its SHA-256. 404 no_artwork without a session or its art, 409 " +
			"artwork_changed when h names other art than the session's (nothing fetched), 502 " +
			"artwork_unavailable when the fetch failed or failed in the last 30 s.",
		Responses: map[string]*huma.Response{"200": {Description: "The cover art.", Content: imageContent()}},
	}, func(ctx context.Context, in *artworkIn) (*artworkOut, error) {
		dev, err := canonical(ctx, s, devs, in.ID)
		if err != nil {
			return nil, err
		}
		v, ok := s.Get(dev)
		if !ok || v.Metadata.ArtworkHash == "" {
			return nil, api.Error(http.StatusNotFound, "no_artwork")
		}
		// the caller keys the image by the hash it read: another session's art would
		// be kept under it (ruling M8-R3)
		if in.H != "" && in.H != v.Metadata.ArtworkHash {
			return nil, api.Error(http.StatusConflict, "artwork_changed")
		}
		if v.Metadata.ArtworkURL == "" {
			img, ok := art.uploaded(cmp.Or(v.ArtworkDevice, dev), v.Metadata.ArtworkHash)
			if !ok {
				return nil, api.Error(http.StatusNotFound, "no_artwork")
			}
			return &artworkOut{ContentType: img.contentType, CacheControl: "no-store", NoSniff: "nosniff", Body: img.data}, nil
		}
		img, err := art.get(v.Metadata.ArtworkURL)
		if err != nil {
			s.log.Warn("artwork_unavailable", "device_id", dev, "reason", artReason(err))
			return nil, api.Error(http.StatusBadGateway, "artwork_unavailable")
		}
		return &artworkOut{ContentType: img.contentType, CacheControl: "no-store", NoSniff: "nosniff", Body: img.data}, nil
	})
}

func canonical(ctx context.Context, s *Store, devs Devices, id string) (string, error) {
	dev, err := devs.Canonical(ctx, id)
	switch {
	case errors.Is(err, devices.ErrNotFound):
		return "", api.Error(http.StatusNotFound, "not_found")
	case err != nil:
		s.log.Error("media_state_failed")
		return "", api.Error(http.StatusInternalServerError, "internal")
	}
	return dev, nil
}
