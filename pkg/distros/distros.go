// Package distros reads the distro manifests in deploy/distros (TV plan W0-C
// item 7): what each distro builds, ships and depends on, with every pin. Data
// only for now; stream E's make dist and the packaging hooks read them.
package distros

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Manifest struct {
	Schema       string       `json:"$schema,omitempty"`
	Name         string       `json:"name"`
	Board        string       `json:"board"`
	Kind         string       `json:"kind"`
	Status       string       `json:"status"`
	OS           string       `json:"os"`
	Arch         []string     `json:"arch"`
	Package      Package      `json:"package"`
	Builder      Builder      `json:"builder"`
	Runtime      Runtime      `json:"runtime"`
	ServerURL    *string      `json:"serverUrl"`
	Components   []string     `json:"components"`
	Media        Media        `json:"media"`
	Dependencies []Dependency `json:"dependencies"`
}

type Package struct {
	Format string `json:"format"`
	Output string `json:"output"`
}

type Builder struct {
	Dockerfile string `json:"dockerfile"`
	Image      string `json:"image,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Command    string `json:"command,omitempty"`
}

type Runtime struct {
	Libc      string `json:"libc"`
	GlibcMax  string `json:"glibcMax,omitempty"`
	OpenSSL   string `json:"openssl,omitempty"`
	Loader    string `json:"loader,omitempty"`
	MinSDK    int    `json:"minSdk,omitempty"`
	TargetSDK int    `json:"targetSdk,omitempty"`
	NDK       string `json:"ndk,omitempty"`
}

type Media struct {
	URL          bool   `json:"url"`
	AirPlay      string `json:"airplay"`
	AudioBackend string `json:"audioBackend"`
	SoloistJail  bool   `json:"soloistJail,omitempty"`
	StaticLink   bool   `json:"staticLink,omitempty"`
	Screen       bool   `json:"screen"`
	Video        string `json:"video,omitempty"`
}

// Dependency is one library or runtime a distro takes, by Source: system
// (a distro's packages), vcpkg (a port at a baseline), prebuilt (an artifact
// by sha256), submodule (a commit) or tarball (an upstream source we build).
// Pending names what decides a pin a planned distro does not have yet.
type Dependency struct {
	Name         string            `json:"name"`
	Source       string            `json:"source"`
	Use          string            `json:"use"`
	License      string            `json:"license,omitempty"`
	Note         string            `json:"note,omitempty"`
	PinsFile     string            `json:"pinsFile,omitempty"`
	Pending      string            `json:"pending,omitempty"`
	Distro       string            `json:"distro,omitempty"`
	Packages     []string          `json:"packages,omitempty"`
	Snapshot     string            `json:"snapshot,omitempty"`
	Image        string            `json:"image,omitempty"`
	Port         string            `json:"port,omitempty"`
	Baseline     string            `json:"baseline,omitempty"`
	Triplet      string            `json:"triplet,omitempty"`
	URL          string            `json:"url,omitempty"`
	SHA256       string            `json:"sha256,omitempty"`
	SHA256ByHost map[string]string `json:"sha256ByHost,omitempty"`
	Version      string            `json:"version,omitempty"`
	Coordinate   string            `json:"coordinate,omitempty"`
	Script       string            `json:"script,omitempty"`
	Path         string            `json:"path,omitempty"`
	Repository   string            `json:"repository,omitempty"`
	Commit       string            `json:"commit,omitempty"`
	Patches      string            `json:"patches,omitempty"`
}

// Parse decodes one manifest, refusing fields it does not know.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	err := dec.Decode(&m)
	return m, err
}

// Load reads every manifest in dir (each *.json but schema.json), by name.
func Load(dir string) (map[string]Manifest, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := map[string]Manifest{}
	for _, p := range paths {
		if filepath.Base(p) == "schema.json" {
			continue
		}
		raw, err := os.ReadFile(p) //nolint:gosec // the repo's own manifests
		if err != nil {
			return nil, err
		}
		m, err := Parse(raw)
		if err != nil {
			return nil, err
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = m
	}
	return out, nil
}
