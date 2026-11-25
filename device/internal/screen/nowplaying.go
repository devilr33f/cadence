package screen

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg" // the covers airplay hands over: jpeg or png
	_ "image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the sessions audiod renders that the screen shows in audio mode; the screen's own player's (VIDEO), mirroring and
// other planes' are not its NowPlaying. a multiroom group's (GROUP) is the leader's session played here: its
// transport goes to audiod as any other's, whose group source hands it to the leader (sync Control)
var nowPlayingSources = map[cadencev2.MediaSource]bool{
	cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY: true, cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST: true,
	cadencev2.MediaSource_MEDIA_SOURCE_URL: true, cadencev2.MediaSource_MEDIA_SOURCE_STREAM: true,
	cadencev2.MediaSource_MEDIA_SOURCE_GROUP: true, cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH: true,
}

// the transport a NowPlaying names, and what the screen's NowPlayingCommand asks, as media.player names them
var nowPlayingActions = map[string]screenv1.MediaAction{
	"pause": screenv1.MediaAction_MEDIA_ACTION_PAUSE, "resume": screenv1.MediaAction_MEDIA_ACTION_RESUME,
	"stop": screenv1.MediaAction_MEDIA_ACTION_STOP, "seek": screenv1.MediaAction_MEDIA_ACTION_SEEK,
	"next": screenv1.MediaAction_MEDIA_ACTION_NEXT, "previous": screenv1.MediaAction_MEDIA_ACTION_PREVIOUS,
}

// a screen command gets this long, as a media.player command does
const nowPlayingWait = 10 * time.Second

// nowPlaying is the device's session as the screen shows it, and the cover file written for it. what it holds is
// memory-only and never logged (D11); the cover file lives in the board's run dir, replaced by the next one
type nowPlaying struct {
	dir string

	mu   sync.Mutex
	last *screenv1.NowPlaying
	// the latest local view, for a cover written later
	local media.Local
	// the cover on disk, and the one being written
	cover   coverFile
	writing string
}

type coverFile struct {
	sha, path      string
	colorA, colorB uint32
}

// localSession takes the media plane's local view (media.UseLocal): it never blocks, so a new cover is written and
// its colours read on a goroutine of its own, and the session goes out again once it is on disk
func (s *Server) localSession(l media.Local) {
	np := s.nowPlaying
	np.mu.Lock()
	np.local = l
	if c := l.Cover; c != nil && np.dir != "" && c.SHA256 != np.cover.sha && c.SHA256 != np.writing {
		np.writing = c.SHA256
		go s.writeCover(c)
	}
	env := np.envelopeLocked()
	np.mu.Unlock()
	if env != nil {
		s.send(env)
	}
}

// envelopeLocked is the NowPlaying the latest local view makes, nil when it is the one the screen has
func (np *nowPlaying) envelopeLocked() *screenv1.Envelope {
	st := np.local.State
	n := &screenv1.NowPlaying{}
	if st != nil && nowPlayingSources[st.GetSource()] && st.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE &&
		st.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_UNSPECIFIED {
		md := st.GetMetadata()
		n = &screenv1.NowPlaying{
			SessionId: st.GetSessionId(), Source: st.GetSource(), State: st.GetState(), PositionMs: st.GetPositionMs(),
			PositionAtMs: st.GetPositionAtMs(), DurationMs: st.GetDurationMs(), Title: md.GetTitle(),
			Artist: md.GetArtist(), Album: md.GetAlbum(), Sender: np.local.Sender,
		}
		for _, a := range st.GetActions() {
			if wa, ok := nowPlayingActions[a]; ok {
				n.Actions = append(n.Actions, wa)
			}
		}
		if c := np.local.Cover; c != nil && c.SHA256 == np.cover.sha {
			n.ArtworkPath, n.ArtworkSha256 = np.cover.path, np.cover.sha
			n.ColorA, n.ColorB = np.cover.colorA, np.cover.colorB
		}
	}
	if np.last != nil && proto.Equal(np.last, n) {
		return nil
	}
	np.last = n
	return &screenv1.Envelope{Body: &screenv1.Envelope_NowPlaying{NowPlaying: n}}
}

// attached is what a screen that (re)connects gets: the session as it stands, sent whatever the last one was
func (np *nowPlaying) attached() *screenv1.Envelope {
	np.mu.Lock()
	defer np.mu.Unlock()
	np.last = nil
	return np.envelopeLocked()
}

// writeCover puts the cover where the screen reads it (mode 600: the screen runs as cadenced's user) under a name of
// its hash, removes the one before, and sends the session again with it
func (s *Server) writeCover(c *media.Cover) {
	np := s.nowPlaying
	a, b := coverColors(c.Data)
	ext := ".jpg"
	if c.Type == "image/png" {
		ext = ".png"
	}
	path := filepath.Join(np.dir, "cover-"+c.SHA256[:16]+ext)
	err := os.MkdirAll(np.dir, 0o700)
	if err == nil {
		tmp := path + ".new"
		if err = os.WriteFile(tmp, c.Data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	np.mu.Lock()
	if np.writing == c.SHA256 {
		np.writing = ""
	}
	if err != nil {
		np.mu.Unlock()
		s.log.Warn("screen_cover_failed")
		return
	}
	old := np.cover.path
	np.cover = coverFile{sha: c.SHA256, path: path, colorA: a, colorB: b}
	env := np.envelopeLocked()
	np.mu.Unlock()
	if old != "" && old != path {
		_ = os.Remove(old)
	}
	if env != nil {
		s.send(env)
	}
}

// coverColors are two colours of a cover, 0xRRGGBB: its most vivid tone (the cells' average weighted by how saturated
// and bright each is) and its overall tone; 0, 0 for an image that does not decode
func coverColors(data []byte) (uint32, uint32) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	r := img.Bounds()
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return 0, 0
	}
	const grid = 12
	var vr, vg, vb, vw, ar, ag, ab float64
	for gy := range grid {
		for gx := range grid {
			// one sample per cell: enough for a tint, and cheap on a tv's cpu
			x := r.Min.X + (2*gx+1)*r.Dx()/(2*grid)
			y := r.Min.Y + (2*gy+1)*r.Dy()/(2*grid)
			cr, cg, cb, _ := img.At(x, y).RGBA()
			fr, fg, fb := float64(cr>>8), float64(cg>>8), float64(cb>>8)
			ar, ag, ab = ar+fr, ag+fg, ab+fb
			hi, lo := max(fr, fg, fb), min(fr, fg, fb)
			sat := 0.0
			if hi > 0 {
				sat = (hi - lo) / hi
			}
			w := sat*sat*(hi/255) + 0.001
			vr, vg, vb, vw = vr+fr*w, vg+fg*w, vb+fb*w, vw+w
		}
	}
	n := float64(grid * grid)
	pack := func(r, g, b float64) uint32 {
		return uint32(min(r, 255))<<16 | uint32(min(g, 255))<<8 | uint32(min(b, 255))
	}
	return pack(vr/vw, vg/vw, vb/vw), pack(ar/n, ag/n, ab/n)
}

// nowPlayingCommand carries out the screen's transport on the session it shows, as media.player's command; one for a
// session that is no longer the one is dropped. its outcome shows in the next NowPlaying
func (s *Server) nowPlayingCommand(c *screenv1.NowPlayingCommand) {
	np := s.nowPlaying
	np.mu.Lock()
	current := np.last.GetSessionId()
	np.mu.Unlock()
	var action string
	for name, a := range nowPlayingActions {
		if a == c.GetAction() {
			action = name
		}
	}
	if s.o.Media == nil || action == "" || current == "" || c.GetSessionId() != current {
		s.log.Info("screen_now_playing_dropped")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), nowPlayingWait)
		defer cancel()
		if err := s.o.Media.Transport(ctx, action, c.GetPositionMs()); err != nil {
			code := "failed"
			if ce := (*capability.CommandError)(nil); errors.As(err, &ce) {
				code = ce.Code
			}
			s.log.Info("screen_now_playing_failed", "action", action, "code", code)
		}
	}()
}
