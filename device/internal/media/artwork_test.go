package media_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type uploads struct {
	mu    sync.Mutex
	got   []string
	types []string
	fail  bool
}

func (u *uploads) upload(_ context.Context, hash, typ string, _ []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.got = append(u.got, hash)
	u.types = append(u.types, typ)
	if u.fail {
		return errors.New("status_503")
	}
	return nil
}

func (u *uploads) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.got)
}

func airplayState(id string) *audiodv1.MediaState {
	return &audiodv1.MediaState{
		SessionId: "ms-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
		Metadata: &audiodv1.MediaMetadata{Title: "Song", ArtworkId: id},
	}
}

// a new cover goes out without its hash, is uploaded once, then the latest state goes out again with the hash; a
// later state of the same cover carries it at once
func TestArtworkUploadedThenNamed(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "artwork")
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(strings.Repeat("p", 100))...)
	if err := os.WriteFile(path, jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}
	r.m.UseArtwork(path, u.upload)
	sum := sha256.Sum256(jpeg)
	want := hex.EncodeToString(sum[:])

	r.mediaState(airplayState("a1"))
	if s := session(t, r.m); s.GetMetadata().GetArtworkSha256() != "" || s.GetMetadata().GetTitle() != "Song" {
		t.Fatalf("first state %v", s)
	}
	if s := session(t, r.m); s.GetMetadata().GetArtworkSha256() != want || s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY {
		t.Fatalf("after the upload %v", s)
	}
	r.mediaState(airplayState("a1"))
	if s := session(t, r.m); s.GetMetadata().GetArtworkSha256() != want {
		t.Fatalf("same cover %v", s)
	}
	if u.count() != 1 || u.types[0] != "image/jpeg" || u.got[0] != want {
		t.Fatalf("uploads %v %v", u.got, u.types)
	}
	// no cover: none named
	r.mediaState(airplayState(""))
	if s := session(t, r.m); s.GetMetadata().GetArtworkSha256() != "" {
		t.Fatalf("no cover %v", s)
	}
	if strings.Contains(r.log.String(), want) {
		t.Fatal("the hash reached the log")
	}
}

// a file that is no image is never uploaded, and a failed upload is not tried again for the same cover
func TestArtworkRefusedOrFailed(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "artwork")
	if err := os.WriteFile(path, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &uploads{fail: true}
	r.m.UseArtwork(path, u.upload)
	r.mediaState(airplayState("a1"))
	session(t, r.m)
	eventually(t, "unusable logged", func() bool { return strings.Contains(r.log.String(), `"msg":"artwork_unusable"`) })
	if u.count() != 0 {
		t.Fatal("an svg was uploaded")
	}

	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nxx"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.mediaState(airplayState("a2"))
	session(t, r.m)
	eventually(t, "the failure logged", func() bool {
		return strings.Contains(r.log.String(), `"msg":"artwork_upload_failed","code":"status_503"`)
	})
	r.mediaState(airplayState("a2"))
	if s := session(t, r.m); s.GetMetadata().GetArtworkSha256() != "" {
		t.Fatalf("failed cover named %v", s)
	}
	time.Sleep(50 * time.Millisecond)
	if u.count() != 1 {
		t.Fatalf("%d uploads, want 1", u.count())
	}
}

// without a client nothing is uploaded and states go out as they come
func TestNoUploaderWithoutAClient(t *testing.T) {
	var none *nilPutter
	if media.ServerUploader(none) != nil {
		t.Fatal("a nil client made an uploader")
	}
}

type nilPutter struct{}

func (*nilPutter) Put(context.Context, string, string, []byte) (int, error) { return 0, nil }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
