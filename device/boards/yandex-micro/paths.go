package micro

import "path/filepath"

// Paths are the small persistent tree and tmpfs runtime used by the Lite.
type Paths struct {
	DataRoot, RuntimeDir, Socket, Audiod, CABundle, CommandModel string
	Kmsg, Proc                                                   string
}

const (
	dataRoot   = "/data/cadence"
	runtimeDir = "/tmp/cadence"
	systemBus  = "/run/dbus/system_bus_socket"
)

func pathsUnder(root string) Paths {
	at := func(p string) string { return under(root, p) }
	return Paths{
		DataRoot: at(dataRoot), RuntimeDir: at(runtimeDir), Socket: at(runtimeDir + "/audiod.sock"),
		Audiod: at(dataRoot + "/bin/cadence-audiod"), CABundle: at(dataRoot + "/share/ca-certificates.crt"),
		CommandModel: at(dataRoot + "/stock/command"), Kmsg: at("/dev/kmsg"), Proc: at("/proc"),
	}
}

func under(root, p string) string {
	if root == "" {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}
