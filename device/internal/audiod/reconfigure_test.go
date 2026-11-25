package audiod_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func alsaOut(device string) *audiodv1.Configure {
	return &audiodv1.Configure{Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_ALSA, Device: device, Channels: 2}}
}

// a Reconfigure reaches the running audiod over a new connection, not a restart, and the level goes first again
// after it (rule 7: audiod's gate is shut by every Configure until a VolumeSet)
func TestReconfigureKeepsAudiodAndSendsTheLevelAgain(t *testing.T) {
	h := startSupervisorConfig(t, 35, audiod.Timings{}, alsaOut("bluealsa:DEV=00:00:00:00:00:01,PROFILE=a2dp"), nil)
	first := `backend=BACKEND_ALSA device="bluealsa:DEV=00:00:00:00:00:01,PROFILE=a2dp" mixer="" period=0 buffer=0`
	h.waitRecord(t, 1, "configure_playback", first)
	h.waitRecord(t, 1, "volume_set", "35")

	h.sup.Reconfigure(&audiodv1.Configure{})
	h.waitRecord(t, 1, "configure_playback", `backend=BACKEND_UNSPECIFIED device="" mixer="" period=0 buffer=0`)
	h.waitLog(t, "audiod_ready", 2)
	h.sup.SetVolume(20)
	h.sup.Reconfigure(alsaOut("bluealsa:DEV=00:00:00:00:00:02,PROFILE=a2dp"))
	h.waitRecord(t, 1, "configure_playback", `backend=BACKEND_ALSA device="bluealsa:DEV=00:00:00:00:00:02,PROFILE=a2dp" mixer="" period=0 buffer=0`)
	h.waitLog(t, "audiod_ready", 3)

	if pids := audiodtest.PIDs(h.dir); len(pids) != 1 {
		t.Fatalf("audiod started %d times", len(pids))
	}
	if h.log.count("audiod_exit") != 0 || h.log.count("audiod_reconfigure") != 2 {
		t.Fatalf("exits %d, reconfigures %d", h.log.count("audiod_exit"), h.log.count("audiod_reconfigure"))
	}
	// each connection's first frame after its Configure is the level in force
	var firsts []string
	seen := false
	for _, r := range audiodtest.Records(h.dir) {
		if seen {
			firsts = append(firsts, r.Kind+" "+r.Detail)
		}
		seen = r.Kind == "configure"
	}
	if !slices.Equal(firsts, []string{"volume_set 35", "volume_set 35", "volume_set 20"}) {
		t.Fatalf("first frames %q", firsts)
	}
}
