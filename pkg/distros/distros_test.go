package distros

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const root = "../.."

var dir = filepath.Join(root, "deploy", "distros")

func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("cadence:///distros/manifest.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("cadence:///distros/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // the repo's own files
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func TestManifestsHoldTheSchema(t *testing.T) {
	s := schema(t)
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	n := 0
	for _, p := range paths {
		if filepath.Base(p) == "schema.json" {
			continue
		}
		n++
		raw, err := os.ReadFile(p) //nolint:gosec // the repo's own manifests
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if err := s.Validate(inst); err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
	if n != 8 {
		t.Fatalf("%d manifests, want 8", n)
	}
}

func TestSchemaRefusesUnpinnedAndUnknown(t *testing.T) {
	s := schema(t)
	base := read(t, "deploy/distros/yandex-pickle.json")
	cases := map[string]string{
		"unknown field":    strings.Replace(base, `"kind": "speaker"`, `"kind": "speaker", "colour": "red"`, 1),
		"unpinned tarball": strings.Replace(base, `"sha256": "8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e",`, ``, 1),
		"unknown source":   strings.Replace(base, `"source": "tarball"`, `"source": "curl"`, 1),
		"speaker url":      strings.Replace(base, `"serverUrl": null`, `"serverUrl": "https://cadence.example"`, 1),
	}
	for name, raw := range cases {
		if raw == base {
			t.Fatalf("%s: replacement did nothing", name)
		}
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if s.Validate(inst) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// what a manifest names must be what the repo builds with: its files, base
// image, hashes and pins, so the data cannot drift from the builders
func TestManifestsMatchTheRepo(t *testing.T) {
	ms, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"android-tv", "rpi", "sim", "sim-tv", "vk-capsula-mini", "yandex-micro", "yandex-midi", "yandex-pickle"}
	var got []string
	for name := range ms {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("manifests %v, want %v", got, want)
	}
	for name, m := range ms {
		t.Run(name, func(t *testing.T) {
			if m.Name != name {
				t.Fatalf("name %q in %s.json", m.Name, name)
			}
			shipping := m.Status == "shipping"
			if shipping {
				if _, err := os.Stat(filepath.Join(root, "device", "boards", m.Board)); err != nil {
					t.Errorf("no board package for %s", m.Board)
				}
				docker := read(t, m.Builder.Dockerfile)
				if m.Builder.Image != "" && !strings.Contains(docker, m.Builder.Image) {
					t.Errorf("%s does not build on %s", m.Builder.Dockerfile, m.Builder.Image)
				}
			}
			for _, d := range m.Dependencies {
				if shipping && d.Pending != "" {
					t.Errorf("%s: a shipping distro with a pending pin", d.Name)
				}
				if d.Pending != "" {
					continue
				}
				if d.Script != "" {
					script := read(t, d.Script)
					for _, sum := range append([]string{d.SHA256}, mapValues(d.SHA256ByHost)...) {
						if sum != "" && !strings.Contains(script, sum) {
							t.Errorf("%s: %s does not hold its sha256", d.Name, d.Script)
						}
					}
					if d.Version != "" && !strings.Contains(script, d.Version) {
						t.Errorf("%s: %s does not hold version %s", d.Name, d.Script, d.Version)
					}
				}
				if d.PinsFile != "" {
					pins := read(t, d.PinsFile)
					for _, p := range append([]string{}, d.Packages...) {
						if !strings.Contains(pins, p) {
							t.Errorf("%s: %s does not pin %s", d.Name, d.PinsFile, p)
						}
					}
					if d.Source == "tarball" && !strings.Contains(pins, d.Name+" "+d.Version) {
						t.Errorf("%s: %s does not pin %s %s", d.Name, d.PinsFile, d.Name, d.Version)
					}
				}
				if shipping && d.Snapshot != "" && !strings.Contains(read(t, m.Builder.Dockerfile), d.Snapshot) {
					t.Errorf("%s: %s uses another snapshot than %s", d.Name, m.Builder.Dockerfile, d.Snapshot)
				}
				if shipping && d.Image != "" && !strings.Contains(read(t, m.Builder.Dockerfile), d.Image) {
					t.Errorf("%s: %s does not use %s", d.Name, m.Builder.Dockerfile, d.Image)
				}
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	out := []string{}
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
