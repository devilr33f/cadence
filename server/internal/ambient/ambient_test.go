package ambient

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/i18n"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

// a synthetic manifest in Apple's shape: nothing of Apple's is committed
const (
	catSpace = "C-SPACE"
	catCity  = "C-CITY"
)

func syntheticEntries() map[string]any {
	asset := func(id, cat string, urls map[string]string, poi map[string]string) map[string]any {
		a := map[string]any{"id": id, "accessibilityLabel": "Label " + id, "categories": []string{cat},
			"pointsOfInterest": poi, "shotID": "S_" + id}
		for k, v := range urls {
			a[k] = v
		}
		return a
	}
	all := func(id string) map[string]string {
		return map[string]string{
			"url-1080-H264": "https://cdn.example/" + id + "_2K_AVC.mov",
			"url-1080-SDR":  "https://cdn.example/" + id + "_SDR_2K_HEVC.mov",
			"url-1080-HDR":  "https://cdn.example/" + id + "_HDR_2K_HEVC.mov",
			"url-4K-SDR":    "https://cdn.example/" + id + "_SDR_4K_HEVC.mov",
			"url-4K-HDR":    "https://cdn.example/" + id + "_HDR_4K_HEVC.mov",
		}
	}
	return map[string]any{
		"version": 1,
		"categories": []map[string]any{
			{"id": catSpace, "localizedNameKey": "AerialCategorySpace", "preferredOrder": 0},
			{"id": catCity, "localizedNameKey": "AerialCategoryCities", "preferredOrder": 2},
		},
		"assets": []map[string]any{
			asset("A1", catSpace, all("A1"), map[string]string{"0": "A1_0", "120": "A1_120"}),
			asset("A2", catCity, all("A2"), map[string]string{"0": "A2_0", "x": "bad"}),
			// h264 only
			asset("A3", catCity, map[string]string{"url-1080-H264": "https://cdn.example/A3.mov"}, nil),
			// no https url: left out
			asset("A4", catCity, map[string]string{"url-1080-H264": "http://cdn.example/A4.mov"}, nil),
		},
	}
}

func syntheticTar(t *testing.T, entries map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	add("entries.json", data)
	_ = tw.WriteHeader(&tar.Header{Name: "TVIdleScreenStrings.bundle/", Typeflag: tar.TypeDir, Mode: 0o755})
	add(stringsFile(i18n.En), encodeStrings(map[string]string{"A1_0": "Over the Earth", "A1_120": "Over the sea",
		"A2_0": "Over a city", "AerialCategorySpace": "Earth"}))
	add(stringsFile(i18n.Ru), encodeStrings(map[string]string{"A1_0": "Над Землёй", "A2_0": "Над городом"}))
	add("TVIdleScreenStrings.bundle/de.lproj/Localizable.nocache.strings", []byte("ignored"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeApple serves the tar over tls with an etag, and counts full and conditional answers
type fakeApple struct {
	tar        []byte
	full, cond atomic.Int32
	status     atomic.Int32
}

func (f *fakeApple) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s := f.status.Load(); s != 0 {
		w.WriteHeader(int(s))
		return
	}
	switch r.URL.Path {
	case "/Aerials/resources-16.tar":
	case "/elsewhere":
		http.Redirect(w, r, "https://localhost"+strings.TrimPrefix(r.Host, "127.0.0.1")+"/Aerials/resources-16.tar",
			http.StatusFound)
		return
	default:
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("If-None-Match") == `"v1"` {
		f.cond.Add(1)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f.full.Add(1)
	w.Header().Set("ETag", `"v1"`)
	_, _ = w.Write(f.tar)
}

func newFake(t *testing.T) (*fakeApple, *httptest.Server) {
	t.Helper()
	f := &fakeApple{tar: syntheticTar(t, syntheticEntries())}
	ts := httptest.NewTLSServer(f)
	t.Cleanup(ts.Close)
	return f, ts
}

func fetcherFor(t *testing.T, ts *httptest.Server, path string) *Fetcher {
	t.Helper()
	f, err := NewFetcher(FetcherConfig{URL: ts.URL + path, Hosts: []string{"127.0.0.1"}, Client: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFetchParsesTheFakeHostsTar(t *testing.T) {
	fake, ts := newFake(t)
	f := fetcherFor(t, ts, "/Aerials/resources-16.tar")
	if f.Manifest() != nil {
		t.Fatal("a manifest before any fetch")
	}
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := f.Manifest()
	if len(m.Clips) != 3 {
		t.Fatalf("%d clips, want 3 (A4 has no https url)", len(m.Clips))
	}
	a1 := m.Clips[0]
	if a1.ID != "A1" || a1.Category != "space" || len(a1.URLs) != 5 ||
		len(a1.Points) != 2 || a1.Points[1] != (Point{AtS: 120, Key: "A1_120"}) {
		t.Errorf("A1: %+v", a1)
	}
	if m.Clips[1].Category != "cities" || len(m.Clips[1].Points) != 1 {
		t.Errorf("A2: %+v", m.Clips[1])
	}
	if m.Caption(i18n.Ru, "A1_0") != "Над Землёй" || m.Caption(i18n.Ru, "A1_120") != "Over the sea" ||
		m.Caption(i18n.Pseudo, "A2_0") != "Over a city" || m.Caption(i18n.En, "none") != "" {
		t.Error("captions")
	}
	// again: a conditional request, the manifest kept
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.full.Load() != 1 || fake.cond.Load() != 1 || f.Manifest() != m {
		t.Errorf("full %d cond %d", fake.full.Load(), fake.cond.Load())
	}
	// a failure keeps the last good one
	fake.status.Store(http.StatusInternalServerError)
	if err := f.Fetch(context.Background()); err == nil || failureCode(err) != "status" {
		t.Errorf("err %v", err)
	}
	if f.Manifest() != m {
		t.Error("lost the manifest on a failure")
	}
}

func TestFetchKeepsToItsHosts(t *testing.T) {
	_, ts := newFake(t)
	// a redirect to another host name of the same server
	f := fetcherFor(t, ts, "/elsewhere")
	err := f.Fetch(context.Background())
	if !errors.Is(err, errHost) || failureCode(err) != "host" {
		t.Fatalf("err %v", err)
	}
	for _, u := range []string{"http://sylvan.apple.com/x.tar", "https://evil.example/x.tar",
		"https://user@sylvan.apple.com/x.tar"} {
		if _, err := NewFetcher(FetcherConfig{URL: u}); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	if f, err := NewFetcher(FetcherConfig{}); err != nil || f.url != ManifestURL {
		t.Errorf("default: %v", err)
	}
}

func TestParseTarRefuses(t *testing.T) {
	for name, entries := range map[string]map[string]any{
		"no clips": {"assets": []any{}},
		"bad json": nil,
	} {
		var data []byte
		if entries == nil {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{Name: "entries.json", Size: 1, Mode: 0o644, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("{"))
			_ = tw.Close()
			data = buf.Bytes()
		} else {
			data = syntheticTar(t, entries)
		}
		if _, err := ParseTar(bytes.NewReader(data)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, err := ParseTar(strings.NewReader("not a tar")); err == nil {
		t.Error("garbage parsed")
	}
}

func manifestOf(t *testing.T) *Manifest {
	t.Helper()
	m, err := ParseTar(bytes.NewReader(syntheticTar(t, syntheticEntries())))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRenditionPerProfile(t *testing.T) {
	m := manifestOf(t)
	a1, a3 := m.Clips[0], m.Clips[2]
	for _, tc := range []struct {
		name string
		p    Profile
		clip Clip
		want Rendition
	}{
		{"stick", Profile{MaxHeight: 1080, Codecs: []string{"h264", "hevc"}}, a1, SDR1080},
		{"stick asking 4k", Profile{MaxHeight: 1080, Codecs: []string{"hevc"}, Quality: "4k"}, a1, SDR1080},
		{"4k screen", Profile{MaxHeight: 2160, Codecs: []string{"HEVC"}}, a1, SDR1080},
		{"4k screen asking 4k", Profile{MaxHeight: 2160, Codecs: []string{"h265"}, Quality: "4k"}, a1, SDR4K},
		{"h264 only", Profile{MaxHeight: 1080, Codecs: []string{"h264"}}, a1, H264_1080},
		{"unknown profile", Profile{}, a1, H264_1080},
		{"hevc screen, h264 clip", Profile{MaxHeight: 2160, Codecs: []string{"hevc"}, Quality: "4k"}, a3, H264_1080},
	} {
		r, u, ok := tc.p.choose(tc.clip)
		if !ok || r != tc.want || u != tc.clip.URLs[tc.want] {
			t.Errorf("%s: %s %s", tc.name, r, u)
		}
	}
	if _, _, ok := (Profile{}).choose(Clip{URLs: map[Rendition]string{HDR4K: "https://x/y"}}); ok {
		t.Error("an hdr-only clip chosen")
	}
}

func newService(m *Manifest, owner []OwnerImage, art Artwork) *Service {
	return New(Config{Manifest: func() *Manifest { return m }, OwnerImages: func() []OwnerImage { return owner },
		Artwork: art, Rand: rand.New(rand.NewPCG(1, 2))})
}

var stick = Profile{MaxHeight: 1080, Codecs: []string{"h264", "hevc"}}

func TestNextClip(t *testing.T) {
	s := newService(manifestOf(t), nil, nil)
	d := Device{ID: "tv1", Language: "ru-RU", Profile: stick, Categories: []string{"space"}}
	n, err := s.Next(context.Background(), d, NextArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "video" || n.ID != "A1" || n.Category != "space" || !strings.HasSuffix(n.URL, "A1_SDR_2K_HEVC.mov") {
		t.Errorf("%+v", n)
	}
	// ru where it has the key, en where not
	want := []Caption{{0, "Над Землёй"}, {120, "Over the sea"}}
	if len(n.Captions) != 2 || n.Captions[0] != want[0] || n.Captions[1] != want[1] {
		t.Errorf("captions %+v", n.Captions)
	}
}

func TestNextAvoidsRepeats(t *testing.T) {
	s := newService(manifestOf(t), nil, nil)
	d := Device{ID: "tv1", Profile: stick}
	seen := map[string]int{}
	var last string
	for range 30 {
		n, err := s.Next(context.Background(), d, NextArgs{})
		if err != nil {
			t.Fatal(err)
		}
		if n.ID == last {
			t.Fatalf("%s twice in a row", n.ID)
		}
		last = n.ID
		seen[n.ID]++
	}
	if len(seen) != 3 {
		t.Errorf("seen %v", seen)
	}
	// one clip alone repeats
	one := newService(manifestOf(t), nil, nil)
	for range 3 {
		if n, err := one.Next(context.Background(), Device{ID: "x", Profile: stick, Categories: []string{"space"}},
			NextArgs{}); err != nil || n.ID != "A1" {
			t.Fatal(n, err)
		}
	}
}

type fakeArtwork struct {
	images []Image
	err    error
}

func (f fakeArtwork) Backdrops(context.Context, string, int) ([]Image, error) { return f.images, f.err }

func TestNextImages(t *testing.T) {
	owner := []OwnerImage{{URL: "https://img.example/a.jpg", Caption: "Дача"}, {URL: "http://img.example/b.jpg"}}
	art := fakeArtwork{images: []Image{{URL: "/api/v1/images/abc"}}}
	s := newService(nil, owner, art)
	n, err := s.Next(context.Background(), Device{ID: "tv", Source: SourceImages}, NextArgs{})
	if err != nil || n.Kind != "image" || n.URL != owner[0].URL || len(n.Captions) != 1 || n.Captions[0].Text != "Дача" {
		t.Errorf("images: %+v %v", n, err)
	}
	n, err = s.Next(context.Background(), Device{ID: "tv", Source: SourceArtwork}, NextArgs{})
	if err != nil || n.URL != "/api/v1/images/abc" || n.Captions != nil {
		t.Errorf("artwork: %+v %v", n, err)
	}
	// mixed with no manifest falls back to images
	for range 5 {
		n, err = s.Next(context.Background(), Device{ID: "tv", Source: SourceMixed}, NextArgs{})
		if err != nil || n.Kind != "image" {
			t.Errorf("mixed: %+v %v", n, err)
		}
	}
	if _, err := newService(nil, nil, fakeArtwork{err: errors.New("down")}).Next(context.Background(),
		Device{Source: SourceArtwork}, NextArgs{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("artwork down: %v", err)
	}
	if err := CheckOwnerImages(owner); err == nil {
		t.Error("an http owner image passed")
	}
}

func TestHandle(t *testing.T) {
	s := newService(manifestOf(t), nil, NoArtwork{})
	d := Device{ID: "tv", Language: "en", Profile: stick}
	for _, tc := range []struct {
		dev        Device
		args, want string
	}{
		{d, ``, rpc.StatusOK},
		{d, `{}`, rpc.StatusOK},
		{d, `{"kind":"aerial"}`, rpc.StatusOK},
		{d, `{"kind":"netflix"}`, rpc.StatusInvalid},
		{d, `{"kind":`, rpc.StatusInvalid},
		{d, `{"kind":"images"}`, rpc.StatusUnavailable},
		{Device{ID: "tv", Source: SourceOff}, ``, rpc.StatusRefused},
	} {
		status, body := s.Handle(context.Background(), tc.dev, []byte(tc.args))
		if status != tc.want {
			t.Errorf("%s: %s %s", tc.args, status, body)
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Errorf("%s: %v", tc.args, err)
		}
		if status == rpc.StatusOK {
			if u, err := url.Parse(out["url"].(string)); err != nil || u.Scheme != "https" || out["kind"] != "video" {
				t.Errorf("%s: %s", tc.args, body)
			}
		} else if _, ok := out["code"]; !ok || len(out) != 1 {
			t.Errorf("%s: %s", tc.args, body)
		}
	}
	if status, _ := New(Config{}).Handle(context.Background(), d, nil); status != rpc.StatusUnavailable {
		t.Errorf("no manifest: %s", status)
	}
}
