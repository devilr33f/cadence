package pickle_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func TestPaths(t *testing.T) {
	want := pickle.Paths{
		DataRoot:       "/data/cadence",
		RuntimeDir:     "/tmp/cadence",
		Socket:         "/tmp/cadence/audiod.sock",
		Audiod:         "/data/cadence/bin/cadence-audiod",
		CABundle:       "/data/cadence/share/ca-certificates.crt",
		CommandModel:   "/data/cadence/stock/command",
		Soloist:        "/data/cadence/soloist",
		SoloistScratch: "/tmp/cadence/soloist",
		Airplay:        "/data/cadence/airplay",
		AirplayScratch: "/tmp/cadence/airplay",
		Kmsg:           "/dev/kmsg",
		Proc:           "/proc",
	}
	if got := pickle.PathsUnder(""); got != want {
		t.Fatalf("paths = %+v", got)
	}
	if got := pickle.New("", nil).Paths(); got != want {
		t.Fatalf("board paths = %+v", got)
	}
}

func golden(t *testing.T) *audiodv1.Configure {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "configure.txtpb"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg audiodv1.Configure
	if err := prototext.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestConfigureCarriesV1Values(t *testing.T) {
	want := golden(t)
	if got := pickle.Configure("", true); !proto.Equal(got, want) {
		t.Fatalf("configure:\n%v\nwant:\n%v", prototext.Format(got), prototext.Format(want))
	}
	// without the owner's command model audiod is never pointed at one
	want.Capture.Engines.CommandModel = ""
	if got := pickle.Configure("", false); !proto.Equal(got, want) {
		t.Fatalf("configure without a command model:\n%v", prototext.Format(got))
	}
}

// the command model counts once the owner pushed its directory; the paths follow the board's root
func TestConfigurePointsAtThePushedCommandModel(t *testing.T) {
	tr := pickleTree(t)
	b, _ := newBoard(t, tr)
	p := b.Paths()
	if got := pickle.ConfigureOf(b).GetCapture().GetEngines(); got.GetCommandModel() != "" ||
		got.GetStockRoots()[0] != tr.path("usr/lib/cadence-stock") || pickle.ConfigureOf(b).GetCaFile() != p.CABundle {
		t.Fatalf("engines without a command model = %v", got)
	}
	tr.write("data/cadence/stock/command", "not a model directory")
	if got := pickle.ConfigureOf(b).GetCapture().GetEngines().GetCommandModel(); got != "" {
		t.Fatalf("a file taken for the model: %q", got)
	}
	tr.remove("data/cadence/stock/command")
	tr.write("data/cadence/stock/command/words.txt", "")
	if got := pickle.ConfigureOf(b).GetCapture().GetEngines().GetCommandModel(); got != p.CommandModel {
		t.Fatalf("command model = %q, want %q", got, p.CommandModel)
	}
}
