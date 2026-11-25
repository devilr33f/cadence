package midi

import "path/filepath"

// Paths are where cadence lives on a midi, the same as on the pickle. Everything kept goes under the data root on
// /data; the root filesystem gets nothing but the inittab line (rule 3), so audiod's binary is there too. The runtime
// dir is tmpfs.
type Paths struct {
	DataRoot   string
	RuntimeDir string
	// audiod's socket, in the runtime dir
	Socket string
	// audiod's binary, unless the config names another
	Audiod string
	// the CA bundle cadence ships (share/ in its tarball), for audiod and soloist's jail and cdn
	CABundle string
	// the owner's command model, pushed from outside the repo; never required
	CommandModel   string
	Soloist        string
	SoloistScratch string
	Airplay        string
	AirplayScratch string
	// the kernel log's device
	Kmsg string
	// the processes and the system's counters
	Proc string
}

const (
	dataRoot   = "/data/cadence"
	runtimeDir = "/tmp/cadence"
	systemBus  = "/run/dbus/system_bus_socket"
)

// pathsUnder are the paths on a device whose filesystem starts at root: "" on the speaker, a fake tree in tests
func pathsUnder(root string) Paths {
	at := func(p string) string { return under(root, p) }
	return Paths{
		DataRoot:       at(dataRoot),
		RuntimeDir:     at(runtimeDir),
		Socket:         at(runtimeDir + "/audiod.sock"),
		Audiod:         at(dataRoot + "/bin/cadence-audiod"),
		CABundle:       at(dataRoot + "/share/ca-certificates.crt"),
		CommandModel:   at(dataRoot + "/stock/command"),
		Soloist:        at(dataRoot + "/soloist"),
		SoloistScratch: at(runtimeDir + "/soloist"),
		Airplay:        at(dataRoot + "/airplay"),
		AirplayScratch: at(runtimeDir + "/airplay"),
		Kmsg:           at("/dev/kmsg"),
		Proc:           at("/proc"),
	}
}

// under puts the device's absolute path p under root; with no root it is p as the device names it, on any host
func under(root, p string) string {
	if root == "" {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}
