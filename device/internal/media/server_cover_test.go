package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// fakeCovers is the server's GET /v2/session-artwork: the cover it holds for an item, else a refusal
type fakeCovers struct {
	mu     sync.Mutex
	covers map[string][]byte
	asked  []string
}

func (f *fakeCovers) GetWith(_ context.Context, path string, h http.Header) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := h.Get("X-Cadence-Item")
	f.asked = append(f.asked, path+" "+item)
	if b, ok := f.covers[item]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, errors.New("status")
}

func (f *fakeCovers) put(item string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.covers[item] = b
}

func (f *fakeCovers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func itemOf(title, artist string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + artist))
	return hex.EncodeToString(sum[:])
}

func groupState(title string) *audiodv1.MediaState {
	return &audiodv1.MediaState{
		SessionId: "g-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_GROUP, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
		Metadata: &audiodv1.MediaMetadata{Title: title, Artist: "Bokka"},
	}
}

// a group member's screen shows the cover the server serves for its session (the stream source's), asked again while
// the server has none yet; the next song drops it until its own comes; nothing of it reaches the log or the server's
// state
func TestGroupMemberTakesTheServersCover(t *testing.T) {
	r := newRig(t)
	seen := &localSeen{}
	r.m.UseLocal(seen.hear)
	srv := &fakeCovers{covers: map[string][]byte{}}
	r.m.UseServerCovers(srv)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("p", 32))

	r.mediaState(groupState("Town of Strangers"))
	eventually(t, "a first ask", func() bool { return srv.count() >= 1 })
	if seen.last().Cover != nil {
		t.Fatal("a cover before the server had one")
	}
	srv.put(itemOf("Town of Strangers", "Bokka"), png)
	eventually(t, "the cover asked again and shown", func() bool { return seen.last().Cover != nil })
	sum := sha256.Sum256(png)
	if c := seen.last().Cover; c.SHA256 != hex.EncodeToString(sum[:]) || c.Type != "image/png" || !bytes.Equal(c.Data, png) {
		t.Fatalf("cover %+v", c)
	}
	if srv.asked[0] != "/v2/session-artwork "+itemOf("Town of Strangers", "Bokka") {
		t.Fatalf("asked %v", srv.asked)
	}
	if session(t, r.m).GetMetadata().GetArtworkSha256() != "" {
		t.Fatal("a fetched cover went to the server's state")
	}
	// a pause of the same song keeps it
	st := groupState("Town of Strangers")
	st.State = audiodv1.PlaybackState_PLAYBACK_STATE_PAUSED
	r.mediaState(st)
	if seen.last().Cover == nil {
		t.Fatal("a pause dropped the cover")
	}
	// the next song: none until its own
	r.mediaState(groupState("Next"))
	if seen.last().Cover != nil {
		t.Fatal("the last song's cover on the next")
	}
	srv.put(itemOf("Next", "Bokka"), png)
	eventually(t, "the next song's cover", func() bool { return seen.last().Cover != nil })
	if strings.Contains(r.log.String(), "Town") || strings.Contains(r.log.String(), "Bokka") {
		t.Fatal("metadata reached the log")
	}
}

// a session with its own cover from audiod, or without a url outside a group, asks the server nothing; neither does a
// device without a screen
func TestNoServerCoverAsked(t *testing.T) {
	r := newRig(t)
	srv := &fakeCovers{covers: map[string][]byte{}}
	r.m.UseServerCovers(srv)
	r.mediaState(groupState("Song"))
	seen := &localSeen{}
	r.m.UseLocal(seen.hear)
	r.mediaState(airplayState("a1"))
	r.mediaState(&audiodv1.MediaState{
		SessionId: "u", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
		Metadata: &audiodv1.MediaMetadata{Title: "radio"},
	})
	if srv.count() != 0 {
		t.Fatalf("asked %v", srv.asked)
	}
	var none *fakeCovers
	media.New(nil).UseServerCovers(none)
}
