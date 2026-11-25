package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/ambient"
	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// aerialFetcher is the Aerial manifest's fetcher as the config has it: Apple's by default, none with off or beside the
// fakes without a url of their own, else the url's host alone
func aerialFetcher(cfg config.Config, log *slog.Logger) *ambient.Fetcher {
	c := ambient.FetcherConfig{Log: log}
	switch {
	case cfg.AmbientManifest == "off", cfg.AmbientManifest == "" && cfg.DevFakes:
		return nil
	case cfg.AmbientManifest != "":
		u, err := url.Parse(cfg.AmbientManifest)
		if err != nil {
			return nil
		}
		c.URL, c.Hosts = cfg.AmbientManifest, []string{u.Hostname()}
	}
	f, err := ambient.NewFetcher(c)
	if err != nil {
		log.Warn("ambient_fetcher_failed")
		return nil
	}
	return f
}

// runAmbient fetches the manifest weekly until ctx ends; the returned func waits for it
func runAmbient(ctx context.Context, f *ambient.Fetcher) func() {
	if f == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// lateArtwork is ambient's artwork source, set once the catalog exists: the screens (and their ambient.next) are
// built before the catalog, which registers onto their router
type lateArtwork struct {
	a atomic.Pointer[ambient.Artwork]
}

func (l *lateArtwork) set(a ambient.Artwork) { l.a.Store(&a) }

func (l *lateArtwork) Backdrops(ctx context.Context, deviceID string, limit int) ([]ambient.Image, error) {
	if a := l.a.Load(); a != nil {
		return (*a).Backdrops(ctx, deviceID, limit)
	}
	return nil, nil
}

func ambientService(f *ambient.Fetcher, art ambient.Artwork, weather *ambient.WeatherLines) *ambient.Service {
	c := ambient.Config{Artwork: art, Weather: weather}
	if f != nil {
		c.Manifest = f.Manifest
	}
	return ambient.New(c)
}

// weatherPlace is the screen's weather provider, home place and language from its effective settings
func weatherPlace(s tools.SettingsFunc) func(context.Context, string) (ambient.WeatherPlace, bool) {
	return func(ctx context.Context, id string) (ambient.WeatherPlace, bool) {
		v, err := s(ctx, id)
		if err != nil {
			return ambient.WeatherPlace{}, false
		}
		return ambient.WeatherPlace{Provider: v.Weather, Location: v.WeatherLocation, Language: v.Language, TimeZone: v.TimeZone}, true
	}
}

// screenAmbient reads what ambient.next needs of the asking screen: its profile from display.screen's state, its
// ambient config from the config the server set, and its language setting
func screenAmbient(state screens.StateFunc, desired func(ctx context.Context, deviceID, capability string) (json.RawMessage, error),
	lang func(context.Context, string) string,
) func(ctx context.Context, id string) ambient.Device {
	return func(ctx context.Context, id string) ambient.Device {
		d := ambient.Device{ID: id, Language: lang(ctx, id)}
		if raw, ok := state(ctx, id, capschema.DisplayScreenID); ok {
			var st struct {
				MaxHeight int      `json:"maxHeight"`
				Codecs    []string `json:"codecs"`
			}
			if json.Unmarshal(raw, &st) == nil {
				d.Profile.MaxHeight, d.Profile.Codecs = st.MaxHeight, st.Codecs
			}
		}
		if desired != nil {
			if raw, err := desired(ctx, id, capschema.DisplayScreenID); err == nil && len(raw) > 0 {
				var c struct {
					Ambient struct {
						Source        ambient.Source `json:"source"`
						Categories    []string       `json:"categories"`
						AerialQuality string         `json:"aerialQuality"`
					} `json:"ambient"`
				}
				if json.Unmarshal(raw, &c) == nil {
					d.Source, d.Categories, d.Profile.Quality = c.Ambient.Source, c.Ambient.Categories, c.Ambient.AerialQuality
				}
			}
		}
		return d
	}
}

// ambientMethod serves rpc ambient.next for the linked screen
func ambientMethod(s *ambient.Service, device func(ctx context.Context, id string) ambient.Device) calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		status, out := s.Handle(ctx, device(ctx, c.DeviceID), c.Args)
		return calls.Reply{Status: status, JSON: json.RawMessage(out)}
	}
}
