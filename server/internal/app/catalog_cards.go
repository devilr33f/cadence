package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/screens"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// the catalog tools' cards on a turn's screens: a search's poster tiles, and a play's card that follows the screen
// until it plays or gives up

const (
	// a play the screen never reported in this long did not start
	playUnseen = 45 * time.Second
	// how long a play's card waits for the screen at all
	playFollow = 2 * time.Minute
	// the catalog read behind an episode's name
	episodeNameWait = 10 * time.Second
	episodeNames    = 200
)

// catalogFound attaches a catalog search's titles to the turn on the room's screens; OK on a tile opens the title
func (p screenParts) catalogFound(open cards.CatalogOpen) func(agent.Call, []catalog.Item) {
	return func(c agent.Call, items []catalog.Item) {
		tiles := make([]cards.CatalogTile, 0, len(items))
		for _, it := range items {
			tiles = append(tiles, catalogTile(it))
		}
		if subj := cards.CatalogFound(c.TurnID+":"+rand.Text()[:8], tiles, open); subj != nil {
			p.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
		}
	}
}

// youtubeFound attaches a youtube search's items to the turn on the room's screens: its videos as wide tiles, OK on
// one playing it on the screen that pressed (a deliberate press, the play its own remote makes), its channels and
// playlists as lines below
func (p screenParts) youtubeFound(play calls.Method) func(agent.Call, []catalog.Item) {
	return func(c agent.Call, items []catalog.Item) {
		var tiles []cards.VideoTile
		var rows []cards.VideoRow
		for _, it := range items {
			switch it.Kind {
			case catalog.KindVideo, catalog.KindLive:
				t := cards.VideoTile{Ref: string(it.Ref), Title: it.Title, DurationS: it.DurationS,
					Live: it.Kind == catalog.KindLive, Artwork: it.Artwork.Hash}
				if it.Owner != nil {
					t.Channel = it.Owner.Title
				}
				if it.Stats != nil {
					t.Views = it.Stats.Views
				}
				tiles = append(tiles, t)
			case catalog.KindChannel, catalog.KindPlaylist:
				r := cards.VideoRow{Title: it.Title, Handle: it.Subtitle}
				if it.Stats != nil {
					r.Subscribers, r.Videos = it.Stats.Subscribers, it.Stats.VideoCount
				}
				rows = append(rows, r)
			}
		}
		if subj := cards.VideosFound(c.TurnID+":"+rand.Text()[:8], tiles, rows, videoOpen(play)); subj != nil {
			p.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
		}
	}
}

// videoOpen plays a found video on the screen that pressed its tile: the screen's own media.play, which resolves
// the ref and never touches the volume
func videoOpen(method calls.Method) cards.VideoOpen {
	return func(ctx context.Context, deviceID, ref string) error {
		raw, err := json.Marshal(tools.MediaPlayArgs{Ref: catalog.Ref(ref), Reason: "user"})
		if err != nil {
			return err
		}
		if reply := method(ctx, calls.Call{DeviceID: deviceID, Args: raw}); reply.Status != rpc.StatusOK {
			return errors.New(reply.Status)
		}
		return nil
	}
}

// catalogTile is a found item as its tile shows it: the name the screen's cards show, its format before its kind
func catalogTile(it catalog.Item) cards.CatalogTile {
	t := cards.CatalogTile{Ref: string(it.Ref), Title: it.Title, Kind: string(it.Kind), Artwork: it.Artwork.Hash,
		Unavailable: it.Unavailable}
	if s := it.Show; s != nil {
		if s.Names.RU != "" {
			t.Title = s.Names.RU
		}
		if s.Format != "" {
			t.Kind = strings.ToLower(s.Format)
		}
		t.Year, t.Score = s.Year, s.Score
		t.Episodes = max(s.EpisodesAired, s.EpisodesTotal)
	}
	return t
}

// openTitle opens a title's page on a screen, as screen_navigate's item does
func openTitle(send func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)) cards.CatalogOpen {
	return func(ctx context.Context, deviceID, ref string) error {
		raw, err := json.Marshal(map[string]string{"route": "title", "ref": ref})
		if err != nil {
			return err
		}
		_, err = send(ctx, deviceID, agent.CapScreen, "show", raw)
		return err
	}
}

// sessionSignal wakes whoever waits on a device's media session (media.Store's OnChange), any device's
type sessionSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

func newSessionSignal() *sessionSignal { return &sessionSignal{ch: make(chan struct{})} }

func (s *sessionSignal) Changed(string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.ch)
	s.ch = make(chan struct{})
}

func (s *sessionSignal) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ch
}

// mediaPlayHooks gives music_play and video_play one stable live card and a
// late outcome, and follows the target session to its first real picture.
func mediaPlayHooks(parts screenParts, sessions *media.Store, playHistory *history.Service, signal *sessionSignal, image *images.Proxy,
	record func(context.Context, tools.PlayRecord)) tools.PlayHooks {
	return tools.PlayHooks{
		Attach: func(call agent.Call, info playprogress.CardInfo) func(playprogress.Update) {
			art := ""
			if image != nil && info.ArtworkURL != "" {
				art = image.Ref(info.ArtworkURL, false)
			}
			card := cards.NewMediaPlay(info, art)
			parts.rooms.Attach(call.DeviceID, call.SessionID, screens.CallKey(call.TurnID, call.CallID), card)
			return func(u playprogress.Update) {
				// the song a list's play fetches now shows its own art, through the proxy too
				if u.Item != nil && image != nil && u.Item.ArtworkURL != "" {
					it := *u.Item
					it.ArtworkRef = image.Ref(it.ArtworkURL, false)
					u.Item = &it
				}
				card.Set(u)
			}
		},
		Record: record,
		Started: func(call agent.Call, info playprogress.CardInfo, started playprogress.Session) {
			if playHistory == nil {
				return
			}
			sess, ok := sessions.Get(info.DeviceID)
			if !ok || sess.SessionID != started.ID {
				return
			}
			playHistory.AttributeObserved(info.DeviceID, started.ID, sess, history.Attribution{
				TurnID: call.TurnID, ItemID: info.ItemID, Source: info.Source, Provider: info.Provider, Kind: info.MediaKind,
				Title: info.Title, Artist: info.Artist, Album: info.Album, ArtworkURL: info.ArtworkURL,
			})
		},
		Session: func(deviceID string) (playprogress.Session, bool) {
			s, ok := sessions.Raw(deviceID)
			if !ok {
				return playprogress.Session{}, false
			}
			percent := -1
			if s.Buffering != nil {
				percent = int(*s.Buffering)
			}
			ready := s.PictureReady || s.Source != "video" && (s.State == "playing" || s.State == "paused")
			return playprogress.Session{ID: s.SessionID, State: s.State, ErrorCode: s.ErrorCode, PictureReady: ready,
				BufferingPercent: percent}, true
		},
		Changes: signal.wait,
	}
}

// playCards builds a catalog play's card on the turn's screens
type playCards struct {
	parts       screenParts
	cat         agent.Catalog
	session     func(deviceID string) (media.MediaSession, bool)
	playHistory *history.Service
	signal      *sessionSignal
	now         func() time.Time
}

func (pc playCards) started(c agent.Call, sp tools.StartedPlay) {
	pc.attribute(c, sp)
	subj := cards.NewCatalogPlay(c.TurnID+":"+rand.Text()[:8], cards.CatalogPlaying{
		Title: sp.Title, Artwork: sp.ArtworkHash, Episode: sp.Episode, EpisodeName: sp.EpisodeName, Provider: sp.Provider, Source: sp.Source,
		Voiceover: sp.Voiceover, Quality: sp.Quality, Screen: sp.DeviceName,
	})
	pc.parts.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
	go pc.episodeName(subj, sp)
	go followPlay(context.Background(), subj, sp.DeviceID, sp.SessionID, pc.session, pc.signal.wait, pc.now)
}

// fetch attaches a catalog play's card from its call on: what was cached of the ref and a bar while the catalog
// resolves, then — on the same card, in place — what started and the screen's own reports (followPlay), music_play's
// real-time card for the catalog
func (pc playCards) fetch(c agent.Call, f tools.CatalogFetching) tools.CatalogFetch {
	subj := cards.NewCatalogFetch(c.TurnID+":"+rand.Text()[:8], cards.CatalogPlaying{
		Title: f.Title, Artwork: f.Artwork, Screen: f.Screen,
	})
	pc.parts.rooms.Attach(c.DeviceID, c.SessionID, screens.CallKey(c.TurnID, c.CallID), subj)
	return fetchCard{subj: subj, pc: pc, call: c}
}

// fetchCard is the tool's handle on the fetch's card
type fetchCard struct {
	subj *cards.CatalogPlay
	pc   playCards
	call agent.Call
}

func (f fetchCard) Progress(ev playprogress.Event) { f.subj.Stage(ev) }

func (f fetchCard) Fail(code string) { f.subj.Fail(code) }

// Started moves the card to what the fetch started, keeping its id, and lets the screen's reports carry it on
func (f fetchCard) Started(sp tools.StartedPlay) {
	f.pc.attribute(f.call, sp)
	f.subj.Start(cards.CatalogPlaying{
		Title: sp.Title, Artwork: sp.ArtworkHash, Episode: sp.Episode, EpisodeName: sp.EpisodeName, Provider: sp.Provider,
		Source: sp.Source, Voiceover: sp.Voiceover, Quality: sp.Quality, Screen: sp.DeviceName,
	})
	go f.pc.episodeName(f.subj, sp)
	if sp.SessionID != "" {
		go followPlay(context.Background(), f.subj, sp.DeviceID, sp.SessionID, f.pc.session, f.pc.signal.wait, f.pc.now)
	}
}

func (pc playCards) attribute(c agent.Call, sp tools.StartedPlay) {
	if pc.playHistory == nil {
		return
	}
	source := strings.ToLower(sp.Provider)
	if source == "" {
		source = "video"
	}
	kind := "video"
	if sp.Episode != "" {
		kind = "episode"
	}
	a := history.Attribution{TurnID: c.TurnID, ItemID: string(sp.Ref), Source: source, Provider: sp.Source,
		Kind: kind, Title: sp.Title}
	if sess, ok := pc.session(sp.DeviceID); ok && sess.SessionID == sp.SessionID {
		pc.playHistory.AttributeObserved(sp.DeviceID, sp.SessionID, sess, a)
		return
	}
	pc.playHistory.Attribute(sp.DeviceID, sp.SessionID, a)
}

// episodeName names the episode once the catalog answered its list
func (pc playCards) episodeName(subj *cards.CatalogPlay, sp tools.StartedPlay) {
	if pc.cat == nil || sp.Episode == "" || sp.Ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), episodeNameWait)
	defer cancel()
	det, err := pc.cat.Item(ctx, agent.CatalogItem{Ref: sp.Ref, Limit: episodeNames})
	if err != nil {
		return
	}
	for _, ch := range det.Children {
		if ch.Episode != nil && ch.Episode.Label == sp.Episode {
			subj.SetEpisodeName(ch.Episode.Name)
			return
		}
	}
}

// followPlay moves a play's card as the screen reports the session it made: loading, then playing; failed when the
// session ends before it played, or never comes
func followPlay(ctx context.Context, subj *cards.CatalogPlay, deviceID, sessionID string,
	session func(string) (media.MediaSession, bool), wait func() <-chan struct{}, now func() time.Time) {
	ctx, cancel := context.WithTimeout(ctx, playFollow)
	defer cancel()
	start, seen := now(), false
	for {
		changed := wait()
		s, ok := session(deviceID)
		switch {
		case ok && s.SessionID == sessionID:
			seen = true
			switch s.State {
			case "playing", "paused":
				subj.Set(cards.PlayPlaying)
				return
			case "buffering":
				subj.Set(cards.PlayBuffering)
			}
		case seen, now().Sub(start) > playUnseen:
			subj.Set(cards.PlayFailed)
			return
		}
		select {
		case <-changed:
		case <-time.After(time.Second):
		case <-ctx.Done():
			if !seen {
				subj.Set(cards.PlayFailed)
			}
			return
		}
	}
}

// catalogVoiceovers reads the household's preferred and avoided dubs (a global key) through a device's settings
func catalogVoiceovers(effective tools.SettingsFunc) func(ctx context.Context, deviceID string) settings.CatalogVoiceovers {
	return func(ctx context.Context, deviceID string) settings.CatalogVoiceovers {
		v, err := effective(ctx, deviceID)
		if err != nil {
			return settings.CatalogVoiceovers{}
		}
		return v.CatalogVoiceovers
	}
}
