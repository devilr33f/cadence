package ambient

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/pkg/i18n"
)

// Rendition is one of a clip's files in the manifest.
type Rendition string

const (
	H264_1080 Rendition = "url-1080-H264"
	SDR1080   Rendition = "url-1080-SDR"
	HDR1080   Rendition = "url-1080-HDR"
	SDR4K     Rendition = "url-4K-SDR"
	HDR4K     Rendition = "url-4K-HDR"
)

var renditions = []Rendition{H264_1080, SDR1080, HDR1080, SDR4K, HDR4K}

// Categories are the manifest's four, by the name display.screen's ambient.categories takes.
var Categories = []string{"space", "landscapes", "cities", "underwater"}

// Clip is one Aerial video.
type Clip struct {
	ID string
	// space, landscapes, cities or underwater; empty when the manifest gives none we know
	Category string
	// Apple's English label (accessibilityLabel), for logs never: a place name is fine, but logs carry ids alone
	Label string
	URLs  map[Rendition]string
	// caption keys by the second they start at, sorted
	Points []Point
}

// Point is a timed caption key of a clip.
type Point struct {
	AtS int
	Key string
}

// Manifest is a parsed resources tar.
type Manifest struct {
	Clips []Clip
	// caption text by language and key; categories' names too (AerialCategorySpace, ...)
	Strings map[i18n.Lang]map[string]string
}

// limits on what a tar may hold; Apple's is 1.9 MB with a 115 kB entries.json and 50 kB strings files
const (
	maxTar     = 32 << 20
	maxEntries = 8 << 20
	maxStrings = 2 << 20
)

// stringsFile is a language's captions in the tar; en_GB and others are not read
func stringsFile(l i18n.Lang) string {
	return "TVIdleScreenStrings.bundle/" + string(l) + ".lproj/Localizable.nocache.strings"
}

type entriesJSON struct {
	Assets []struct {
		ID                 string            `json:"id"`
		AccessibilityLabel string            `json:"accessibilityLabel"`
		Categories         []string          `json:"categories"`
		PointsOfInterest   map[string]string `json:"pointsOfInterest"`
	} `json:"assets"`
	Categories []struct {
		ID               string `json:"id"`
		LocalizedNameKey string `json:"localizedNameKey"`
	} `json:"categories"`
}

// ParseTar reads the manifest tar: entries.json, and the en and ru caption strings (both optional; a clip then has
// no caption text). Clip urls must be https; a clip with none is left out.
func ParseTar(r io.Reader) (*Manifest, error) {
	tr := tar.NewReader(io.LimitReader(r, maxTar))
	var entries []byte
	m := &Manifest{Strings: map[i18n.Lang]map[string]string{}}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ambient: tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		switch name {
		case "entries.json":
			if entries, err = readLimited(tr, maxEntries); err != nil {
				return nil, err
			}
		default:
			for _, l := range i18n.Catalogued {
				if name != stringsFile(l) {
					continue
				}
				data, err := readLimited(tr, maxStrings)
				if err != nil {
					return nil, err
				}
				s, err := decodeStrings(data)
				if err != nil {
					return nil, fmt.Errorf("ambient: %s: %w", l, err)
				}
				m.Strings[l] = s
			}
		}
	}
	if entries == nil {
		return nil, errors.New("ambient: no entries.json")
	}
	clips, err := parseEntries(entries)
	if err != nil {
		return nil, err
	}
	m.Clips = clips
	return m, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("ambient: tar: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("ambient: tar member too large")
	}
	return data, nil
}

func parseEntries(data []byte) ([]Clip, error) {
	var e entriesJSON
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("ambient: entries.json: %w", err)
	}
	// the asset objects again, for their url-* fields
	var raw struct {
		Assets []map[string]json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("ambient: entries.json: %w", err)
	}
	cats := map[string]string{}
	for _, c := range e.Categories {
		name := strings.ToLower(strings.TrimPrefix(c.LocalizedNameKey, "AerialCategory"))
		if slices.Contains(Categories, name) {
			cats[c.ID] = name
		}
	}
	var clips []Clip
	seen := map[string]bool{}
	for i, a := range e.Assets {
		if a.ID == "" || seen[a.ID] {
			continue
		}
		c := Clip{ID: a.ID, Label: a.AccessibilityLabel, URLs: map[Rendition]string{}}
		for _, id := range a.Categories {
			if name, ok := cats[id]; ok {
				c.Category = name
				break
			}
		}
		for _, r := range renditions {
			var s string
			if v, ok := raw.Assets[i][string(r)]; ok && json.Unmarshal(v, &s) == nil && httpsURL(s) {
				c.URLs[r] = s
			}
		}
		if len(c.URLs) == 0 {
			continue
		}
		for at, key := range a.PointsOfInterest {
			s, err := strconv.Atoi(at)
			if err != nil || s < 0 || key == "" {
				continue
			}
			c.Points = append(c.Points, Point{AtS: s, Key: key})
		}
		sort.Slice(c.Points, func(i, j int) bool { return c.Points[i].AtS < c.Points[j].AtS })
		seen[a.ID] = true
		clips = append(clips, c)
	}
	if len(clips) == 0 {
		return nil, errors.New("ambient: entries.json has no playable clip")
	}
	return clips, nil
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// Caption is a clip's caption text in a language, falling back to en; "" when neither has the key.
func (m *Manifest) Caption(l i18n.Lang, key string) string {
	if s, ok := m.Strings[l][key]; ok {
		return s
	}
	return m.Strings[i18n.En][key]
}
