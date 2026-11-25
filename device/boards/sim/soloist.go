package sim

import (
	"path/filepath"
	"runtime"
	"strconv"

	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

// the runtime image has no openssl default paths, only this bundle
const caBundle = "/etc/ssl/certs/ca-certificates.crt"

// SoloistCDN is where the sim's soloist updater gets its builds: the e2e's
// fake CDN. The sim never runs Spotify's build, so without it nothing is
// downloaded.
type SoloistCDN struct {
	URL string
	// CAFile alone verifies the CDN
	CAFile string
	// Machine is the builds' ELF e_machine; zero is the sim's own cpu's
	Machine uint16
}

func WithSoloistCDN(c SoloistCDN) Option { return func(s *sim) { s.soloistCDN = &c } }

// SoloistCDNFromEnv is the e2e's fake CDN: CADENCE_SIM_SOLOIST_CDN (the
// tarball's https url), CADENCE_SIM_SOLOIST_CDN_CA and
// CADENCE_SIM_SOLOIST_MACHINE (decimal; anything else is the sim's own cpu).
// Without a url there is none.
func SoloistCDNFromEnv(getenv func(string) string) (SoloistCDN, bool) {
	url := getenv("CADENCE_SIM_SOLOIST_CDN")
	if url == "" {
		return SoloistCDN{}, false
	}
	machine, err := strconv.ParseUint(getenv("CADENCE_SIM_SOLOIST_MACHINE"), 10, 16)
	if err != nil {
		machine = 0
	}
	return SoloistCDN{URL: url, CAFile: getenv("CADENCE_SIM_SOLOIST_CDN_CA"), Machine: uint16(machine)}, true
}

// soloistOptions keeps soloist's builds and runtime under the data dir and
// its scratch beside audiod's socket; no slider moves the level while a ring sounds
func (s *sim) soloistOptions() soloist.Options {
	o := soloist.Options{
		Dir:        filepath.Join(s.dataDir, "soloist"),
		ScratchDir: filepath.Join(s.dataDir, "run", "soloist"),
		CAFile:     caBundle,
	}
	if c := s.soloistCDN; c != nil {
		machine := c.Machine
		if machine == 0 {
			machine = map[string]uint16{"amd64": updater.MachineX8664, "arm64": updater.MachineAArch64}[runtime.GOARCH]
		}
		o.Updates = &soloist.Updates{URL: c.URL, CAFile: c.CAFile, Machine: machine}
	}
	if s.ringer != nil {
		o.Ringing = s.ringer.Ringing
	}
	return o
}
