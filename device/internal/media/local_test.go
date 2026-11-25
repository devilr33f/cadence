package media_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type localSeen struct {
	mu  sync.Mutex
	got []media.Local
}

func (l *localSeen) hear(v media.Local) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, v)
}

func (l *localSeen) last() media.Local {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.got) == 0 {
		return media.Local{}
	}
	return l.got[len(l.got)-1]
}

// the screen on the device sees an airplay cast with its sender's name, and its cover once read, without any server
// (no uploader): neither reaches the server's state nor the log
func TestLocalViewHasSenderAndCover(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "artwork")
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(strings.Repeat("c", 64))...)
	if err := os.WriteFile(path, jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	seen := &localSeen{}
	r.m.UseLocal(seen.hear)
	r.m.UseArtwork(path, nil)
	sum := sha256.Sum256(jpeg)
	want := hex.EncodeToString(sum[:])

	st := airplayState("a1")
	st.Metadata.Sender = "iPhone Маши"
	r.mediaState(st)
	s := session(t, r.m)
	if s.GetMetadata().GetArtworkSha256() != "" {
		t.Fatalf("a hash went to the server without an upload: %v", s)
	}
	eventually(t, "the cover read", func() bool { return seen.last().Cover != nil })
	l := seen.last()
	if l.Sender != "iPhone Маши" || l.State.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY ||
		l.State.GetMetadata().GetTitle() != "Song" || l.Cover.SHA256 != want || l.Cover.Type != "image/jpeg" ||
		string(l.Cover.Data) != string(jpeg) {
		t.Fatalf("local %+v", l)
	}
	if strings.Contains(r.log.String(), "iPhone") || strings.Contains(r.log.String(), want) {
		t.Fatal("the sender or the hash reached the log")
	}

	// audiod gone: no session
	r.m.MediaGone()
	if l := seen.last(); l.State.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE || l.Sender != "" || l.Cover != nil {
		t.Fatalf("after audiod went %+v", l)
	}
}

// the screen's controls go to audiod as media.player's commands would
func TestTransportIsThePlayersCommand(t *testing.T) {
	r := newRig(t)
	r.up(t)
	done := make(chan error, 1)
	go func() { done <- r.m.Transport(context.Background(), "seek", 42000) }()
	env := r.next(t)
	mc := env.GetMediaCommand()
	if mc.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_SEEK || mc.GetPositionMs() != 42000 {
		t.Fatalf("sent %v", env)
	}
	r.answer(mc.GetRequestId(), true, "")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	go func() { done <- r.m.Transport(context.Background(), "pause", 0) }()
	pause := r.next(t).GetMediaCommand()
	if pause.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_PAUSE {
		t.Fatal("no pause")
	}
	r.answer(pause.GetRequestId(), false, "no_session")
	if code(wait(t, done)) != "no_session" {
		t.Fatal("the code was not passed on")
	}
}

// a watcher (the group's localSource) hears every session the server gets, and an idle one once audiod goes
func TestWatchSessionHearsEveryChange(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var got []*cadencev2.MediaSessionState
	r.m.WatchSession(func(s *cadencev2.MediaSessionState) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	})
	last := func() *cadencev2.MediaSessionState {
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			return nil
		}
		return got[len(got)-1]
	}
	r.mediaState(airplayState("a1"))
	if s := last(); s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY {
		t.Fatalf("watched %v", s)
	}
	r.mediaState(&audiodv1.MediaState{SessionId: "g1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_GROUP,
		State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING})
	if s := last(); s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_GROUP {
		t.Fatalf("watched %v", s)
	}
	r.m.MediaGone()
	if s := last(); s.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE || s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_UNSPECIFIED {
		t.Fatalf("after audiod went %v", s)
	}
}
