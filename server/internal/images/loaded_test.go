package images_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/images"
)

// an image the server holds itself (a device's uploaded cover) gets an id like a url's, read through its loader once
func TestRefLoadedServesWhatItsLoaderReads(t *testing.T) {
	p := images.New(images.Config{})
	reads := 0
	load := func() (string, []byte, error) {
		reads++
		return "image/png", []byte("png"), nil
	}
	id := p.RefLoaded("dev\x00hash", load)
	if !images.IDPattern.MatchString(id) || id != p.RefLoaded("dev\x00hash", load) || id == p.RefLoaded("dev\x00other", load) {
		t.Fatalf("id %q", id)
	}
	if p.RefLoaded("", load) != "" {
		t.Fatal("an empty key got an id")
	}
	for range 2 {
		if typ, data, err := p.Get(context.Background(), id); err != nil || typ != "image/png" || string(data) != "png" {
			t.Fatalf("%q %q %v", typ, data, err)
		}
	}
	if reads != 1 {
		t.Fatalf("read %d times", reads)
	}
}
