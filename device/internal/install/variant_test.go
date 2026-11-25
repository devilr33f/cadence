package install

import (
	"errors"
	"strings"
	"testing"
)

// a variant whose facts all hold replaces the profile's facts and adjusts the rest before recovery is checked: here
// it drops dropbear from the recovery daemons, so a device without one installs as tested
func TestVariantAdjustsTheProfile(t *testing.T) {
	f := v1Speaker(t)
	f.put("/usr/lib/stock/VERSION", []byte("3.22\n"), 0o644)
	for _, p := range f.pidsOf("/usr/sbin/dropbear") {
		delete(f.procs, p)
	}
	f.p.Variants = []Variant{
		{Name: "never", Facts: []Fact{{Name: "VERSION", Paths: []string{"/usr/lib/stock/VERSION"}, Want: "9.9"}}},
		{Name: "3.22", Facts: []Fact{{Name: "VERSION", Paths: []string{"/usr/lib/stock/VERSION"}, Want: "3.22"}},
			Apply: func(p *Profile) { p.RecoveryDaemons = []string{"adbd"} }},
	}
	out, err := install(t, f, runOpts(t, f))
	if err != nil || !strings.Contains(out, "firmware variant 3.22") || !strings.Contains(out, "firmware as tested") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// no variant holding keeps the profile: its facts still refuse
func TestNoVariantKeepsTheProfile(t *testing.T) {
	f := v1Speaker(t)
	f.put("/usr/lib/stock/VERSION", []byte("3.22\n"), 0o644)
	f.p.Variants = []Variant{{Name: "never", Facts: []Fact{{Name: "VERSION", Paths: []string{"/usr/lib/stock/VERSION"}, Want: "9.9"}},
		Apply: func(p *Profile) { p.Facts = nil }}}
	out, err := install(t, f, runOpts(t, f))
	if !errors.Is(err, ErrRefused) || strings.Contains(out, "firmware variant") ||
		!strings.Contains(err.Error(), "firmware differs from the tested one (VERSION)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}
