package torrserver_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/torrserver/torrservertest"
)

func rig(t *testing.T, lib torrservertest.Library, public string) (*torrserver.Provider, *torrservertest.Fake, string) {
	t.Helper()
	fake := torrservertest.New(lib)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := torrserver.NewClient(torrserver.ClientConfig{BaseURL: srv.URL, PublicURL: public, User: lib.User, Password: lib.Password})
	if err != nil {
		t.Fatal(err)
	}
	p := torrserver.New(c)
	p.Poll = 5 * time.Millisecond
	return p, fake, srv.URL
}

func movieFile() catalog.Key {
	return catalog.Key{Provider: catalog.ProviderTorrServer, Source: torrserver.SourceLibrary, Kind: catalog.KindFile,
		ID: torrservertest.MovieHash + "/1"}
}

func movie() catalog.Key {
	return catalog.Key{Provider: catalog.ProviderTorrServer, Source: torrserver.SourceLibrary, Kind: catalog.KindTorrent,
		ID: torrservertest.MovieHash}
}

func TestContract(t *testing.T) {
	lib := torrservertest.Scripted()
	lib.User, lib.Password = "owner", "secret"
	p, _, _ := rig(t, lib, "")
	catalogtest.Contract(t, p, catalogtest.Case{Query: "дюна", Item: movie(), Play: movieFile()})
}

func TestSearchAddPollAndPlay(t *testing.T) {
	p, fake, base := rig(t, torrservertest.Scripted(), "")
	ctx := context.Background()
	var mu sync.Mutex
	got := map[catalog.SourceID]catalog.SearchBatch{}
	if err := p.Search(ctx, catalog.SearchQuery{Query: "dune"}, func(b catalog.SearchBatch) error {
		mu.Lock()
		got[b.Source] = b
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[torrserver.SourceRutor].Items) != 1 || len(got[torrserver.SourceTorznab].Items) != 1 {
		t.Fatalf("batches %+v", got)
	}
	found := got[torrserver.SourceRutor].Items[0]
	if found.Torrent.Seeders != 120 || found.Torrent.SizeBytes < 30<<30 {
		t.Fatalf("found %+v", found.Torrent)
	}
	var stages []catalog.Stage
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: found.Key, OnStage: func(s catalog.Stage) { stages = append(stages, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.Known(torrservertest.FoundHash) || pl.Stream.URL != base+"/play/"+torrservertest.FoundHash+"/1" {
		t.Fatalf("url %q", pl.Stream.URL)
	}
	if len(stages) < 2 || stages[0] != catalog.StageConnectingPeers {
		t.Fatalf("stages %v", stages)
	}
}

func TestFilesSubtitlesAndPublicBase(t *testing.T) {
	p, _, _ := rig(t, torrservertest.Scripted(), "http://192.168.0.10:8090")
	ctx := context.Background()
	d, err := p.Item(ctx, catalog.ItemRequest{Key: catalog.Key{Provider: catalog.ProviderTorrServer,
		Source: torrserver.SourceLibrary, Kind: catalog.KindTorrent, ID: torrservertest.SeriesHash}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Children) != 4 || !d.Children[0].Playable || d.Children[3].Playable {
		t.Fatalf("children %+v", d.Children)
	}
	o, err := p.Options(ctx, catalog.OptionsRequest{Key: d.Item.Key})
	if err != nil || len(o.Groups[0].Choices) != 3 || o.Selected[catalog.GroupFile] != "1" {
		t.Fatalf("options %+v %v", o, err)
	}
	pl, err := p.Resolve(ctx, catalog.ResolveRequest{Key: movieFile(), From: catalog.FromResume})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pl.Stream.URL, "http://192.168.0.10:8090/play/") || len(pl.Stream.Subtitles) != 1 ||
		!strings.HasPrefix(pl.Stream.Subtitles[0].URL, "http://192.168.0.10:8090/subs/") {
		t.Fatalf("stream %+v", pl.Stream)
	}
	if err := p.Report(ctx, catalog.ProgressReport{Key: movieFile(), PositionS: 600}); err != nil {
		t.Fatal(err)
	}
	pl, _ = p.Resolve(ctx, catalog.ResolveRequest{Key: movieFile(), From: catalog.FromResume})
	if pl.Stream.ResumeMS != 600_000 {
		t.Fatalf("resume %d", pl.Stream.ResumeMS)
	}
}

func TestSearchOffAndAuth(t *testing.T) {
	lib := torrservertest.Scripted()
	lib.Torznab = nil
	p, _, _ := rig(t, lib, "")
	err := p.Search(context.Background(), catalog.SearchQuery{Query: "dune", Sources: []catalog.SourceID{torrserver.SourceTorznab}},
		func(b catalog.SearchBatch) error {
			if b.Failure != nil || len(b.Items) != 0 {
				t.Errorf("an off search %+v", b)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	lib = torrservertest.Scripted()
	lib.User, lib.Password = "owner", "secret"
	fake := torrservertest.New(lib)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, _ := torrserver.NewClient(torrserver.ClientConfig{BaseURL: srv.URL, User: "owner", Password: "wrong"})
	_, err = torrserver.New(c).Browse(context.Background(), catalog.BrowseRequest{})
	var f *catalog.Failure
	if !errors.As(err, &f) || f.Code != catalog.FailAuth {
		t.Fatalf("err %v", err)
	}
}
