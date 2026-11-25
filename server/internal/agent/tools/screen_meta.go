package tools

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
)

// metaWait bounds the catalog read behind a play's metadata when the server no longer holds the item (a restart,
// the 12 h it keeps an item); the resolve runs beside it, so a play waits for neither alone
const metaWait = 3 * time.Second

// videoMeta is media.video/play's metadata: what the screen shows of the play (the osd's title and the line under
// it, the provider's badge, the art). memory-only on the device and the screen (D11), never logged
type videoMeta struct {
	Title          string `json:"title,omitempty"`
	Series         string `json:"series,omitempty"`
	Artist         string `json:"artist,omitempty"`
	Channel        string `json:"channel,omitempty"`
	Provider       string `json:"provider,omitempty"`
	ProviderName   string `json:"providerName,omitempty"`
	ProviderSource string `json:"providerSource,omitempty"`
	Episode        string `json:"episode,omitempty"`
	EpisodeName    string `json:"episodeName,omitempty"`
	ArtworkURL     string `json:"artworkUrl,omitempty"`
	ItemRef        string `json:"itemRef,omitempty"`
	// artwork is the image proxy id of the art, for the play's card; never sent
	artwork string
}

// legacy is the metadata a screen knew before the channel and the provider
func (m videoMeta) legacy() videoMeta {
	return videoMeta{Title: m.Title, Episode: m.Episode, ArtworkURL: m.ArtworkURL, ItemRef: m.ItemRef}
}

// oldScreen says the device refused a play's metadata by its schema: a cadenced from before the fields it carries now
func oldScreen(err error) bool {
	var inv *commands.InvalidArgumentsError
	return errors.As(err, &inv) && strings.HasPrefix(inv.Path, "/metadata")
}

// playMeta is the item behind ref as the screen shows it: as the catalog last handed it out, else read again. It also
// answers the item's episode labels with their names, for the episode a resolve picks
func (d ScreenDeps) playMeta(ctx context.Context, ref catalog.Ref) (videoMeta, map[string]string) {
	m := videoMeta{ItemRef: string(ref)}
	names := map[string]string{}
	it, ok := d.Catalog.Cached(ref)
	if !ok || withEpisodes[it.Kind] {
		rctx, cancel := context.WithTimeout(ctx, metaWait)
		defer cancel()
		if dt, err := d.Catalog.Item(rctx, agent.CatalogItem{Ref: ref}); err == nil {
			if !ok {
				it = dt.Item
			}
			for _, c := range dt.Children {
				if c.Episode != nil && c.Episode.Label != "" && c.Episode.Name != "" {
					names[c.Episode.Label] = clipText(c.Episode.Name, 128)
				}
			}
		}
	}
	m.Title = clipText(it.Title, 256)
	if it.Show != nil && it.Show.Names.RU != "" {
		// the name the screen's cards show
		m.Title = clipText(it.Show.Names.RU, 256)
	}
	if e := it.Episode; e != nil {
		// an episode's own ref: the show's name, and the episode's under its label
		if series := d.seriesName(ref); series != "" {
			m.Title = series
		}
		if e.Label != "" && e.Name != "" {
			names[e.Label] = clipText(e.Name, 128)
		}
	}
	provider := it.Provider
	if provider == "" {
		provider = it.Key.Provider
	}
	kind := it.Kind
	if kind == "" {
		kind = it.Key.Kind
	}
	switch {
	case it.Music != nil && it.Music.Artist != "":
		m.Artist = clipText(it.Music.Artist, 256)
	case kind == catalog.KindVideo || kind == catalog.KindLive:
		// a video's subtitle is its channel
		m.Channel = clipText(it.Subtitle, 256)
	}
	m.Provider, m.ProviderName = providerOf(provider, kind)
	if imageID.MatchString(it.Artwork.Hash) {
		m.artwork = it.Artwork.Hash
	}
	if it.Source != "" {
		m.ProviderSource = clipText(string(it.Source), 64)
	}
	if d.ImageURL != nil && imageID.MatchString(it.Artwork.Hash) {
		if u := d.ImageURL(it.Artwork.Hash); httpURL.MatchString(u) && len(u) <= 2048 {
			m.ArtworkURL = u
		}
	}
	return m, names
}

// providerOf is a catalog provider as the screen's badge names it (cadence.v2 MediaProvider: a token and a name); a
// YouTube video reads «YouTube», its music «YouTube Music»
func providerOf(p catalog.ProviderID, kind catalog.Kind) (string, string) {
	switch p {
	case catalog.ProviderYouTubeMusic:
		if kind == catalog.KindVideo || kind == catalog.KindLive || kind == catalog.KindChannel {
			return "youtube", "YouTube"
		}
		return "youtube_music", "YouTube Music"
	case catalog.ProviderTorrplayer:
		return "torrplayer", "torrplayer"
	case catalog.ProviderTorrServer:
		return "torrserver", "TorrServer"
	case catalog.ProviderSpotify:
		return "spotify", "Spotify"
	}
	return "", ""
}

// imageID is the image proxy's id, as the screen fetches it
var imageID = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)

// withEpisodes are the kinds whose plays name an episode of the item
var withEpisodes = map[catalog.Kind]bool{
	catalog.KindTitleGroup: true, catalog.KindSeries: true, catalog.KindSeason: true,
}
