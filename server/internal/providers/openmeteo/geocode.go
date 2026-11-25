package openmeteo

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Place is Open-Meteo geocoding's first match for a requested name.
type Place struct {
	Name      string
	Latitude  float64
	Longitude float64
}

// Geocoder is Open-Meteo's keyless place lookup, shared by weather modules.
type Geocoder struct {
	url    string
	now    func() time.Time
	client *http.Client
	cache  *geoCache
}

func NewGeocoder(c Config, client *http.Client) *Geocoder {
	if c.GeocodingURL == "" {
		c.GeocodingURL = defaultGeocodingURL
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Geocoder{url: c.GeocodingURL, now: c.Now, client: client, cache: newGeoCache()}
}

func (g *Geocoder) Locate(ctx context.Context, location, language string) (Place, error) {
	key := language + "|" + strings.ToLower(strings.TrimSpace(location))
	now := g.now()
	if p, ok := g.cache.get(key, now); ok {
		return p, nil
	}
	v := url.Values{}
	v.Set("name", location)
	v.Set("count", "1")
	v.Set("format", "json")
	if language != "" {
		v.Set("language", language)
	}
	var body struct {
		Results []struct {
			Name      string  `json:"name"`
			Country   string  `json:"country"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := getJSON(ctx, g.client, g.url, v, &body); err != nil {
		return Place{}, err
	}
	if len(body.Results) == 0 || body.Results[0].Name == "" {
		return Place{}, providers.ErrPlaceNotFound
	}
	r := body.Results[0]
	name := r.Name
	if r.Country != "" {
		name += ", " + r.Country
	}
	p := Place{Name: name, Latitude: r.Latitude, Longitude: r.Longitude}
	g.cache.put(key, p, now)
	return p, nil
}
