package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"google.golang.org/protobuf/proto"
)

// a cover the server serves may be larger than an upload: it may come from an artwork url
const maxServerCoverBytes = 4 << 20

// a cover the server may not have yet (a group's stream source uploads its airplay cover after its state, a member's
// state may reach the server before the source's) is asked again after each of these, then left until the item changes
// about two minutes: the source uploads its cover after its state, and again after a server restart once it relinks
var serverCoverRetries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second,
	30 * time.Second, 60 * time.Second}

const serverCoverWait = 10 * time.Second

// Getter is the device's GET of the server's https routes with its token and header; an answer other than 200 is an
// error.
type Getter interface {
	GetWith(ctx context.Context, path string, header http.Header) (io.ReadCloser, error)
}

// itemHeader names the item a cover is asked for: the hex SHA-256 of its title, a NUL and its artist
const itemHeader = "X-Cadence-Item"

// serverCovers takes the local view's cover from the server when audiod handed none: a multiroom member's (its
// stream source's: an airplay upload, an artwork url) and an artwork url's (a url's, soloist's). the server answers
// the cover of this device's session as it sees it (GET /v2/session-artwork), checked against the title and artist
// the device names. memory and the screen's run dir only (D11); logs carry nothing of it
type serverCovers struct {
	get Getter

	mu sync.Mutex
	// the item the cover is for, or is being fetched for; "" for none
	key    string
	cover  *Cover
	cancel context.CancelFunc
}

// UseServerCovers has the local view (UseLocal) take a session's cover from the server through get when audiod named
// none of its own; nil (or a nil client) takes none. Call it before audiod connects.
func (m *Media) UseServerCovers(get Getter) {
	if v := reflect.ValueOf(get); get == nil || v.Kind() == reflect.Pointer && v.IsNil() {
		return
	}
	m.srv = &serverCovers{get: get}
}

// coverItem keys what a state plays for a server cover: "" when it is none the server would have one of (no session,
// audiod's own cover, the screen's own player)
func coverItem(s *cadencev2.MediaSessionState, artworkID string) string {
	if s == nil || artworkID != "" || !nowPlaying(s) {
		return ""
	}
	md := s.GetMetadata()
	if s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_GROUP && md.GetArtworkUrl() == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(serverText(md.GetTitle()) + "\x00" + serverText(md.GetArtist())))
	return hex.EncodeToString(sum[:]) + "\x00" + md.GetArtworkUrl() + "\x00" + md.GetAlbum()
}

// nowPlaying is a session audiod renders that plays or is paused
func nowPlaying(s *cadencev2.MediaSessionState) bool {
	switch s.GetSource() {
	case cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST,
		cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL, cadencev2.MediaSource_MEDIA_SOURCE_UNSPECIFIED:
		return false
	}
	st := s.GetState()
	return st != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE && st != cadencev2.PlaybackState_PLAYBACK_STATE_UNSPECIFIED
}

// wantServerCover starts the fetch of a new item's cover, and stops one for an item no longer played; callers hold
// stateMu
func (m *Media) wantServerCover(s *cadencev2.MediaSessionState, artworkID string) {
	c := m.srv
	if c == nil || m.local == nil {
		return
	}
	key := coverItem(s, artworkID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.key {
		return
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.key, c.cover = key, nil
	if key == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	item, _, _ := strings.Cut(key, "\x00")
	go m.fetchServerCover(ctx, key, item)
}

// serverCover is the cover fetched for the latest state, if it was; callers hold stateMu
func (m *Media) serverCover() *Cover {
	c := m.srv
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == "" || c.key != coverItem(m.latest, m.latestArt) {
		return nil
	}
	return c.cover
}

func (m *Media) fetchServerCover(ctx context.Context, key, item string) {
	c := m.srv
	for attempt := 0; ; attempt++ {
		cover, err := m.getServerCover(ctx, item)
		if err == nil {
			c.mu.Lock()
			current := c.key == key
			if current {
				c.cover, c.cancel = cover, nil
			}
			c.mu.Unlock()
			if !current {
				return
			}
			m.stateMu.Lock()
			m.notifyLocal()
			m.stateMu.Unlock()
			return
		}
		if attempt >= len(serverCoverRetries) || ctx.Err() != nil {
			if ctx.Err() == nil {
				m.log.Info("server_cover_unavailable")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(serverCoverRetries[attempt]):
		}
	}
}

func (m *Media) getServerCover(ctx context.Context, item string) (*Cover, error) {
	ctx, cancel := context.WithTimeout(ctx, serverCoverWait)
	defer cancel()
	body, err := m.srv.get.GetWith(ctx, "/v2/session-artwork", http.Header{itemHeader: {item}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxServerCoverBytes+1))
	if err != nil || len(data) > maxServerCoverBytes {
		return nil, errArtwork
	}
	typ := coverType(data)
	if typ == "" {
		return nil, errArtwork
	}
	sum := sha256.Sum256(data)
	return &Cover{SHA256: hex.EncodeToString(sum[:]), Type: typ, Data: data}, nil
}

// serverText is a title or an artist as the server's view keeps it: one past its 255 bytes is dropped
func serverText(s string) string {
	if len(s) > 255 {
		return ""
	}
	return s
}

// Relinked runs on each new link. The server keeps covers in memory alone, so after its restart the latest cover is
// uploaded again (a source's), and a fetch of the source's cover starts again (a member's)
func (m *Media) Relinked() {
	if a := m.art; a != nil {
		a.mu.Lock()
		a.doneID, a.doneSHA, a.triedID = "", "", ""
		a.mu.Unlock()
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.latest == nil {
		return
	}
	if m.art != nil {
		m.withArtwork(proto.Clone(m.latest).(*cadencev2.MediaSessionState), m.latestArt)
	}
	if c := m.srv; c != nil {
		c.mu.Lock()
		if c.cancel != nil {
			c.cancel()
			c.cancel = nil
		}
		c.key = ""
		c.mu.Unlock()
		m.wantServerCover(m.latest, m.latestArt)
	}
}
