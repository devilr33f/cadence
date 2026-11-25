package images

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"mime"
	"strconv"
	"strings"

	"golang.org/x/image/webp"
)

const (
	// maxConverted is what a screen takes encoded (screen/ui/images' cap)
	maxConverted = 4 << 20
	// maxSide is a converted source's largest side, as a screen decodes
	maxSide      = 4096
	jpegQuality  = 85
	convertSlots = 2
)

// ErrNotAcceptable is an image in no type the caller takes, and none it can
// be converted to
var ErrNotAcceptable = errors.New("images: not acceptable")

// accepted is an Accept header's image types: a type, image/* or */*, each
// with a q above 0. an empty header takes anything
type accepted struct {
	any   bool
	types map[string]bool
}

func parseAccept(header string) accepted {
	a := accepted{types: map[string]bool{}}
	if strings.TrimSpace(header) == "" {
		a.any = true
		return a
	}
	for _, part := range strings.Split(header, ",") {
		typ, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if q, ok := params["q"]; ok {
			if v, err := strconv.ParseFloat(q, 64); err != nil || v <= 0 {
				continue
			}
		}
		if typ == "*/*" || typ == "image/*" {
			a.any = true
			continue
		}
		a.types[typ] = true
	}
	return a
}

func (a accepted) takes(typ string) bool { return a.any || a.types[typ] }

// convertible are the types a screen's decoder lacks that this proxy
// converts (gif's first frame)
func convertible(typ string) bool { return typ == "image/webp" || typ == "image/gif" }

// convertKey names a converted copy in the cache: the targets a caller takes
func (a accepted) convertKey(id string) (string, bool) {
	jpg, pngOK := a.takes("image/jpeg"), a.takes("image/png")
	switch {
	case jpg && pngOK:
		return id + ".jp", true
	case jpg:
		return id + ".j", true
	case pngOK:
		return id + ".p", true
	}
	return "", false
}

// convert decodes a webp or a gif's first frame and encodes it as png when
// it has transparency and png is taken, else as jpeg (or png when jpeg is
// not taken); bounded by maxSide and maxConverted
func convert(typ string, data []byte, a accepted) (string, []byte, error) {
	var cfg image.Config
	var err error
	switch typ {
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	case "image/gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	default:
		return "", nil, ErrNotAcceptable
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide {
		return "", nil, ErrUnavailable
	}
	var img image.Image
	if typ == "image/webp" {
		img, err = webp.Decode(bytes.NewReader(data))
	} else {
		img, err = gif.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return "", nil, ErrUnavailable
	}
	opaque := true
	if o, ok := img.(interface{ Opaque() bool }); ok {
		opaque = o.Opaque()
	}
	var out bytes.Buffer
	switch {
	case a.takes("image/png") && (!opaque || !a.takes("image/jpeg")):
		typ, err = "image/png", (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img)
	case a.takes("image/jpeg"):
		typ, err = "image/jpeg", jpeg.Encode(&out, img, &jpeg.Options{Quality: jpegQuality})
	default:
		return "", nil, ErrNotAcceptable
	}
	if err != nil || out.Len() > maxConverted {
		return "", nil, ErrUnavailable
	}
	return typ, out.Bytes(), nil
}
