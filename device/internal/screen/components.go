package screen

import (
	"bufio"
	"os"
	"strings"
)

// the distro manifest's components (deploy/distros/schema.json) the screen's capabilities stand on
const (
	// display.screen, media.video, media.mpv and input.remote
	ComponentScreen = "cadence-screen"
	// media.screencast
	ComponentAirplay = "libairplay"
)

// Components are what an artifact shipped; only their capabilities are advertised. The zero value ships everything:
// a sim or a development build has no MANIFEST.
type Components struct {
	set map[string]bool
}

// ComponentsOf is a set of the components named.
func ComponentsOf(names ...string) Components {
	c := Components{set: map[string]bool{}}
	for _, n := range names {
		c.set[n] = true
	}
	return c
}

// Has is whether the artifact shipped name.
func (c Components) Has(name string) bool { return c.set == nil || c.set[name] }

// ReadComponents is the packaging hook: the distro's components, as `make dist` writes them into the artifact's
// MANIFEST, one header line "# components <name> <name>...". A MANIFEST that is missing, or has no such line, ships
// everything (a sim, a development build); one that lists them ships only those.
func ReadComponents(manifest string) Components {
	f, err := os.Open(manifest) //nolint:gosec // the board's own install path
	if err != nil {
		return Components{}
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "#"))
		if len(fields) > 0 && fields[0] == "components" {
			return ComponentsOf(fields[1:]...)
		}
	}
	return Components{}
}
