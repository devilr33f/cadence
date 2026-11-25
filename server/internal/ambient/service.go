package ambient

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"

	"git.shino.boo/devilreef/cadence/pkg/i18n"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

// Source is display.screen's ambient.source.
type Source string

const (
	SourceAerial  Source = "aerial"
	SourceImages  Source = "images"
	SourceArtwork Source = "artwork"
	SourceMixed   Source = "mixed"
	SourceOff     Source = "off"
)

// Device is what the service knows of the asking screen: its id, language setting, profile and ambient config.
type Device struct {
	ID       string
	Language string
	Profile  Profile
	// ambient.source; empty is aerial
	Source Source
	// ambient.categories; empty is all four
	Categories []string
}

// NextArgs are ambient.next's arguments. Kind overrides the device's source for one call: the screen asks for an
// image while a clip buffers, say. Empty takes the source.
type NextArgs struct {
	Kind Source `json:"kind,omitempty"`
}

// Caption is a clip's caption from AtS seconds into it.
type Caption struct {
	AtS  int    `json:"atS"`
	Text string `json:"text"`
}

// Next is ambient.next's result.
type Next struct {
	// video (an Aerial clip, played muted) or image (drawn with the Ken Burns drift)
	Kind string `json:"kind"`
	// a clip's or image's id, so the screen can tell a repeat
	ID       string    `json:"id,omitempty"`
	URL      string    `json:"url"`
	Captions []Caption `json:"captions,omitempty"`
	Category string    `json:"category,omitempty"`
	// seconds; 0 when not known (the manifest gives no lengths: a clip plays to its end, an image for the screen's
	// own interval)
	Duration int `json:"duration"`
	// the overlay's weather line at the device's home place ("+3…+7 °C · облачно"), absent when it has none
	Weather string `json:"weather,omitempty"`
}

// ErrUnavailable means nothing can be served yet: no manifest, or no image.
var ErrUnavailable = errors.New("ambient: nothing to show")

// errOff means the device's ambient is off
var errOff = errors.New("ambient: off")

// recent is how many past picks per device a pick avoids
const recent = 8

// Config configures the service.
type Config struct {
	Manifest func() *Manifest
	// the owner's list (checked by CheckOwnerImages); served for images and mixed
	OwnerImages func() []OwnerImage
	Artwork     Artwork
	// Weather is the overlay's weather line; nil is none
	Weather *WeatherLines
	// Rand picks; defaults to a random source
	Rand *rand.Rand
}

// Service answers ambient.next.
type Service struct {
	manifest func() *Manifest
	owner    func() []OwnerImage
	artwork  Artwork
	weather  *WeatherLines

	mu     sync.Mutex
	rand   *rand.Rand
	recent map[string][]string
}

// New makes the service.
func New(c Config) *Service {
	s := &Service{manifest: c.Manifest, owner: c.OwnerImages, artwork: c.Artwork, weather: c.Weather, rand: c.Rand,
		recent: map[string][]string{}}
	if s.manifest == nil {
		s.manifest = func() *Manifest { return nil }
	}
	if s.owner == nil {
		s.owner = func() []OwnerImage { return nil }
	}
	if s.artwork == nil {
		s.artwork = NoArtwork{}
	}
	if s.rand == nil {
		s.rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // a background's pick
	}
	return s
}

// Handle is ambient.next's rpc handler: the RpcRequest's json in, an RpcResponse's status and json out ({"code":
// ...} unless ok). The integrator's rpc dispatcher calls it with the linked device.
func (s *Service) Handle(ctx context.Context, d Device, args []byte) (string, []byte) {
	var a NextArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return rpc.StatusInvalid, code("args")
		}
	}
	switch a.Kind {
	case "", SourceAerial, SourceImages, SourceArtwork, SourceMixed:
	default:
		return rpc.StatusInvalid, code("kind")
	}
	n, err := s.Next(ctx, d, a)
	if err == nil {
		n.Weather = s.weather.Line(ctx, d.ID)
	}
	switch {
	case errors.Is(err, errOff):
		return rpc.StatusRefused, code("off")
	case errors.Is(err, ErrUnavailable):
		return rpc.StatusUnavailable, code("none")
	case err != nil:
		return rpc.StatusInternal, code("internal")
	}
	out, err := json.Marshal(n)
	if err != nil {
		return rpc.StatusInternal, code("internal")
	}
	return rpc.StatusOK, out
}

func code(c string) []byte {
	b, _ := json.Marshal(map[string]string{"code": c})
	return b
}

// Next picks the device's next background, never one of its last few when another is left.
func (s *Service) Next(ctx context.Context, d Device, a NextArgs) (Next, error) {
	src := a.Kind
	if src == "" {
		src = d.Source
	}
	if src == "" {
		src = SourceAerial
	}
	lang := i18n.Normalize(d.Language)
	switch src {
	case SourceOff:
		return Next{}, errOff
	case SourceAerial:
		return s.clip(d, lang)
	case SourceImages:
		return s.image(ctx, d, true, false)
	case SourceArtwork:
		return s.image(ctx, d, false, true)
	default:
		// mixed: a clip or an image, half each, falling back to whichever there is
		var first, second func() (Next, error)
		clip := func() (Next, error) { return s.clip(d, lang) }
		img := func() (Next, error) { return s.image(ctx, d, true, true) }
		first, second = clip, img
		s.mu.Lock()
		if s.rand.IntN(2) == 1 {
			first, second = img, clip
		}
		s.mu.Unlock()
		n, err := first()
		if errors.Is(err, ErrUnavailable) {
			return second()
		}
		return n, err
	}
}

func (s *Service) clip(d Device, lang i18n.Lang) (Next, error) {
	m := s.manifest()
	if m == nil {
		return Next{}, ErrUnavailable
	}
	type pick struct {
		clip Clip
		url  string
	}
	var picks []pick
	for _, c := range m.Clips {
		if len(d.Categories) > 0 && !slices.Contains(d.Categories, c.Category) {
			continue
		}
		if _, u, ok := d.Profile.choose(c); ok {
			picks = append(picks, pick{c, u})
		}
	}
	if len(picks) == 0 {
		return Next{}, ErrUnavailable
	}
	ids := make([]string, len(picks))
	for i, p := range picks {
		ids[i] = p.clip.ID
	}
	p := picks[s.choose(d.ID, ids)]
	n := Next{Kind: "video", ID: p.clip.ID, URL: p.url, Category: p.clip.Category}
	for _, pt := range p.clip.Points {
		if text := m.Caption(lang, pt.Key); text != "" {
			n.Captions = append(n.Captions, Caption{AtS: pt.AtS, Text: text})
		}
	}
	return n, nil
}

func (s *Service) image(ctx context.Context, d Device, owner, artwork bool) (Next, error) {
	var images []Image
	if owner {
		for _, im := range s.owner() {
			if httpsURL(im.URL) {
				images = append(images, Image(im))
			}
		}
	}
	if artwork {
		art, err := s.artwork.Backdrops(ctx, d.ID, 50)
		if err != nil && len(images) == 0 {
			return Next{}, ErrUnavailable
		}
		images = append(images, art...)
	}
	if len(images) == 0 {
		return Next{}, ErrUnavailable
	}
	ids := make([]string, len(images))
	for i, im := range images {
		ids[i] = im.URL
	}
	im := images[s.choose(d.ID, ids)]
	n := Next{Kind: "image", URL: im.URL}
	if im.Caption != "" {
		n.Captions = []Caption{{AtS: 0, Text: im.Caption}}
	}
	return n, nil
}

// choose picks an index of ids at random among those the device was not shown lately (all of them when every one
// was), and remembers it
func (s *Service) choose(device string, ids []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := s.recent[device]
	var fresh []int
	for i, id := range ids {
		if !slices.Contains(past, id) {
			fresh = append(fresh, i)
		}
	}
	var i int
	if len(fresh) > 0 {
		i = fresh[s.rand.IntN(len(fresh))]
	} else {
		i = s.rand.IntN(len(ids))
	}
	past = append(past, ids[i])
	if len(past) > min(recent, len(ids)-1) {
		past = past[len(past)-max(min(recent, len(ids)-1), 0):]
	}
	s.recent[device] = past
	return i
}

// Forget drops a device's recent picks (it was deleted).
func (s *Service) Forget(device string) {
	s.mu.Lock()
	delete(s.recent, device)
	s.mu.Unlock()
}
