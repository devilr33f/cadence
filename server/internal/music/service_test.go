package music_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// rig is the service over fakes: a player that resolves every video but the
// ones in refuse, a YouTube Music whose search and radio answer as set, a
// device that records each song it is sent and plays it as its session
type rig struct {
	t   *testing.T
	svc *music.Service

	mu       sync.Mutex
	now      time.Time
	refuse   map[string]error
	search   []providers.Track
	radio    []providers.Track
	radioErr error
	seeds    []string // the videos radio was asked for
	played   []string // video ids, in order
	sess     *media.MediaSession
	relayErr error
	image    func(string) string
	// the song's own provider (instance "sc"): what it refuses, the hls flags the relay got, its radio
	nativeErr      map[string]error
	relayedHLS     []bool
	nativeRadio    []providers.Track
	nativeRadioErr error
	nativeSeeds    []providers.Track
	heard          []providers.ListeningEvent
}

func (n rigNative) ReportListening(_ context.Context, ev providers.ListeningEvent) error {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	n.r.heard = append(n.r.heard, ev)
	return nil
}

// waitHeard waits for n listening events the provider got
func (r *rig) waitHeard(t *testing.T, n int) []providers.ListeningEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		got := append([]providers.ListeningEvent(nil), r.heard...)
		r.mu.Unlock()
		if len(got) >= n {
			return got[:n]
		}
		if time.Now().After(deadline) {
			t.Fatalf("heard %d of %d listening events: %+v", len(got), n, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type rigNative struct{ r *rig }

func (n rigNative) SearchMusic(context.Context, string, int) ([]providers.Track, error) {
	return nil, nil
}

func (n rigNative) StreamTrack(_ context.Context, t providers.Track) (providers.Stream, error) {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	if err := n.r.nativeErr[t.ID]; err != nil {
		return providers.Stream{}, err
	}
	return providers.Stream{URL: "https://media.example/hls/" + t.ID, HLS: true}, nil
}

func (n rigNative) TrackRadio(_ context.Context, seed providers.Track, _ int) ([]providers.Track, error) {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	n.r.nativeSeeds = append(n.r.nativeSeeds, seed)
	if n.r.nativeRadioErr != nil {
		return nil, n.r.nativeRadioErr
	}
	out := append([]providers.Track(nil), n.r.nativeRadio...)
	for i := range out {
		out[i].Station = seed.Station
	}
	return out, nil
}

type rigPlayer struct{ r *rig }

func (p rigPlayer) Resolve(_ context.Context, id string) (providers.Stream, error) {
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	if err := p.r.refuse[id]; err != nil {
		return providers.Stream{}, err
	}
	return providers.Stream{URL: "https://rr1.googlevideo.com/videoplayback?id=" + id + "&sig=SIGNED"}, nil
}

type rigYouTube struct{ r *rig }

func (y rigYouTube) SearchMusic(context.Context, string, int) ([]providers.Track, error) {
	return y.r.search, nil
}

func (y rigYouTube) Radio(_ context.Context, id string, _ int) ([]providers.Track, error) {
	y.r.mu.Lock()
	defer y.r.mu.Unlock()
	y.r.seeds = append(y.r.seeds, id)
	return y.r.radio, y.r.radioErr
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), refuse: map[string]error{}, nativeErr: map[string]error{}}
	r.svc = music.New(music.Deps{
		Settings: func(context.Context, string) (settings.Values, error) {
			return settings.Values{Music: settings.Names{"ytm", "sc"}, MusicPlayer: "ytdlp"}, nil
		},
		Player: func(_ context.Context, name string) (providers.Player, error) {
			if name != "ytdlp" {
				return nil, errors.New("no instance")
			}
			return rigPlayer{r}, nil
		},
		YouTube: rigYouTube{r},
		Music: func(_ context.Context, name string) (providers.Music, error) {
			if name != "sc" {
				return nil, errors.New("no instance")
			}
			return rigNative{r}, nil
		},
		Stream: func(_ string, upstream providers.Stream) (string, error) {
			if r.relayErr != nil {
				return "", r.relayErr
			}
			r.relayedHLS = append(r.relayedHLS, upstream.HLS)
			if id, ok := strings.CutPrefix(upstream.URL, "https://media.example/hls/"); ok {
				return "https://cadence.example/v2/stream/" + id, nil
			}
			return "https://cadence.example/v2/stream/" + strings.TrimPrefix(upstream.URL, "https://rr1.googlevideo.com/videoplayback?id=")[:11], nil
		},
		Command: func(_ context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
			if capability != "media.url" || name != "play" || deviceID != "d1" {
				t.Errorf("command %s %s %s", deviceID, capability, name)
			}
			var a struct {
				URL      string
				Mode     string
				Metadata struct{ Title, Artist, ArtworkURL string }
			}
			_ = json.Unmarshal(args, &a)
			if strings.Contains(string(args), "googlevideo") || a.Mode != "session" {
				t.Errorf("play args %s", args)
			}
			r.mu.Lock()
			r.played = append(r.played, a.URL[len(a.URL)-11:])
			r.sess = &media.MediaSession{
				SessionID: "s" + a.URL[len(a.URL)-11:], Source: "url", State: "playing", DurationMs: 180_000,
				PositionAt: r.now.Format(time.RFC3339Nano), Metadata: media.MediaMetadata{Title: a.Metadata.Title},
				Actions: []string{"pause", "resume", "stop", "seek"},
			}
			r.mu.Unlock()
			return nil, nil
		},
		Session: func(string) (media.MediaSession, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.sess == nil {
				return media.MediaSession{}, false
			}
			return *r.sess, true
		},
		Now: func() time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.now
		},
		Image: func(raw string) string {
			if r.image != nil {
				return r.image(raw)
			}
			return raw
		},
	})
	t.Cleanup(r.svc.StopFeedback)
	return r
}

func (r *rig) playedIDs() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.played, ",")
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func track(id string) providers.Track {
	return providers.Track{
		Source: providers.SourceYouTube, ID: id, VideoID: id, Title: "Song " + id, Artist: "A",
		DurationS: 180, WebURL: "https://music.youtube.com/watch?v=" + id,
	}
}

func ids(n ...string) []providers.Track {
	out := make([]providers.Track, len(n))
	for i, id := range n {
		out[i] = track(id)
	}
	return out
}

// 11-char video ids
const (
	a = "aaaaaaaaaaa"
	b = "bbbbbbbbbbb"
	c = "ccccccccccc"
	x = "xxxxxxxxxxx"
	y = "yyyyyyyyyyy"
)

func TestPlaySendsTheRelayedStreamAndSkips(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.svc.Play(ctx, "d1", ids(a, b, c), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Skip(ctx, "d1", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Skip(ctx, "d1", -1); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != a+","+b+","+a {
		t.Fatalf("played %s", got)
	}
	if err := r.svc.Skip(ctx, "d1", -1); !errors.Is(err, music.ErrQueueEnd) {
		t.Fatalf("before the first: %v", err)
	}
	v := r.svc.State("d1")
	if !v.Active || v.Current.Title != "Song "+a || len(v.Upcoming) != 2 || v.Current.Provider != "YouTube Music" ||
		v.Current.WebURL != "https://music.youtube.com/watch?v="+a {
		t.Fatalf("view %+v", v)
	}
}

func TestPlaySkipsSongsThatWillNotPlay(t *testing.T) {
	r := newRig(t)
	r.refuse[a] = providers.ErrSignInRequired
	r.refuse[b] = providers.ErrUnplayable
	if err := r.svc.Play(context.Background(), "d1", ids(a, b, c), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != c {
		t.Fatalf("played %s", got)
	}
	r2 := newRig(t)
	r2.refuse[a] = providers.ErrUnplayable
	if err := r2.svc.Play(context.Background(), "d1", ids(a), 0, music.Options{}); !errors.Is(err, providers.ErrUnplayable) {
		t.Fatalf("lone unplayable: %v", err)
	}
	if v := r2.svc.State("d1"); v.Current != nil {
		t.Fatal("a queue that never played was kept")
	}
}

func TestRepeatAndRadio(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.svc.SetRepeat("d1", music.RepeatAll)
	_ = r.svc.Play(ctx, "d1", ids(a, b), 0, music.Options{})
	_ = r.svc.Skip(ctx, "d1", 1)
	if err := r.svc.Skip(ctx, "d1", 1); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != a+","+b+","+a {
		t.Fatalf("repeat all: %s", got)
	}
	// radio tops the queue up at its end, leaving out what it holds
	r2 := newRig(t)
	r2.radio = ids(a, x, y)
	_ = r2.svc.Play(ctx, "d1", ids(a), 0, music.Options{Radio: true})
	if err := r2.svc.Skip(ctx, "d1", 1); err != nil {
		t.Fatal(err)
	}
	if got := r2.playedIDs(); got != a+","+x {
		t.Fatalf("radio: %s", got)
	}
	if v := r2.svc.State("d1"); len(v.Upcoming) != 1 || v.Upcoming[0].Title != "Song "+y || len(v.History) != 1 {
		t.Fatalf("view %+v", v)
	}
	// no radio, no repeat: the end
	r3 := newRig(t)
	_ = r3.svc.Play(ctx, "d1", ids(a), 0, music.Options{})
	if err := r3.svc.Skip(ctx, "d1", 1); !errors.Is(err, music.ErrQueueEnd) {
		t.Fatalf("%v", err)
	}
}

func TestShuffleKeepsThePlayingSong(t *testing.T) {
	r := newRig(t)
	_ = r.svc.Play(context.Background(), "d1", ids(a, b, c, x, y), 2, music.Options{})
	r.svc.SetShuffle("d1", true)
	v := r.svc.State("d1")
	if v.Current.Title != "Song "+c || len(v.Upcoming) != 4 || !v.Shuffle {
		t.Fatalf("view %+v", v)
	}
	r.svc.SetShuffle("d1", false)
	v = r.svc.State("d1")
	if v.Current.Title != "Song "+c || len(v.Upcoming) != 2 || v.Upcoming[0].Title != "Song "+x {
		t.Fatalf("unshuffled %+v", v)
	}
}

func TestAutoAdvanceOnlyWhenASongEndsByItself(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_ = r.svc.Play(ctx, "d1", ids(a, b, c), 0, music.Options{})
	r.advance(10 * time.Second)
	r.svc.SessionChanged("d1") // playing at 0 s of 180
	// stopped half way: nothing follows
	r.mu.Lock()
	r.sess.PositionMs = 90_000
	r.sess.PositionAt = r.now.Format(time.RFC3339Nano)
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	r.mu.Lock()
	r.sess = nil
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	time.Sleep(50 * time.Millisecond)
	if got := r.playedIDs(); got != a {
		t.Fatalf("a stop advanced: %s", got)
	}
	// played to its end: the next one plays
	_ = r.svc.Restart(ctx, "d1")
	r.advance(10 * time.Second)
	r.mu.Lock()
	r.sess.PositionMs = 175_000
	r.sess.PositionAt = r.now.Format(time.RFC3339Nano)
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	r.advance(5 * time.Second)
	r.mu.Lock()
	r.sess = nil
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	deadline := time.Now().Add(2 * time.Second)
	for r.playedIDs() != a+","+a+","+b && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.playedIDs(); got != a+","+a+","+b {
		t.Fatalf("played %s", got)
	}
}

func TestInterceptAndDecorateOnlyTheQueuesSong(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// nothing of the queue's: next goes to the device
	if _, handled, _ := r.svc.Intercept(ctx, "d1", "media.player", "next", nil); handled {
		t.Fatal("handled without a queue")
	}
	_ = r.svc.Play(ctx, "d1", ids(a, b), 0, music.Options{})
	if _, handled, err := r.svc.Intercept(ctx, "d1", "media.player", "next", nil); !handled || err != nil {
		t.Fatalf("handled %v err %v", handled, err)
	}
	if got := r.playedIDs(); got != a+","+b {
		t.Fatalf("played %s", got)
	}
	_, handled, err := r.svc.Intercept(ctx, "d1", "media.player", "next", nil)
	var device *commands.DeviceError
	if !handled || !errors.As(err, &device) || device.Code != "queue_end" {
		t.Fatalf("at the end: %v %v", handled, err)
	}
	if _, handled, _ := r.svc.Intercept(ctx, "d1", "media.player", "pause", nil); handled {
		t.Fatal("pause intercepted")
	}
	sess, _ := r.svc.State("d1"), 0
	_ = sess
	r.mu.Lock()
	s := *r.sess
	r.mu.Unlock()
	r.svc.Decorate("d1", &s)
	if s.Provider != "YouTube Music" || s.WebURL != "https://music.youtube.com/watch?v="+b || !strings.Contains(strings.Join(s.Actions, ","), "next,previous") {
		t.Fatalf("decorated %+v", s)
	}
	// another source's session: untouched, and its skips go to the device
	other := media.MediaSession{Source: "soloist", Metadata: media.MediaMetadata{Title: "Song " + b}, Actions: []string{"next"}}
	r.mu.Lock()
	r.sess = &other
	r.mu.Unlock()
	o := other
	r.svc.Decorate("d1", &o)
	if o.Provider != "" || o.WebURL != "" {
		t.Fatalf("decorated another source %+v", o)
	}
	if _, handled, _ := r.svc.Intercept(ctx, "d1", "media.player", "next", nil); handled {
		t.Fatal("intercepted soloist's next")
	}
}

func TestPlayMatchesASpotifySong(t *testing.T) {
	r := newRig(t)
	r.search = []providers.Track{
		{VideoID: x, Title: "Numb (Official Video)", DurationS: 205},
		{VideoID: y, Title: "In the End", DurationS: 187},
		{VideoID: a, Title: "Numb", DurationS: 188},
	}
	song := providers.Track{Source: providers.SourceSpotify, ID: "2nLt", Title: "Numb - Remastered", Artist: "Linkin Park", DurationS: 186}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{song}, 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != a {
		t.Fatalf("played %s", got)
	}
	r.search = []providers.Track{{VideoID: x, Title: "Numb", DurationS: 400}}
	if err := r.svc.Play(context.Background(), "d1", []providers.Track{song}, 0, music.Options{}); !errors.Is(err, music.ErrNotOnYouTube) {
		t.Fatalf("%v", err)
	}
}

func TestJumpAndRelayFailure(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_ = r.svc.Play(ctx, "d1", ids(a, b, c), 0, music.Options{})
	if err := r.svc.Jump(ctx, "d1", 2); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Jump(ctx, "d1", 1); !errors.Is(err, music.ErrQueueEnd) {
		t.Fatalf("%v", err)
	}
	if got := r.playedIDs(); got != a+","+c {
		t.Fatalf("played %s", got)
	}
	r.relayErr = errors.New("full")
	if err := r.svc.Skip(ctx, "d1", -1); err == nil || err.Error() != providers.CodePlayerFailed {
		t.Fatalf("%v", err)
	}
}

// a screen without audiod plays the queue on its own player: no media.url command, each song through the screen's
// path; its video session is the queue's (skips go through the queue) and its end plays the next one
func TestScreenWithoutAudiodPlaysOnItsPlayer(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	var screened []string
	r.svc.UseScreens(func(_ context.Context, dev string, tr providers.Track) (string, error) {
		if dev != "d1" {
			t.Errorf("screen play on %s", dev)
		}
		if tr.VideoID == x {
			return "", providers.ErrUnplayable
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		screened = append(screened, tr.VideoID)
		// the screen names the song by its video's title
		r.sess = &media.MediaSession{SessionID: "v" + tr.VideoID, Source: "video", State: "playing", DurationMs: 180_000,
			PositionAt: r.now.Format(time.RFC3339Nano), Metadata: media.MediaMetadata{Title: "Video of " + tr.VideoID}}
		return "Video of " + tr.VideoID, nil
	}, func(_ context.Context, dev string) bool { return dev == "d1" })
	if err := r.svc.Play(ctx, "d1", ids(x, a, b), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := r.playedIDs(); got != "" {
		t.Fatalf("media.url plays sent: %s", got)
	}
	if strings.Join(screened, ",") != a || !r.svc.Owns("d1") {
		t.Fatalf("screened %v, owns %v", screened, r.svc.Owns("d1"))
	}
	if _, handled, err := r.svc.Intercept(ctx, "d1", "media.player", "next", nil); !handled || err != nil {
		t.Fatalf("next: handled %v, %v", handled, err)
	}
	if strings.Join(screened, ",") != a+","+b {
		t.Fatalf("after next %v", screened)
	}
}

// regression: the device reported the song playing before the play command answered (a multiroom leader at once), so
// the queue forgot it, and the song that then played to its end unreported never advanced
func TestAutoAdvanceWhenTheOnlyReportCameBeforeThePlayAnswered(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.svc.Play(ctx, "d1", ids(a, b), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	r.advance(181 * time.Second)
	r.mu.Lock()
	r.sess = nil
	r.mu.Unlock()
	r.svc.SessionChanged("d1")
	deadline := time.Now().Add(2 * time.Second)
	for r.playedIDs() != a+","+b && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.playedIDs(); got != a+","+b {
		t.Fatalf("played %s", got)
	}
}
