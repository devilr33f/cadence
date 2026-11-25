package installer

import (
	_ "embed"

	"git.shino.boo/devilreef/cadence/device/internal/install"
)

// the OTA's two entry points, shadowed in the overlay by our own stubs: voice-agent runs fwupdater to check for and
// fetch firmware, and fwupdater hands an image to swupdate.sh, which writes the other slot and switches to it
var (
	//go:embed files/fwupdater.stub
	fwupdaterStub []byte
	//go:embed files/swupdate.sh.stub
	swupdateStub []byte
)

// hardening are the OTA stubs (rule 1): each writes its stub over the vendor file through the overlay and reverts by
// writing the lower copy's bytes back. --restore-stock leaves them in place
func hardening() []install.HardeningStep {
	return []install.HardeningStep{
		&install.OverlayStub{StepName: "fwupdater-stub", Path: "/usr/bin/fwupdater", Lower: rom + "/usr/bin/fwupdater", Content: fwupdaterStub},
		&install.OverlayStub{StepName: "swupdate-stub", Path: "/usr/bin/swupdate.sh", Lower: rom + "/usr/bin/swupdate.sh", Content: swupdateStub},
	}
}
