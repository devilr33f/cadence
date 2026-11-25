package install

import (
	"errors"
	"testing"
)

// the anchor is the line "a"; only lines that are exactly the entry count
func TestInittabEntries(t *testing.T) {
	const e = "::respawn:/x"
	lookalikes := "#" + e + "\n" + e + " -v\n" + e + "\r\n" + " a\n" + "a\n"
	for name, tc := range map[string]struct{ in, without, with string }{
		"middle":      {"a\n" + e + "\nb\n", "a\nb\n", "a\n" + e + "\nb\n"},
		"twice":       {e + "\na\n" + e + "\n", "a\n", "a\n" + e + "\n"},
		"last, bare":  {"a\n" + e, "a\n", "a\n" + e + "\n"},
		"lookalikes":  {lookalikes, lookalikes, lookalikes + e + "\n"},
		"anchor bare": {"x\na", "x\na", "x\na\n" + e + "\n"},
	} {
		if got := string(withoutEntry([]byte(tc.in), e)); got != tc.without {
			t.Errorf("%s: without = %q, want %q", name, got, tc.without)
		}
		if got, err := withEntry([]byte(tc.in), e, "a"); err != nil || string(got) != tc.with {
			t.Errorf("%s: with = %q (%v), want %q", name, got, err, tc.with)
		}
	}
	if _, err := withEntry([]byte(" a\nb\n"), e, "a"); !errors.Is(err, ErrRefused) {
		t.Fatalf("no anchor: %v", err)
	}
}

// v1's file is read exactly as v1's persisted() read it (ruling M2-R9): anything else gives no level, never a clamped one
func TestV1Level(t *testing.T) {
	for raw, want := range map[string]int{
		"45\n": 45, "45": 45, "0\n": 0, "100\n": 100, "007\n": 7, "000": 0, "099\n": 99,
	} {
		if got, ok := v1Level([]byte(raw)); !ok || got != want {
			t.Errorf("%q = %d, %v; want %d", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"", "\n", "-5\n", "4 5\n", "45\n\n", " 45\n", "4.5\n", "123456789", "45\x00", `{"level":45}`,
		"101\n", "150\n", "999", "0100\n", "1000", "9999999\n",
	} {
		if got, ok := v1Level([]byte(raw)); ok {
			t.Errorf("%q read as %d", raw, got)
		}
		if data, _, ok := toV2([]byte(raw)); ok || data != nil {
			t.Errorf("%q became %s", raw, data)
		}
	}
	if data, level, ok := toV1([]byte(`{"level":20}`)); !ok || level != 20 || string(data) != "20\n" {
		t.Fatalf("toV1 = %q, %d, %v", data, level, ok)
	}
	if _, _, ok := toV1([]byte(`{"level":101}`)); ok {
		t.Fatal("an out of range v2 level reached v1")
	}
}
