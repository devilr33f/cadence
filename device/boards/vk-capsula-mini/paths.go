package vkcapsula

import "path/filepath"

// Paths are the persistent tree under /data and the tmpfs runtime of the capsula.
type Paths struct {
	DataRoot, RuntimeDir, Socket, Audiod, CABundle, Model, MuteState, AsoundConf string
	Kmsg, Proc                                                                   string
}

const (
	dataRoot   = "/data/cadence"
	runtimeDir = "/tmp/cadence"
)

func pathsUnder(root string) Paths {
	at := func(p string) string { return under(root, p) }
	return Paths{
		DataRoot: at(dataRoot), RuntimeDir: at(runtimeDir), Socket: at(runtimeDir + "/audiod.sock"),
		Audiod: at(dataRoot + "/bin/cadence-audiod"), CABundle: at(dataRoot + "/share/ca-certificates.crt"),
		Model: at(wakeModel), MuteState: at(dataRoot + "/state/mute"), Kmsg: at("/dev/kmsg"), Proc: at("/proc"),
		AsoundConf: at("/etc/asound.conf"),
	}
}

func under(root, p string) string {
	if root == "" {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}
