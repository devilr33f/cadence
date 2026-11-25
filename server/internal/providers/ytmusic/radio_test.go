package ytmusic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytmusic"
)

// the fixture is a trimmed live radio of "Numb": the seed first, then its mix
func TestRadioAnswersTheMixAfterTheSeed(t *testing.T) {
	body, err := os.ReadFile("testdata/radio.json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		VideoID, PlaylistID string
		IsAudioOnly         bool
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/youtubei/v1/next" {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := ytmusic.New(ytmusic.Config{BaseURL: srv.URL}, probetest.Client(srv))
	res, err := c.Radio(context.Background(), "5qZQEq_C3vc", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoID != "5qZQEq_C3vc" || got.PlaylistID != "RDAMVM5qZQEq_C3vc" || !got.IsAudioOnly {
		t.Fatalf("request %+v", got)
	}
	if len(res) != 3 {
		t.Fatalf("%d tracks", len(res))
	}
	first := res[0]
	first.ArtworkURL = ""
	want := providers.Track{Source: "youtube", ID: "BLZWkjBXfN8", VideoID: "BLZWkjBXfN8", Title: "In the End", Artist: "Linkin Park", Album: "Hybrid Theory (Bonus Edition)", DurationS: 217, WebURL: "https://music.youtube.com/watch?v=BLZWkjBXfN8"}
	if first != want {
		t.Fatalf("first %+v", first)
	}
	if res[1].Title != "Bring Me To Life" || res[1].Artist != "Evanescence" {
		t.Fatalf("second %+v", res[1])
	}
	if _, err := c.Radio(context.Background(), "--bad", 3); err == nil {
		t.Fatal("radio of a bad id")
	}
}
