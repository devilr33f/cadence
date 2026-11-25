package midi_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func TestPaths(t *testing.T) {
	want := midi.Paths{
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
	if got := midi.PathsUnder(""); got != want {
		t.Fatalf("paths = %+v", got)
	}
	if got := midi.New("", nil).Paths(); got != want {
		t.Fatalf("board paths = %+v", got)
	}
}

// soloist runs on the 64-bit kernel beside the 32-bit userland: the board fetches spotify's arm64 build
func TestSoloistBuildIsArm64(t *testing.T) {
	p := midi.PathsUnder("")
	u := midi.SpotifyCDN(p)
	if u.URL != updater.SpotifyARM64 || u.Machine != updater.MachineAArch64 || u.CAFile != p.CABundle {
		t.Fatalf("updates = %+v", u)
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
	if got := midi.Configure("", true); !proto.Equal(got, want) {
		t.Fatalf("configure:\n%v\nwant:\n%v", prototext.Format(got), prototext.Format(want))
	}
	// without the owner's command model audiod is never pointed at one
	want.Capture.Engines.CommandModel = ""
	if got := midi.Configure("", false); !proto.Equal(got, want) {
		t.Fatalf("configure without a command model:\n%v", prototext.Format(got))
	}
}

// the command model counts once the owner pushed its directory; the paths follow the board's root
func TestConfigurePointsAtThePushedCommandModel(t *testing.T) {
	tr := midiTree(t)
	b, _ := newBoard(t, tr)
	p := b.Paths()
	if got := midi.ConfigureOf(b).GetCapture().GetEngines(); got.GetCommandModel() != "" ||
		got.GetStockRoots()[0] != tr.path("usr/lib/cadence-stock") || midi.ConfigureOf(b).GetCaFile() != p.CABundle {
		t.Fatalf("engines without a command model = %v", got)
	}
	tr.write("data/cadence/stock/command", "not a model directory")
	if got := midi.ConfigureOf(b).GetCapture().GetEngines().GetCommandModel(); got != "" {
		t.Fatalf("a file taken for the model: %q", got)
	}
	tr.remove("data/cadence/stock/command")
	tr.write("data/cadence/stock/command/words.txt", "")
	if got := midi.ConfigureOf(b).GetCapture().GetEngines().GetCommandModel(); got != p.CommandModel {
		t.Fatalf("command model = %q, want %q", got, p.CommandModel)
	}
}
